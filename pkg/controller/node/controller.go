package node

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/docker/machine/libmachine/drivers"
	"github.com/docker/machine/libmachine/host"
	"github.com/golang/glog"
	"github.com/kube-node/kube-machine/pkg/controller"
	"github.com/kube-node/kube-machine/pkg/libmachine"
	"github.com/kube-node/kube-machine/pkg/nodeclass"
	"github.com/kube-node/kube-machine/pkg/options"
	"github.com/kube-node/nodeset/pkg/nodeset/v1alpha1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/runtime"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/pkg/api/v1"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/util/workqueue"
)

// Mostly from https://github.com/rmohr/kubernetes/blob/b39b3ba01675828c13bc0dea67d5114b4c225644/staging/src/k8s.io/client-go/examples/workqueue/main.go

type Controller struct {
	nodeInformer      cache.Controller
	nodeIndexer       cache.Indexer
	nodeQueue         workqueue.RateLimitingInterface
	nodeClassStore    cache.Store
	nodeClassInformer cache.Controller
	mapi              *libmachine.Client
	client            *kubernetes.Clientset
}

const (
	nodeClassAnnotationKey  = "node.k8s.io/node-class"
	driverDataAnnotationKey = "node.k8s.io/driver-data"
	noExecuteTaintKey       = "node.k8s.io/not-up"
	deleteFinalizerName     = "node.k8s.io/delete"
)

func New(
	client *kubernetes.Clientset,
	queue workqueue.RateLimitingInterface,
	nodeIndexer cache.Indexer,
	nodeInformer cache.Controller,
	nodeClassStore cache.Store,
	nodeClassController cache.Controller,
	mapi *libmachine.Client,
) controller.Interface {
	return &Controller{
		nodeInformer:      nodeInformer,
		nodeIndexer:       nodeIndexer,
		nodeQueue:         queue,
		nodeClassInformer: nodeClassController,
		nodeClassStore:    nodeClassStore,
		mapi:              mapi,
		client:            client,
	}
}

func (c *Controller) processNextItem() bool {
	// Wait until there is a new item in the working nodeQueue
	key, quit := c.nodeQueue.Get()
	if quit {
		return false
	}

	defer c.nodeQueue.Done(key)

	// Invoke the method containing the business logic
	err := c.syncNode(key.(string))
	// Handle the error if something went wrong during the execution of the business logic
	c.handleErr(err, key)
	return true
}

func (c *Controller) deleteNode(node *v1.Node) error {
	h, err := c.mapi.Load(node)
	if err != nil {
		return err
	}

	return h.Driver.Remove()
}

func (c *Controller) createNode(node *v1.Node) (*host.Host, error) {
	nodeClass := node.Annotations[nodeClassAnnotationKey]
	ncobj, exists, err := c.nodeClassStore.GetByKey("default/" + nodeClass)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch nodeclass from store: %v", err)
	}
	if !exists {
		return nil, fmt.Errorf("nodeclass %q for node %q not found", nodeClass, node.GetName())
	}

	class := ncobj.(*v1alpha1.NodeClass)
	var config nodeclass.NodeClassConfig
	err = json.Unmarshal(class.Config.Raw, &config)
	if err != nil {
		return nil, fmt.Errorf("failed parsing config from nodeclass %q: %v", nodeClass, err)
	}

	rawDriver, err := json.Marshal(&drivers.BaseDriver{MachineName: node.Name})
	if err != nil {
		return nil, fmt.Errorf("error attempting to marshal bare driver data: %s", err)
	}

	mhost, err := c.mapi.NewHost(config.Provider, rawDriver)
	if err != nil {
		return nil, fmt.Errorf("failed to create docker machine host for node %q: %v", node.Name, err)
	}

	opts := options.New(config.DockerMachineFlags)
	mcnFlags := mhost.Driver.GetCreateFlags()
	driverOpts := options.GetDriverOpts(opts, mcnFlags, class.Resources)

	mhost.Driver.SetConfigFromFlags(driverOpts)
	err = c.mapi.Create(mhost, &config)
	if err != nil {
		mhost.Driver.Remove()
		return nil, fmt.Errorf("failed to create node %q on cloud provider: %v", node.Name, err)
	}

	return mhost, nil
}

func nodeHasNoExecuteTaint(n *v1.Node) bool {
	for _, t := range n.Spec.Taints {
		if t.Key == noExecuteTaintKey {
			return true
		}
	}
	return false
}

func nodeHasJoined(n *v1.Node) bool {
	for _, c := range n.Status.Conditions {
		if c.Reason == "NodeStatusNeverUpdated" {
			return false
		}
	}
	return true
}

func nodeHasFinalizer(n *v1.Node) bool {
	for _, f := range n.Finalizers {
		if f == deleteFinalizerName {
			return true
		}
	}
	return false
}

func nodeWasDeleted(n *v1.Node) bool {
	if n.DeletionTimestamp != nil {
		return true
	}
	return false
}

