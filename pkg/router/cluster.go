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
	config config.Cluster

	ingressEvents chan state.IngressChange

	backendEvents chan state.BackendChange

	// Channel used to indicate connection issues and clear all state
	clearChannel chan bool

	// Channel used to stop the aggregator logic
	aggregatorStopChannel chan bool

	currentClusterState state.ClusterState

	// Whether we want to stop right now
	shallExit bool

	// Channel used for cluster state updates, shared externally
	clusterStateChannel chan state.ClusterState

	loadBalancerChannel chan state.LoadBalancerChange

	readinessChannel chan bool

	knownIngresses map[string]state.K8RouterIngress

	knownPods map[string]state.K8RouterBackend

	isFirstConnectionAttempt bool

	latestIngressVersion string

	latestPodVersion string

	// Clientset used for the informer API
	client kubernetes.Interface
}

// Initialize a new cluster
func Initialize(config config.Cluster, clusterStateChannel chan state.ClusterState, loadBalancerChannel chan state.LoadBalancerChange) *Cluster {
	obj := Cluster{
		config:                   config,
		ingressEvents:            make(chan state.IngressChange, 2),
		backendEvents:            make(chan state.BackendChange, 2),
		clusterStateChannel:      clusterStateChannel,
		loadBalancerChannel:      loadBalancerChannel,
		readinessChannel:         make(chan bool, 2),
		clearChannel:             make(chan bool, 2),
		aggregatorStopChannel:    make(chan bool, 2),
		shallExit:                false,
		knownIngresses:           map[string]state.K8RouterIngress{},
		knownPods:                map[string]state.K8RouterBackend{},
		isFirstConnectionAttempt: true,
	}
	obj.currentClusterState.Name = config.Name
	return &obj
}

// Start the cluster
func (c *Cluster) Start() {
	c.shallExit = false
	go c.eventLoop()
}

// Wait for the cluster
func (c *Cluster) Wait() {
	_ = <-c.readinessChannel
}

// Stop the cluster
func (c *Cluster) Stop() {
	// TODO: Fix this and use it
	c.shallExit = true
	c.aggregatorStopChannel <- true
	c.aggregatorStopChannel <- true
}

