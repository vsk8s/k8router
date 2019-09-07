package state

import "net"

// K8RouterIngress contains all ingress-related information
type K8RouterIngress struct {
	Name  string
	Hosts []string
}

// K8RouterBackend contains all backend-related information
type K8RouterBackend struct {
	Name string
	IP   *net.IP
}

// ClusterState contains the full state of a given ClusterInternal. This should be enough to build the haproxy config
type ClusterState struct {
	Name      string
	Ingresses []K8RouterIngress
	Backends  []K8RouterBackend
}

// IngressChange represents an ingress change event
type IngressChange struct {
	Ingress K8RouterIngress
	Created bool
}

// BackendChange contains a backend change event
type BackendChange struct {
	Backend K8RouterBackend
	Created bool
}