func (c *Controller) syncNode(key string) error {
	nobj, exists, err := c.nodeIndexer.GetByKey(key)
	if err != nil {
		glog.Errorf("Fetching object with key %s from store failed with %v", key, err)
		return err
	}

	if !exists {
		glog.Infof("Node %s does not exist anymore\n", key)
		return nil
	}

	node := nobj.(*v1.Node)
	glog.V(4).Infof("Processing Node %s\n", node.GetName())

	if !nodeHasFinalizer(node) {
		node.Finalizers = append(node.Finalizers, deleteFinalizerName)
		node, err = c.client.Nodes().Update(node)
		if err != nil {
			return err
		}
		err = c.nodeIndexer.Update(node)
		if err != nil {
			return err
		}
	} else if !machinesIsCreated(node) && !nodeHasNoExecuteTaint(node) {
		//Add a noSchedule taint so the node does not get evicted before joining
		//Otherwise Daemonsets would not get deployed
		node.Spec.Taints = append(node.Spec.Taints, v1.Taint{
			Key:    noExecuteTaintKey,
			Effect: v1.TaintEffectNoExecute,
			Value:  "kube-machine",
		})
		node, err = c.client.Nodes().Update(node)
		if err != nil {
			return err
		}
		err = c.nodeIndexer.Update(node)
		if err != nil {
			return err
		}
	} else if !machinesIsCreated(node) {
		//Create machine
		mhost, err := c.createNode(node)
		if err != nil {
			return fmt.Errorf("failed creating node %q: %v", node.Name, err)
		}
		data, err := json.Marshal(mhost)
		if err != nil {
			return err
		}
		patchData := []byte(fmt.Sprintf(`
	{"metadata": {"annotations": {"%s": %s}}}"
		`, driverDataAnnotationKey, strconv.Quote(string(data))))
		node, err = c.client.Nodes().Patch(node.Name, types.StrategicMergePatchType, patchData)
		if err != nil {
			return err
		}
		err = c.nodeIndexer.Update(node)
		if err != nil {
			return err
		}
	} else if machinesIsCreated(node) && nodeHasJoined(node) && nodeHasNoExecuteTaint(node) {
		//Remove noSchedule taint
		//TODO: only remove the one taint we created
		node.Spec.Taints = []v1.Taint{}
		node, err = c.client.Nodes().Update(node)
		if err != nil {
			return err
		}
		err = c.nodeIndexer.Update(node)
		if err != nil {
			return err
		}
	} else if nodeWasDeleted(node) && nodeHasFinalizer(node) {
		err := c.deleteNode(node)
		if err != nil {
			return err
		}

		//TODO: Only remove our finalizer, not all...
		node.Finalizers = []string{}
		node, err = c.client.Nodes().Update(node)
		if err != nil {
			return err
		}
		err = c.nodeIndexer.Update(node)
		if err != nil {
			return err
		}
	}

	return nil
}
func machinesIsCreated(node *v1.Node) bool {
	return node.Annotations[driverDataAnnotationKey] != ""
}

// handleErr checks if an error happened and makes sure we will retry later.
func (c *Controller) handleErr(err error, key interface{}) {
	if err == nil {
		// Forget about the #AddRateLimited history of the key on every successful synchronization.
		// This ensures that future processing of updates for this key is not delayed because of
		// an outdated error history.
		c.nodeQueue.Forget(key)
		return
	}

	// This controller retries 5 times if something goes wrong. After that, it stops trying.
	if c.nodeQueue.NumRequeues(key) < 5 {
		glog.Infof("Error syncing node %v: %v", key, err)

		// Re-enqueue the key rate limited. Based on the rate limiter on the
		// nodeQueue and the re-enqueue history, the key will be processed later again.
		c.nodeQueue.AddRateLimited(key)
		return
	}

	c.nodeQueue.Forget(key)
	// Report to an external entity that, even after several retries, we could not successfully process this key
	runtime.HandleError(err)
	glog.Infof("Dropping node %q out of the queue: %v", key, err)
}

func (c *Controller) Run(workerCount int, stopCh chan struct{}) {
	defer runtime.HandleCrash()

	// Let the workers stop when we are done
	defer c.nodeQueue.ShutDown()
	glog.Info("Starting Node controller")

	go c.nodeInformer.Run(stopCh)
	go c.nodeClassInformer.Run(stopCh)

	// Wait for all involved caches to be synced, before processing items from the nodeQueue is started
	if !cache.WaitForCacheSync(stopCh, c.nodeInformer.HasSynced, c.nodeClassInformer.HasSynced) {
		runtime.HandleError(errors.New("timed out waiting for caches to sync"))
		return
	}

	for i := 0; i < workerCount; i++ {
		go wait.Until(c.runWorker, time.Second, stopCh)
	}

	<-stopCh
	glog.Info("Stopping Node controller")
}

func (c *Controller) runWorker() {
	for c.processNextItem() {
	}
}
