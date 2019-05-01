package config

import (
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

type CertificateInternal struct {
	// How the certificate is named internally
	Name string `yaml:"name"`
	// Path to CertificateInternal bundle
	Cert string `yaml:"cert"`
	// Path to CertificateInternal key
	Key string `yaml:"key"`
	// Whether this is a wildcard CertificateInternal
	IsWildcard bool `yaml:"wildcard"`
	// List of domains this CertificateInternal is valid for
	Domains []string `yaml:"domains"`
}

// Describe all information we need to know about a ClusterInternal
type ClusterInternal struct {
	// Name of the ClusterInternal (used for logging)
	Name string `yaml:"name"`
	// Path to kubeconfig used to connect to the ClusterInternal
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
		return errors.New("ClusterInternal: kubeconfig missing")
	}
	if c.Name == "" {
		return errors.New("ClusterInternal: name missing")
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
		return errors.New("CertificateInternal: cert missing")
	}
	if c.Key == "" {
		return errors.New("CertificateInternal: cert key missing")
	}
	if len(c.Domains) == 0 && !c.IsWildcard {
		return errors.New("CertificateInternal: cert is not valid for any domain?")
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
		return nil, errors.New("CertificateInternal list missing")
	}
	if obj.Clusters == nil {
		return nil, errors.New("ClusterInternal list missing")
	}
	return &obj, nil
}
