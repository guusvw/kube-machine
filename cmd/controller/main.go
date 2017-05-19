package main

import (
	goflag "flag"
	"fmt"
	"time"

	"github.com/docker/machine/libmachine/log"
	"github.com/docker/machine/libmachine/ssh"
	"github.com/golang/glog"
	"github.com/kube-node/kube-machine/pkg/controller/node"
	"github.com/kube-node/kube-machine/pkg/libmachine"
	flag "github.com/spf13/pflag"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/client-go/util/workqueue"
)

var kubeconfig *string = flag.String("kubeconfig", "", "Path to kubeconfig")

func main() {
	flag.CommandLine.AddGoFlagSet(goflag.CommandLine)
	flag.Parse()
	log.SetDebug(true)

	var config *rest.Config
	var err error
	if *kubeconfig != "" {
		glog.V(6).Infof("Using local kubeconfig located at %q", *kubeconfig)

		config, err = clientcmd.BuildConfigFromFlags("", *kubeconfig)
		if err != nil {
			panic(err.Error())
		}
	} else {
		glog.V(6).Info("Using in-cluster config")
		config, err = rest.InClusterConfig()
		if err != nil {
			panic(err.Error())
		}
	}
	client := kubernetes.NewForConfigOrDie(config)

	queue := workqueue.NewRateLimitingQueue(workqueue.DefaultControllerRateLimiter())

	indexer, informer := cache.NewIndexerInformer(
		&cache.ListWatch{
			ListFunc: func(options metav1.ListOptions) (runtime.Object, error) {
				options.LabelSelector = "node.k8s.io/controller=kube-machine"
				return client.Nodes().List(options)
			},
			WatchFunc: func(options metav1.ListOptions) (watch.Interface, error) {
				options.LabelSelector = "node.k8s.io/controller=kube-machine"
				return client.Nodes().Watch(options)
			},
		},
		&v1.Node{},
		5*time.Minute,
		cache.ResourceEventHandlerFuncs{
			AddFunc: func(obj interface{}) {
				key, err := cache.MetaNamespaceKeyFunc(obj)
				if err == nil {
					queue.Add(key)
				}
			},
			UpdateFunc: func(old interface{}, new interface{}) {
				key, err := cache.MetaNamespaceKeyFunc(new)
				if err == nil {
					queue.Add(key)
				}
			},
			DeleteFunc: func(obj interface{}) {
				// IndexerInformer uses a delta queue, therefore for deletes we have to use this
				// key function.
				key, err := cache.DeletionHandlingMetaNamespaceKeyFunc(obj)
				if err == nil {
					queue.Add(key)
				}
			},
		},
		cache.Indexers{},
	)
	//Is default on docker-machine. Lets stick to defaults.
	ssh.SetDefaultClient(ssh.External)

	api := libmachine.New()

	controller := node.New(queue, indexer, informer, api)
	fmt.Printf("%v", controller)

	stop := make(chan struct{})
	defer close(stop)
	go controller.Run(1, stop)

	// Wait forever
	select {}
}
