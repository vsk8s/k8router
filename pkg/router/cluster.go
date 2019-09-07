package router

import (
	log "github.com/sirupsen/logrus"
	"github.com/vsk8s/k8router/pkg/config"
	"github.com/vsk8s/k8router/pkg/state"
	v1coreapi "k8s.io/api/core/v1"
	v1beta1extensionsapi "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/watch"
	v1core "k8s.io/client-go/kubernetes/typed/core/v1"
	v1beta1extension "k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"sync"
	"time"
)

// Cluster handles all single-cluster related tasks
type Cluster struct {
	// Config stanza this object takes care of
	config config.Cluster
	// K8S client (extensions)
	extensionClient v1beta1extension.ExtensionsV1beta1Interface
	// K8S client (core)
	coreClient v1core.CoreV1Interface
	// Channel for ingress change events
	ingressEvents chan state.IngressChange
	// Channel for backend change events
	backendEvents chan state.BackendChange
	// Channel used to stop our goroutines
	stopChannel chan bool
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
}

// ClusterFromConfig creates a new cluster handler for the provided config entry
func ClusterFromConfig(config config.Cluster, clusterStateChannel chan state.ClusterState) *Cluster {
	obj := Cluster{
		config:              config,
		ingressEvents:       make(chan state.IngressChange, 2),
		backendEvents:       make(chan state.BackendChange, 2),
		stopChannel:         make(chan bool, 2),
		clusterStateChannel: clusterStateChannel,
		readinessChannel:    make(chan bool, 2),
		stopFlag:            false,
		knownIngresses:      map[string]state.K8RouterIngress{},
		knownPods:           map[string]state.K8RouterBackend{},
		first:               true,
	}
	obj.clusterState.Name = config.Name
	return &obj
}

// Try to connect to the cluster
func (c *Cluster) connect() error {
	c.extensionClient = nil
	c.coreClient = nil
	kubeCfg, err := clientcmd.LoadFromFile(c.config.Kubeconfig)
	if err != nil {
		return err
	}
	clientCfg, err := clientcmd.NewDefaultClientConfig(*kubeCfg, &clientcmd.ConfigOverrides{}).ClientConfig()
	if err != nil {
		return err
	}
	c.extensionClient, err = v1beta1extension.NewForConfig(clientCfg)
	if err != nil {
		return err
	}
	c.coreClient, err = v1core.NewForConfig(clientCfg)
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
		case _ = <-c.stopChannel:
			return
		}
	}
}

// Take care of events from the pod watcher on ingress pods
func (c *Cluster) handlePodEvents(events <-chan watch.Event, wg *sync.WaitGroup) {
	for {
		event, ok := <-events
		if !ok {
			wg.Done()
			return
		}
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
			"obj":     event.Object,
		}).Debug("Pod event handler tick")
		if event.Type == watch.Error {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"obj":     event.Object,
			}).Warning("Got error in Pod event handler, aborting for reconnect...")
			wg.Done()
			return
		}
		eventObj, ok := event.Object.(*v1coreapi.Pod)
		if !ok {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Got event in pod handler which does not contain a pod?")
			continue
		}
		c.lastPodVersion = eventObj.ResourceVersion
		ip := net.ParseIP(eventObj.Status.PodIP)
		if ip == nil {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"pod":     eventObj.Name,
				"ip":      eventObj.Status.PodIP,
			}).Error("Couldn't parse pod ip")
			continue
		}
		obj := state.K8RouterBackend{
			IP:   &ip,
			Name: eventObj.Name,
		}
		myEvent := state.BackendChange{
			Backend: obj,
			Created: false,
		}
		switch event.Type {
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
}

