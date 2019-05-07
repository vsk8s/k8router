package state

// Check whether two backends are equivalent in the context of update coalescing
func IsBackendEquivalent(backendA *K8RouterBackend, backendB *K8RouterBackend) bool {
	if backendA == nil || backendB == nil {
		return false
	}
	if backendA.Name != backendB.Name {
		return false
	}
	return backendA.IP.Equal(*backendB.IP)
}

// Check whether two Ingresses are equivalent in the context of update coalescing
func IsIngressEquivalent(ingressA *K8RouterIngress, ingressB *K8RouterIngress) bool {
	if ingressA == nil || ingressB == nil {
		return false
	}
	if ingressA.Name != ingressB.Name {
		return false
	}
	if len(ingressA.Hosts) != len(ingressB.Hosts) {
		return false
	}
	for index, value := range ingressA.Hosts {
		if ingressB.Hosts[index] != value {
			return false
		}
	}
	return true
}

// Check whether two whole cluster state objects are equivalent in the context of update coalescing
func IsClusterStateEquivalent(clusterA *ClusterState, clusterB *ClusterState) bool {
	if clusterA == nil || clusterB == nil {
		return false
	}
	if clusterA.Name != clusterB.Name {
		return false
	}
	if len(clusterA.Backends) != len(clusterB.Backends) {
		return false
	}
	for index, value := range clusterA.Backends {
		if IsBackendEquivalent(&clusterB.Backends[index], &value) {
			return false
		}
	}
	if len(clusterA.Ingresses) != len(clusterB.Ingresses) {
		return false
	}
	for index, value := range clusterA.Ingresses {
		if IsIngressEquivalent(&clusterB.Ingresses[index], &value) {
			return false
		}
	}
	return true
}
