package router

import (
	log "github.com/sirupsen/logrus"
	"github.com/vsk8s/k8router/pkg/config"
	"github.com/vsk8s/k8router/pkg/state"
	v1coreapi "k8s.io/api/core/v1"
	v1beta1extensionsapi "k8s.io/api/extensions/v1beta1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/cache"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"time"
)

// Cluster handles all single-cluster related tasks
type Cluster struct {
	// Config stanza this object takes care of
	config config.Cluster
	// Channel for ingress change events
	ingressEvents chan state.IngressChange
	// Channel for backend change events
	backendEvents chan state.BackendChange
	// Channel used to indicate connection issues and clear all state
	clearChannel chan bool
	// Channel used to stop the aggregator logic
	aggregatorStopChannel chan bool
	// Current cluster state
	clusterState state.ClusterState
	// Whether we want to stop right now
	stopFlag bool
	// Channel used for cluster state updates, shared externally
	clusterStateChannel chan state.ClusterState
	// Channel used for readiness updates
	readinessChannel chan bool
	// Map of previously known ingresses (useful after reconnect)
	knownIngresses map[string]state.K8RouterIngress
	// Map of previously known pods (useful after reconnect)
	knownPods map[string]state.K8RouterBackend
	// Whether this is the first successful connection attempt
	first bool
	// Last version of an ingress received
	lastIngressVersion string
	// Last version of a pod received
	lastPodVersion string
	// Clientset used for the informer API
	client kubernetes.Interface
}

// ClusterFromConfig creates a new cluster handler for the provided config entry
func ClusterFromConfig(config config.Cluster, clusterStateChannel chan state.ClusterState) *Cluster {
	obj := Cluster{
		config:                config,
		ingressEvents:         make(chan state.IngressChange, 2),
		backendEvents:         make(chan state.BackendChange, 2),
		clusterStateChannel:   clusterStateChannel,
		readinessChannel:      make(chan bool, 2),
		clearChannel:          make(chan bool, 2),
		aggregatorStopChannel: make(chan bool, 2),
		stopFlag:              false,
		knownIngresses:        map[string]state.K8RouterIngress{},
		knownPods:             map[string]state.K8RouterBackend{},
		first:                 true,
	}
	obj.clusterState.Name = config.Name
	return &obj
}

// Try to connect to the cluster
func (c *Cluster) connect() error {
	kubeCfg, err := clientcmd.LoadFromFile(c.config.Kubeconfig)
	if err != nil {
		return err
	}
	clientCfg, err := clientcmd.NewDefaultClientConfig(*kubeCfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return err
	}
	c.client, err = kubernetes.NewForConfig(clientCfg)
	if err != nil {
		return err
	}
	return nil
}

// Aggregate all changes into a new cluster view
func (c *Cluster) aggregator() {
	for {
		select {
		// We have a new ingress or an ingress has been deleted
		case ingress := <-c.ingressEvents:
			if ingress.Created {
				// It's new, add it to the list
				c.clusterState.Ingresses = append(c.clusterState.Ingresses, ingress.Ingress)
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"ingress": ingress.Ingress.Name,
				}).Info("Detected new ingress.")
			} else {
				// Remove it
				for idx, elm := range c.clusterState.Ingresses {
					if elm.Name == ingress.Ingress.Name {
						c.clusterState.Ingresses[idx] = c.clusterState.Ingresses[len(c.clusterState.Ingresses)-1]
						c.clusterState.Ingresses = c.clusterState.Ingresses[:len(c.clusterState.Ingresses)-1]
						log.WithFields(log.Fields{
							"cluster": c.config.Name,
							"ingress": ingress.Ingress.Name,
						}).Info("Removed old ingress.")
						break
					}
				}
			}
			c.clusterStateChannel <- c.clusterState
		// Same as above, but for backends
		case backend := <-c.backendEvents:
			if backend.Created {

				c.clusterState.Backends = append(c.clusterState.Backends, backend.Backend)
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"backend": backend.Backend.Name,
					"ip":      backend.Backend.IP,
				}).Info("Detected new backend pod.")
			} else {
				// Remove it
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"backend": backend.Backend.Name,
					"ip":      backend.Backend.IP,
				}).Debug("Detected backend pod removal, searching...")
				for idx, elm := range c.clusterState.Backends {
					if elm.Name == backend.Backend.Name {
						c.clusterState.Backends[idx] = c.clusterState.Backends[len(c.clusterState.Backends)-1]
						c.clusterState.Backends = c.clusterState.Backends[:len(c.clusterState.Backends)-1]
						log.WithFields(log.Fields{
							"cluster": c.config.Name,
							"backend": backend.Backend.Name,
							"ip":      backend.Backend.IP,
						}).Info("Removed old backend pod.")
						break
					}
				}
			}
			c.clusterStateChannel <- c.clusterState
		case _ = <-c.aggregatorStopChannel:
			return
		case _ = <-c.clearChannel:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Debug("Clearing full cluster state...")
			c.clusterState.Backends = nil
			c.clusterState.Ingresses = nil
			c.clusterStateChannel <- c.clusterState
		}
	}
}