// Take care of ingress events from the ingress watch
func (c *Cluster) handleIngressEvents(events <-chan watch.Event, wg *sync.WaitGroup) {
	for {
		event, ok := <-events
		if !ok {
			wg.Done()
			return
		}
		eventObj, ok := event.Object.(*v1beta1extensionsapi.Ingress)
		if !ok {
			if event.Type != watch.Error {
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
				}).Error("Got event in ingress handler which does not contain an ingress?")
			} else {
				log.WithFields(log.Fields{
					"cluster": c.config.Name,
				}).Error("Some other error")
			}
			continue
		}
		c.lastIngressVersion = eventObj.ResourceVersion
		switch event.Type {
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
			if event.Type == watch.Modified && !isEquivalent {
				c.ingressEvents <- myEvent
			}
			if !isEquivalent {
				myEvent.Created = true
				c.ingressEvents <- myEvent
			}
			c.knownIngresses[obj.Name] = obj
		case watch.Error:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"obj":     event.Object,
			}).Warning("Got error in Ingress event handler, aborting for reconnect...")
			wg.Done()
			return
		default:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Unknown event type in ingress handler!")
		}
	}
}

// Setup watchers and coordinate their goroutines
func (c *Cluster) watch() error {
	if c.extensionClient == nil {
		err := c.connect()
		if err != nil {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).WithError(err).Warn("Couldn't connect to cluster!")
			return err
		}
	}

	// We're connected -> setup watches
	wg := sync.WaitGroup{}
	var timeout int64 = 600 // 10 minutes
	wg.Add(2)
	watchOptions := metav1.ListOptions{
		Watch:          true,
		TimeoutSeconds: &timeout,
	}
	//if c.lastIngressVersion != "" {
	//	watchOptions.ResourceVersion = c.lastIngressVersion
	//}
	ingressWatcher, err := c.extensionClient.Ingresses("").Watch(watchOptions)
	if err != nil {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).WithError(err).Warn("Couldn't watch for ingresses, check RBAC!")
		return err
	}
	go c.handleIngressEvents(ingressWatcher.ResultChan(), &wg)

	labelMap := map[string]string{}
	labelMap["app.kubernetes.io/name"] = c.config.IngressAppName
	watchOptions = metav1.ListOptions{
		Watch:          true,
		TimeoutSeconds: &timeout,
		LabelSelector:  labels.SelectorFromSet(labelMap).String(),
	}
	//if c.lastPodVersion != "" {
	//	watchOptions.ResourceVersion = c.lastPodVersion
	//}
	podWatcher, err := c.coreClient.Pods(c.config.IngressNamespace).Watch(watchOptions)
	if err != nil {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).WithError(err).Warn("Couldn't watch for pods, check RBAC!")
		return err
	}
	go c.handlePodEvents(podWatcher.ResultChan(), &wg)

	go func() {
		_ = <-c.stopChannel
		podWatcher.Stop()
		ingressWatcher.Stop()
	}()

	go c.aggregator()
	if c.first {
		c.readinessChannel <- true
		c.first = false
	}
	wg.Wait()

	log.WithFields(log.Fields{
		"cluster": c.config.Name,
	}).Debug("Stopping event handlers")

	// Stop the goroutines
	c.stopChannel <- true
	c.stopChannel <- true
	return nil
}

// Main work loop responsible for reconnecting
func (c *Cluster) workLoop() {
	for {
		// TODO: Maybe do smart backoff instead of hardcoded 5-second intervals
		err := c.connect()
		if err != nil {
			log.WithField("cluster", c.config.Name).WithError(err).Info("Couldn't connect to cluster")
			time.Sleep(5 * time.Second)
			continue
		}
		// If this works, it'll block. If it doesn't, it will return an error
		err = c.watch()
		if err != nil {
			log.WithField("cluster", c.config.Name).WithError(err).Info("Couldn't watch cluster resources")
			time.Sleep(5 * time.Second)
			continue
		}
		// Since watch() didn't return an error, it's safe to assume that the client was shut down using an ordinary
		// exit-on-error -> restart the whole thing in the next loop iteration

		// ...except if we're to exit
		if c.stopFlag {
			break
		}
	}
	close(c.readinessChannel)
	close(c.stopChannel)
	close(c.ingressEvents)
	close(c.backendEvents)
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
	c.stopChannel <- true
	c.stopChannel <- true
}
