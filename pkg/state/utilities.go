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
