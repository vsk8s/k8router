package state

import "net"

// All ingress-related information
type Ingress struct {
	Name string
	Hosts []string
}

// All backend-related information
type Backend struct {
	Name string
	IP *net.IP
}

// The full state of a given Cluster. This should be enough to build the haproxy config
type ClusterState struct {
	Ingresses []Ingress
	Backends []Backend
}

// An ingress change event
type IngressChange struct {
	Ingress Ingress
	Created bool
}

// A backend change event
type BackendChange struct {
	Backend Backend
	Created bool
}