// Take care of events from the pod watcher on ingress pods
func (c *Cluster) handlePodEvents(event interface{}, action watch.EventType) {
	log.WithFields(log.Fields{
		"cluster": c.config.Name,
		"obj":     event,
	}).Debug("Pod event handler tick")
	eventObj, ok := event.(*v1coreapi.Pod)
	if eventObj.Namespace != c.config.IngressNamespace {
		return
	}
	if !ok {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).Error("Got event in pod handler which does not contain a pod?")
		return
	}
	c.lastPodVersion = eventObj.ResourceVersion
	ip := net.ParseIP(eventObj.Status.PodIP)
	if ip == nil {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
			"pod":     eventObj.Name,
			"ip":      eventObj.Status.PodIP,
		}).Error("Couldn't parse pod ip")
		return
	}
	obj := state.K8RouterBackend{
		IP:   &ip,
		Name: eventObj.Name,
	}
	myEvent := state.BackendChange{
		Backend: obj,
		Created: false,
	}
	switch action {
	case watch.Deleted:
		delete(c.knownPods, eventObj.Namespace+"-"+eventObj.Name)
		c.backendEvents <- myEvent
	case watch.Modified:
		c.backendEvents <- myEvent
		myEvent.Created = true
		c.backendEvents <- myEvent
		c.knownPods[eventObj.Namespace+"-"+eventObj.Name] = obj
	case watch.Added:
		val, ok := c.knownPods[eventObj.Namespace+"-"+eventObj.Name]
		if !ok || !state.IsBackendEquivalent(&obj, &val) {
			myEvent.Created = true
			c.backendEvents <- myEvent
		}
		c.knownPods[eventObj.Namespace+"-"+eventObj.Name] = obj
	default:
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).Error("Unknown event type in pod handler!")
	}
}

// Take care of ingress events from the ingress watch
func (c *Cluster) handleIngressEvent(event interface{}, action watch.EventType) {
	eventObj, ok := event.(*v1beta1extensionsapi.Ingress)
	if !ok {
		if action != watch.Error {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Got event in ingress handler which does not contain an ingress?")
		} else {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Some other error")
		}
		return
	}
	c.lastIngressVersion = eventObj.ResourceVersion
	switch action {
	case watch.Deleted:
		event := state.IngressChange{
			Ingress: state.K8RouterIngress{
				Name:  eventObj.Namespace + "-" + eventObj.Name,
				Hosts: []string{},
			},
			Created: false,
		}
		delete(c.knownIngresses, event.Ingress.Name)
		c.ingressEvents <- event
	case watch.Modified:
	case watch.Added:
		obj := state.K8RouterIngress{
			Name:  eventObj.Namespace + "-" + eventObj.Name,
			Hosts: []string{},
		}
		for _, rule := range eventObj.Spec.Rules {
			obj.Hosts = append(obj.Hosts, rule.Host)
		}
		myEvent := state.IngressChange{
			Ingress: obj,
			Created: false,
		}
		val, _ := c.knownIngresses[obj.Name]
		isEquivalent := ok && state.IsIngressEquivalent(&obj, &val)
		if action == watch.Modified && !isEquivalent {
			c.ingressEvents <- myEvent
		}
		if !isEquivalent {
			myEvent.Created = true
			c.ingressEvents <- myEvent
		}
		c.knownIngresses[obj.Name] = obj
	}
}

