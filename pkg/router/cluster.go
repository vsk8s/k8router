package router

import (
	log "github.com/sirupsen/logrus"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
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

// Handle all single-cluster related tasks
type Cluster struct {
	config              config.Cluster
	extensionClient     v1beta1extension.ExtensionsV1beta1Interface
	coreClient          v1core.CoreV1Interface
	ingressEvents       chan state.IngressChange
	backendEvents       chan state.BackendChange
	stopChannel         chan bool
	clusterState        state.ClusterState
	stopFlag            bool
	clusterStateChannel chan state.ClusterState
	readinessChannel    chan bool
}

// Create a new cluster handler for the provided config entry
func ClusterFromConfig(config config.Cluster, clusterStateChannel chan state.ClusterState) *Cluster {
	obj := Cluster{
		config:              config,
		ingressEvents:       make(chan state.IngressChange, 2),
		backendEvents:       make(chan state.BackendChange, 2),
		stopChannel:         make(chan bool, 2),
		clusterStateChannel: clusterStateChannel,
		readinessChannel:    make(chan bool, 2),
		stopFlag:            false,
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
func (c *Cluster) handlePodEvents(events <-chan watch.Event, wg sync.WaitGroup) {
	for event := range events {
		if event.Type == watch.Error {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"obj":     event.Object,
			}).Warning("Got error in event handler, aborting for reconnect...")
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
			c.backendEvents <- myEvent
		case watch.Modified:
			c.backendEvents <- myEvent
			myEvent.Created = true
			c.backendEvents <- myEvent
		case watch.Added:
			myEvent.Created = true
			c.backendEvents <- myEvent
		default:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Unknown event type in pod handler!")
		}
	}
}

// Take care of ingress events from the ingress watch
func (c *Cluster) handleIngressEvents(events <-chan watch.Event, wg sync.WaitGroup) {
	for event := range events {
		eventObj, ok := event.Object.(*v1beta1extensionsapi.Ingress)
		if !ok && event.Type != watch.Error {
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
			}).Error("Got event in ingress handler which does not contain an ingress?")
			continue
		}
		switch event.Type {
		case watch.Deleted:
			event := state.IngressChange{
				Ingress: state.K8RouterIngress{
					Name:  eventObj.Namespace + "-" + eventObj.Name,
					Hosts: []string{},
				},
				Created: false,
			}
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
			if event.Type == watch.Modified {
				c.ingressEvents <- myEvent
			}
			myEvent.Created = true
			c.ingressEvents <- myEvent
		case watch.Error:
			log.WithFields(log.Fields{
				"cluster": c.config.Name,
				"obj":     event.Object,
			}).Warning("Got error in event handler, aborting for reconnect...")
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
	wg.Add(2)
	ingressWatcher, err := c.extensionClient.Ingresses("").Watch(metav1.ListOptions{})
	if err != nil {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).WithError(err).Warn("Couldn't watch for ingresses, check RBAC!")
		return err
	}
	go c.handleIngressEvents(ingressWatcher.ResultChan(), wg)

	labelMap := map[string]string{}
	labelMap["app.kubernetes.io/name"] = c.config.IngressAppName
	podWatcher, err := c.coreClient.Pods(c.config.IngressNamespace).Watch(metav1.ListOptions{
		LabelSelector: labels.SelectorFromSet(labelMap).String(),
	})
	if err != nil {
		log.WithFields(log.Fields{
			"cluster": c.config.Name,
		}).WithError(err).Warn("Couldn't watch for pods, check RBAC!")
		return err
	}
	go c.handlePodEvents(podWatcher.ResultChan(), wg)

	go func() {
		_ = <-c.stopChannel
		podWatcher.Stop()
		ingressWatcher.Stop()
	}()

	go c.aggregator()
	c.readinessChannel <- true
	wg.Wait()

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
	c.stopFlag = true
	c.stopChannel <- true
	c.stopChannel <- true
}
