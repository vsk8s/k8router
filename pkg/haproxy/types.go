package haproxy

import "net"

type TemplateInfo struct {
	// Name of wildcard cert, if available
	WildcardCertName string
	// Map of cert names to domains
	SniMap map[string][]string
	// Map from ingress domain to clusters with that ingress
	IngressInCluster map[string][]string
	// Map from cluster combinations to backend IPs
	BackendClusterCombinations map[string][]*net.IP
	// Map from certificate to certificate path
	Certs map[string]string
}
