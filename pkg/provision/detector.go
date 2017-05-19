package detector

import (
	"encoding/base64"
	"fmt"
	"io/ioutil"
	"text/template"

	"bytes"
	"github.com/docker/machine/libmachine/auth"
	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/engine"
	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/provision"
	"github.com/docker/machine/libmachine/swarm"
)

const (
	nodeKubeconfigPath = "/etc/kubeconfig"
	kubeletUnitPath    = "/etc/systemd/system/kubelet.service"
	kubeletUnitFile    = `[Unit]
Description=Kubernetes Kubelet

[Service]
Restart=always
RestartSec=10
Environment="PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin:/opt/bin"
ExecStartPre=/usr/bin/mkdir -p /var/lib/kubelet /var/run/kubernetes
ExecStartPre=/usr/bin/curl -L -o /var/lib/kubelet/kubelet https://storage.googleapis.com/kubernetes-release/release/v1.5.3/bin/linux/amd64/kubelet
ExecStartPre=/usr/bin/chmod +x /var/lib/kubelet/kubelet
ExecStartPre=/usr/bin/mkdir -p /opt/bin
ExecStartPre=/usr/bin/curl -L -o /opt/bin/socat https://s3-eu-west-1.amazonaws.com/kubermatic/coreos/socat
ExecStartPre=/usr/bin/chmod +x /opt/bin/socat
ExecStart=/var/lib/kubelet/kubelet \
  --address=0.0.0.0 \
  --anonymous-auth=false \
  --kubeconfig=/etc/kubeconfig \
  --require-kubeconfig \
  --cluster-dns=10.10.10.10 \
  --cluster-domain=cluster.local \
  --allow-privileged=true \
  --client-ca-file=/etc/ssl/etcd/root-ca.crt \
  --hostname-override=207.154.215.45 \
  --v=2 \
  --logtostderr=true \
  --network-plugin=cni
[Install]
WantedBy=multi-user.target
`
)

type ExtendedKubeProvisionerDetector struct {
	provision.Detector
	KubeconfigPath string
}

type KubeletProvisionerWrapper struct {
	provision.Provisioner
	KubeconfigPath string
}

func (d *ExtendedKubeProvisionerDetector) DetectProvisioner(driver drivers.Driver) (provision.Provisioner, error) {
	p, err := d.Detector.DetectProvisioner(driver)
	if err != nil {
		return nil, err
	}

	return &KubeletProvisionerWrapper{p, d.KubeconfigPath}, nil
}

func (p *KubeletProvisionerWrapper) Provision(swarmOptions swarm.Options, authOptions auth.Options, engineOptions engine.Options) error {
	err := p.Provisioner.Provision(swarmOptions, authOptions, engineOptions)
	if err != nil {
		return err
	}

	data, err := ioutil.ReadFile(p.KubeconfigPath)
	if err != nil {
		return err
	}

	log.Infof("Copying %q to %q on the node...", p.KubeconfigPath, nodeKubeconfigPath)
	err = p.scp(data, nodeKubeconfigPath, "0600")
	if err != nil {
		return err
	}

	log.Infof("Copying %q to %q on the node...", "kubelet unit file", kubeletUnitPath)
	err = p.scp([]byte(kubeletUnitFile), kubeletUnitPath, "0600")
	if err != nil {
		return err
	}

	return nil
}

func (p *KubeletProvisionerWrapper) scp(data []byte, path string, chmod string) error {
	data64 := base64.StdEncoding.EncodeToString(data)

	ctx := struct {
		Path, Data64, Chmod string
	}{
		Path:   nodeKubeconfigPath,
		Data64: data64,
		Chmod:  chmod,
	}
	cmd := &bytes.Buffer{}
	cmdTmpl := template.New(`touch {{.Path}} && chmod {{.Chmod}} {{.Path}} && echo "{{.Data64}}" | base64 -d >> {{.Path}}`)
	err := cmdTmpl.Execute(cmd, ctx)
	if err != nil {
		return err
	}
	out, err := p.Provisioner.SSHCommand(cmd.String())
	if err != nil {
		return fmt.Errorf("Failed to run SSH command (error: %v): %v", err, out)
	}
	return nil
}