func (c *Cluster) eventLoop() {
	log.WithField("cluster", c.config.Name).Debug("Starting work loop")
	go c.aggregateClusterView()
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

		if c.shallExit {
			log.WithField("cluster", c.config.Name).Info("cluster watcher shutting down")
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

// Aggregate all changes into a new cluster view
func (c *Cluster) aggregateClusterView() {
	for {
		select {
		case event := <-c.ingressEvents:
			if event.Created {
				c.currentClusterState.Ingresses = append(c.currentClusterState.Ingresses, event.Ingress)
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"ingress": event.Ingress.Name,
				}).Info("Detected new ingress.")
			} else {
				for i, ingress := range c.currentClusterState.Ingresses {
					if ingress.Name == event.Ingress.Name {
						c.currentClusterState.Ingresses[i] = c.currentClusterState.Ingresses[len(c.currentClusterState.Ingresses)-1]
						c.currentClusterState.Ingresses = c.currentClusterState.Ingresses[:len(c.currentClusterState.Ingresses)-1]
						log.WithFields(log.Fields{
							"cluster": c.config.Name,
							"ingress": event.Ingress.Name,
						}).Info("Removed old ingress.")
						break
					}
				}
			}
			c.clusterStateChannel <- c.currentClusterState
		case event := <-c.backendEvents:
			if event.Created {
				c.currentClusterState.Backends = append(c.currentClusterState.Backends, event.Backend)
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"backend": event.Backend.Name,
					"ip":      event.Backend.IP,
				}).Info("Detected new backend pod.")
			} else {
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
					"backend": event.Backend.Name,
					"ip":      event.Backend.IP,
				}).Debug("Detected backend pod removal, searching...")
				for i, backend := range c.currentClusterState.Backends {
					if backend.Name == event.Backend.Name {
						c.currentClusterState.Backends[i] = c.currentClusterState.Backends[len(c.currentClusterState.Backends)-1]
						c.currentClusterState.Backends = c.currentClusterState.Backends[:len(c.currentClusterState.Backends)-1]
						log.WithFields(log.Fields{
							"cluster": c.config.Name,
							"backend": event.Backend.Name,
							"ip":      event.Backend.IP,
						}).Info("Removed old backend pod.")
						break
					}
				}
			}
			c.clusterStateChannel <- c.currentClusterState
		case _ = <-c.aggregatorStopChannel:
			return
		case _ = <-c.clearChannel:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Debug("Clearing full cluster state...")
			c.currentClusterState.Backends = nil
			c.currentClusterState.Ingresses = nil
			c.clusterStateChannel <- c.currentClusterState
		}
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

	LoadBalancerInformer := factory.Core().V1().Services().Informer()
	LoadBalancerInformer.AddEventHandler(cache.ResourceEventHandlerFuncs{
		AddFunc:    func(obj interface{}) { c.handleLoadBalancerEvent(obj, watch.Added) },
		DeleteFunc: func(obj interface{}) { c.handleLoadBalancerEvent(obj, watch.Deleted) },
		UpdateFunc: func(old interface{}, new interface{}) { c.handleLoadBalancerEvent(new, watch.Modified) },
	})
	go LoadBalancerInformer.Run(stopper)

	if c.isFirstConnectionAttempt {
		c.readinessChannel <- true
		c.isFirstConnectionAttempt = false
	}
	<-c.aggregatorStopChannel
	log.WithField("cluster", c.config.Name).Debug("Waiting for watches to exit...")

	log.WithFields(log.Fields{
		"cluster": c.config.Name,
	}).Debug("Stopping event handlers")

	log.WithField("cluster", c.config.Name).Debug("Event handlers stopped")
	return nil
}

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
	c.latestPodVersion = eventObj.ResourceVersion
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
			}).Error("Got event in ingress handler which contains no ingress")
		} else {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"event":   event,
			}).Error("Some other error")
		}
		return
	}
	c.latestIngressVersion = eventObj.ResourceVersion
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

func (c *Cluster) handleLoadBalancerEvent(event interface{}, action watch.EventType) {
	eventObj, ok := event.(*v1coreapi.Service)
	if !ok {
		if action != watch.Error {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Got event in service handler which contains no service")
		} else {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"event":   event,
			}).Error("Some other error")
		}
		return
	}
	if eventObj.Spec.Type != "LoadBalancer" {
		return
	}

	for _, port := range eventObj.Spec.Ports {
		ip := net.ParseIP(eventObj.Spec.ClusterIP)
		if ip == nil {
			log.WithField("service", eventObj.Name).WithField("IP", eventObj.Spec.ClusterIP).Warn("Could not parse IP")
			continue
		}
		message := state.LoadBalancer{
			Name:     eventObj.Name,
			IP:       &ip,
			Port:     port.Port,
			Protocol: port.Protocol,
		}

		switch action {
		case watch.Added:
			c.loadBalancerChannel <- state.LoadBalancerChange{
				Service: message,
				Created: true,
			}
		case watch.Modified:
			c.loadBalancerChannel <- state.LoadBalancerChange{
				Service: message,
				Created: false,
			}
			c.loadBalancerChannel <- state.LoadBalancerChange{
				Service: message,
				Created: true,
			}
		case watch.Deleted:
			c.loadBalancerChannel <- state.LoadBalancerChange{
				Service: message,
				Created: false,
			}
		default:
			log.WithField("err", event).Error("Got error event")
		}
	}
}

func (c *Cluster) connect() error {
	kubeConfig, err := clientcmd.LoadFromFile(c.config.Kubeconfig)
	if err != nil {
		return err
	}
	clientConfig, err := clientcmd.NewDefaultClientConfig(*kubeConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return err
	}
	c.client, err = kubernetes.NewForConfig(clientConfig)
	if err != nil {
		return err
	}
	return nil
}
