package config

import (
	"github.com/onsi/gomega"
	"io/ioutil"
	"net"
	"os"
	"path"
	"testing"
)

// Helper function to write a config string to file and load it
func writeAndLoadConfig(config string, t *testing.T) (*Config, error) {
	dir := os.TempDir()
	file := path.Join(dir, "testfile")
	err := ioutil.WriteFile(file, []byte(config), 0644)
	if err != nil {
		t.Error(err)
		return nil, err
	}

	return FromFile(file)
}

// Helper function to check whether loading the specified config produces the specified error
func testError(config string, error string, t *testing.T, g *gomega.WithT) {
	_, err := writeAndLoadConfig(config, t)
	g.Expect(err).NotTo(gomega.BeNil(), "This should have resulted in an error")
	g.Expect(err.Error()).To(gomega.BeIdenticalTo(error), "This should have matched the error description")
}

// Check a basic invalid config parse
func TestInvalidConfigParse(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	configStr := `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - name: testcluster
`
	testError(configStr, "Cluster: kubeconfig missing", t, g)
}

func TestDefaultConfigParse(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	configStr := `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - name: testcluster
    kubeconfig: /etc/kubernetes/kubeconfig.yml
certificates:
  - cert: /foo
    name: foo
    domains:
      - example.org
ips:
  - 127.0.0.1
`
	uut, err := writeAndLoadConfig(configStr, t)
	if err != nil {
		t.Error(err)
		return
	}

	g.Expect(len(uut.Clusters)).To(gomega.BeIdenticalTo(1), "There should be 1 cluster.")
	g.Expect(len(uut.Certificates)).To(gomega.BeIdenticalTo(1), "There should be 1 certificate.")
	g.Expect(uut.Clusters[0].IngressNamespace).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressAppName).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressPort).To(gomega.BeIdenticalTo(80))
	g.Expect(len(uut.IPs)).To(gomega.BeIdenticalTo(1))
	g.Expect(*uut.IPs[0]).To(gomega.BeEquivalentTo(net.ParseIP("127.0.0.1")))
}

func TestErrorConditions(t *testing.T) {
	// Cluster config issues
	g := gomega.NewGomegaWithT(t)
	configStr := `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - kubeconfig: /foo/bar
`
	testError(configStr, "Cluster: name missing", t, g)

	// Certificate config issues
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - kubeconfig: /foo/bar
    name: foo
certificates:
  - name: foo
    domains:
      - example.org
`
	testError(configStr, "Certificate: cert missing", t, g)
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - kubeconfig: /foo/bar
    name: foo
certificates:
  - cert: /foo
    domains:
      - example.org
`
	testError(configStr, "Certificate: name missing", t, g)
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - kubeconfig: /foo/bar
    name: foo
certificates:
  - cert: /foo
    name: foo
`
	testError(configStr, "Certificate: cert is not valid for any domain?", t, g)

	// overall config issues
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - kubeconfig: /foo/bar
    name: foo
`
	testError(configStr, "Certificate list missing", t, g)
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
certificates:
  - cert: /foo
    name: foo
    domains:
      - example.org
`
	testError(configStr, "Cluster list missing", t, g)
	configStr = `
haproxyTemplatePath: /foo/bar/test.cfg
certificates:
  - cert: /foo
    name: foo
    domains:
      - example.org
clusters:
  - kubeconfig: /foo/bar
    name: foo
`
	testError(configStr, "IP list missing", t, g)
}