// Setup watchers and coordinate their goroutines
func (c *Cluster) watch() error {
	log.WithField("cluster", c.config.Name).Debug("Adding watches")

	factory := informers.NewSharedInformerFactory(c.client, 0)
	stopper := make(chan struct{})
	defer close(stopper)

	podInformer := factory.Core().V1().Pods().Informer()
	podInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.handlePodEvents(obj, watch.Added) },
		DeleteFunc: func(obj interface{}) { c.handlePodEvents(obj, watch.Deleted) },
		UpdateFunc: func(old interface{}, new interface{}) { c.handlePodEvents(new, watch.Modified) },
	})
	go podInformer.Run(stopper)

	ingressInformer := factory.Extensions().V1beta1().Ingresses().Informer()
	ingressInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.handleIngressEvent(obj, watch.Added) },
		DeleteFunc: func(obj interface{}) { c.handleIngressEvent(obj, watch.Deleted) },
		UpdateFunc: func(old interface{}, new interface{}) { c.handleIngressEvent(new, watch.Modified) },
	})
	go ingressInformer.Run(stopper)

	if c.first {
		c.readinessChannel <- true
		c.first = false
	}
	<-c.aggregatorStopChannel
	log.WithField("cluster", c.config.Name).Debug("Waiting for watches to exit...")

	log.WithFields(log.Fields{
		"cluster": c.config.Name,
	}).Debug("Stopping event handlers")

	log.WithField("cluster", c.config.Name).Debug("Event handlers stopped")
	return nil
}

// Main work loop responsible for reconnecting
func (c *Cluster) workLoop() {
	log.WithField("cluster", c.config.Name).Debug("Starting work loop")
	go c.aggregator()
	firstTry := true
	for {
		// TODO(uubk): Maybe do smart backoff instead of hardcoded intervals
		log.WithField("cluster", c.config.Name).Debug("About to connect")
		err := c.connect()
		if err != nil {
			if firstTry {
				c.clearChannel <- true
			}
			log.WithField("cluster", c.config.Name).WithError(err).Info("Couldn't connect to cluster")
			time.Sleep(60 * time.Second)
			firstTry = false
			continue
		}
		// If this works, it'll block. If it doesn't, it will return an error
		err = c.watch()
		if err != nil {
			if firstTry {
				c.clearChannel <- true
			}
			log.WithField("cluster", c.config.Name).WithError(err).Info("Couldn't watch cluster resources")
			time.Sleep(60 * time.Second)
			firstTry = false
			continue
		}

		time.Sleep(1 * time.Second)
		// Since watch() didn't return an error, it's safe to assume that the client was shut down using an ordinary
		// exit-on-error -> restart the whole thing in the next loop iteration

		// ...except if we're to exit
		if c.stopFlag {
			break
		}
	}
	c.aggregatorStopChannel <- true
	close(c.aggregatorStopChannel)
	close(c.readinessChannel)
	close(c.ingressEvents)
	close(c.backendEvents)
	log.WithField("cluster", c.config.Name).Debug("Work loop done")
}

// Start watching for cluster events
func (c *Cluster) Start() {
	c.stopFlag = false
	go c.workLoop()
}

// Wait until this handler is ready
func (c *Cluster) Wait() {
	_ = <-c.readinessChannel
}

// Stop watching for cluster events
func (c *Cluster) Stop() {
	// TODO: Fix this and use it
	c.stopFlag = true
	c.aggregatorStopChannel <- true
	c.aggregatorStopChannel <- true
}
