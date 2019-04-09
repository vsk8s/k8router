package state

import "net"

// All ingress-related information
type K8RouterIngress struct {
	Name  string
	Hosts []string
}

// All backend-related information
type K8RouterBackend struct {
	Name string
	IP   *net.IP
}

// The full state of a given Cluster. This should be enough to build the haproxy config
type ClusterState struct {
	Name string
	Ingresses []K8RouterIngress
	Backends  []K8RouterBackend
}

// An ingress change event
type IngressChange struct {
	Ingress K8RouterIngress
	Created bool
}

// A backend change event
type BackendChange struct {
	Backend K8RouterBackend
	Created bool
}
