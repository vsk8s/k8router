package config

import (
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
	"net"
)

// Everything you ever wanted to know about a certificate
type CertificateInternal struct {
	// How the certificate is named internally
	Name string `yaml:"name"`
	// Path to certificate directory
	Cert string `yaml:"cert"`
	// List of domains this certificate is valid for
	Domains []string `yaml:"domains"`
}

// Describe all information we need to know about a cluster
type ClusterInternal struct {
	// Name of the cluster (used for logging)
	Name string `yaml:"name"`
	// Path to kubeconfig used to connect to the cluster
	Kubeconfig string `yaml:"kubeconfig"`
	// Namespace where the Ingress is located
	IngressNamespace string `yaml:"ingressNamespace"`
	// Name of the ingress deployment (the pod label "app.kubernetes.io/name" will be checked)
	IngressAppName string `yaml:"ingressDeamonSetName"`
	// Port the ingress pods use
	IngressPort int `yaml:"ingressPort"`
}

// This struct only exists for parser trickery
type Cluster struct {
	*ClusterInternal
}

// This struct only exists for parser trickery
type Certificate struct {
	*CertificateInternal
}

// The main k8router config. This is deserialized from YAML using the annotations
type Config struct {
	// Path to the config template to use for HAProxy
	HAProxyTemplatePath string `yaml:"haproxyTemplatePath"`
	// Path to HAProxy config dropin to create for this service
	HAProxyDropinPath string `yaml:"haproxyDropinPath"`
	// Mode to use in case the config file is created
	HAProxyDropinMode string `yaml:"haproxyDropinMode"`
	// List of clusters to route to
	Clusters []Cluster `yaml:"clusters"`
	// List of TLS certificates to use
	Certificates []Certificate `yaml:"certificates"`
	// List of IPs to listen on
	IPs []*net.IP `yaml:"ips"`
}

// Custom deserializer for 'Cluster' in order to transparently provide default values where applicable
func (c *Cluster) UnmarshalYAML(unmarshal func(interface{}) error) error {
	obj := ClusterInternal{}
	err := unmarshal(&obj)

	if err != nil {
		return err
	}
	c.ClusterInternal = &obj

	if c.IngressAppName == "" {
		c.IngressAppName = "ingress-nginx"
	}
	if c.IngressNamespace == "" {
		c.IngressNamespace = "ingress-nginx"
	}
	if c.Kubeconfig == "" {
		return errors.New("Cluster: kubeconfig missing")
	}
	if c.Name == "" {
		return errors.New("Cluster: name missing")
	}
	if c.IngressPort == 0 {
		c.IngressPort = 80
	}

	return nil
}

// Custom deserializer for 'Certificate' in order to transparently provide default values where applicable
func (c *Certificate) UnmarshalYAML(unmarshal func(interface{}) error) error {
	obj := CertificateInternal{}
	err := unmarshal(&obj)

	if err != nil {
		return err
	}

	c.CertificateInternal = &obj

	if c.Cert == "" {
		return errors.New("Certificate: cert missing")
	}
	if c.Name == "" {
		return errors.New("Certificate: name missing")
	}
	if len(c.Domains) == 0 {
		return errors.New("Certificate: cert is not valid for any domain?")
	}

	return nil
}

// Create a config object by parsing it from file
func FromFile(path string) (*Config, error) {
	obj := Config{}
	data, err := ioutil.ReadFile(path)
	if err != nil {
		err = errors.Wrap(err, "file read failed")
		return nil, err
	}
	err = yaml.UnmarshalStrict(data, &obj)
	if err != nil {
		return nil, err
	}
	if obj.Certificates == nil {
		return nil, errors.New("Certificate list missing")
	}
	if obj.Clusters == nil {
		return nil, errors.New("Cluster list missing")
	}
	if len(obj.IPs) == 0 {
		return nil, errors.New("IP list missing")
	}
	return &obj, nil
}
