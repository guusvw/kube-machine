package detector

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"text/template"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/provision"
	"github.com/golang/glog"
	"github.com/kube-node/kube-machine/pkg/nodeclass"
)

type NodeClassProvisionerWrapper struct {
	provision.Provisioner
}

type KubeMachineProvisioner interface {
	provision.Provisioner
	ProvisionConfig(config *nodeclass.NodeClassConfig) error
}

func DetectProvisioner(driver drivers.Driver) (KubeMachineProvisioner, error) {
	p, err := provision.DetectProvisioner(driver)
	if err != nil {
		return nil, err
	}

	return &NodeClassProvisionerWrapper{p}, nil
}

func (p *NodeClassProvisionerWrapper) ProvisionConfig(config *nodeclass.NodeClassConfig) error {
	for _, f := range config.Provisioning.Files {
		if err := p.scp([]byte(f.Content), f.Path, f.Permissions, f.Owner); err != nil {
			return fmt.Errorf("failed to create file %q: %v", f.Path, err)
		}
	}

	for _, c := range config.Provisioning.Commands {
		glog.V(6).Infof("Executing command %q", c)
		out, err := p.SSHCommand(c)
		glog.V(6).Infof("Output %q", out)
		if err != nil {
			return fmt.Errorf("failed to execute command %q: %v", c, err)
		}
	}

	return nil
}

func (p *NodeClassProvisionerWrapper) scp(data []byte, path string, chmod string, owner string) error {
	data64 := base64.StdEncoding.EncodeToString(data)

	ctx := struct {
		Path, Data64, Chmod, Chown string
	}{
		Path:   path,
		Data64: data64,
		Chmod:  chmod,
		Chown:  owner,
	}
	cmd := &bytes.Buffer{}
	cmdTmpl := template.New("cmd")
	cmdTmpl.Parse(`
sudo mkdir -p "$(dirname "{{.Path}}")" && \
sudo touch {{.Path}} && \
sudo chown {{.Chown}} {{.Path}} && \
sudo chmod 777 {{.Path}} && \
echo "{{.Data64}}" | base64 -d >> {{.Path}} && \
sudo chmod {{.Chmod}} {{.Path}}`)
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
