package main

import (
	"encoding/json"
	"fmt"

	"github.com/docker/machine/libmachine/drivers"
	mlog "github.com/docker/machine/libmachine/log"
	mssh "github.com/docker/machine/libmachine/ssh"
	"github.com/kube-node/kube-machine/pkg/libmachine"
	"github.com/kube-node/kube-machine/pkg/options"
	"k8s.io/client-go/pkg/api/v1"
)

func main() {
	//Enable debug logs on docker-machine
	mlog.SetDebug(true)

	//Is default on docker-machine. Lets stick to defaults.
	mssh.SetDefaultClient(mssh.External)

	api := libmachine.New()

	rawDriver, err := json.Marshal(&drivers.BaseDriver{
		MachineName: "node1",
	})
	if err != nil {
		panic(fmt.Errorf("Error attempting to marshal bare driver data: %s", err).Error())
	}

	host, err := api.NewHost("digitalocean", rawDriver)
	if err != nil {
		panic(err.Error())
	}

	n := v1.Node{}
	n.ObjectMeta.Annotations = map[string]string{
		"node.k8s.io/digitalocean-access-token":        "",
		"node.k8s.io/digitalocean-ssh-key-fingerprint": "",
	}
	ops := options.New(n)
	host.Driver.SetConfigFromFlags(ops)
	err = api.Create(host)
	if err != nil {
		panic(err.Error())
	}
}
