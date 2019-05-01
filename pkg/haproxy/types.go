package haproxy

import "net"

type SniDetail struct {
	// List of domains this certificate is valid for. Filtered to domains actually required
	Domains []string
	// Whether this is a wildcard certificate
	IsWildcard bool
	// Which port to use for the dummy forward (see docs)
	LocalForwardPort int
	// Path to concatenated x509 chain and key in PEM format
	Path string
}

type Backend struct {
	IP   *net.IP
	Name string
}

type TemplateInfo struct {
	// Map of certificate names to their details as required for the different config sections
	SniList map[string]SniDetail
	// Map of backend name to actual backend hosts
	BackendCombinationList map[string][]Backend
	// Map of host name to backend name
	HostToBackend map[string]string
	// Default certificate to use
	DefaultWildcardCert string
	// List of IPs to listen on
	IPs []*net.IP
}
