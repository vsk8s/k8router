package router

import (
	"github.com/pkg/errors"
	"gopkg.in/yaml.v2"
	"io/ioutil"
)

type certificate struct {
	// Path to certificate bundle
	Cert string `yaml:"cert"`
	// Path to certificate key
	Key string `yaml:"key"`
	// Whether this is a wildcard certificate
	IsWildcard bool `yaml:"wildcard"`
	// List of domains this certificate is valid for
	Domains []string `yaml:"domains"`
}

// Describe all information we need to know about a cluster
type cluster struct {
	// Name of the cluster (used for logging)
	Name string `yaml:"name"`
	// Path to kubeconfig used to connect to the cluster
	Kubeconfig string `yaml:"kubeconfig"`
	// Namespace where the Ingress is located
	IngressNamespace string `yaml:"ingressNamespace"`
	// Name of the ingress deployment
	IngressDeamonSetName string `yaml:"ingressDeamonSetName"`
	// Port the ingress pods use
	IngressPort int `yaml:"ingressPort"`
}

// This struct only exists for parser trickery
type dummyCluster struct {
	*cluster
}

// This struct only exists for parser trickery
type dummyCertificate struct {
	*certificate
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
	Clusters []dummyCluster `yaml:"clusters"`
	// List of TLS certificates to use
	Certificates []dummyCertificate `yaml:"certificates"`
}

// Custom deserializer for 'dummyCluster' in order to transparently provide default values where applicable
func (c *dummyCluster) UnmarshalYAML(unmarshal func(interface{}) error) error {
	obj := cluster{}
	err := unmarshal(&obj)

	if err != nil {
		return err
	}
	c.cluster = &obj

	if c.IngressDeamonSetName == "" {
		c.IngressDeamonSetName = "ingress-nginx"
	}
	if c.IngressNamespace == "" {
		c.IngressNamespace = "ingress-nginx"
	}
	if c.Kubeconfig == "" {
		return errors.New("cluster: kubeconfig missing")
	}
	if c.Name == "" {
		return errors.New("cluster: name missing")
	}
	if c.IngressPort == 0 {
		c.IngressPort = 80
	}

	return nil
}

// Custom deserializer for 'dummyCertificate' in order to transparently provide default values where applicable
func (c *dummyCertificate) UnmarshalYAML(unmarshal func(interface{}) error) error {
	obj := certificate{}
	err := unmarshal(&obj)

	if err != nil {
		return err
	}

	c.certificate = &obj

	if c.Cert == "" {
		return errors.New("certificate: cert missing")
	}
	if c.Key == "" {
		return errors.New("certificate: cert key missing")
	}
	if len(c.Domains) == 0 && ! c.IsWildcard {
		return errors.New("certificate: cert is not valid for any domain?")
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
		return nil, errors.New("certificate list missing")
	}
	if obj.Clusters == nil {
		return nil, errors.New("cluster list missing")
	}
	return &obj, nil
}