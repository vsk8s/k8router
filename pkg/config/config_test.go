package config

import (
	"github.com/onsi/gomega"
	"io/ioutil"
	"os"
	"path"
	"testing"
)

func TestInvalidConfigParse(t *testing.T) {
	configStr := `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - name: testcluster
`
	dir := os.TempDir()
	file := path.Join(dir, "testfile")
	err := ioutil.WriteFile(file, []byte(configStr), 0644)
	if err != nil {
		t.Error(err)
		return
	}

	_, err = FromFile(file)
	if err == nil || err.Error() != "ClusterInternal: kubeconfig missing" {
		t.Fail()
	}
}

func TestDefaultConfigParse(t *testing.T) {
	g := gomega.NewGomegaWithT(t)

	configStr := `
haproxyTemplatePath: /foo/bar/test.cfg
clusters:
  - name: testcluster
    kubeconfig: /etc/kubernetes/kubeconfig.yml
certificates:
  - cert: /foo.pem
    key: /bar.pem
    domains:
      - example.org
`
	dir := os.TempDir()
	file := path.Join(dir, "testfile")
	err := ioutil.WriteFile(file, []byte(configStr), 0644)
	if err != nil {
		t.Error(err)
		return
	}

	uut, err := FromFile(file)
	if err != nil {
		t.Error(err)
		return
	}

	g.Expect(len(uut.Clusters)).To(gomega.BeIdenticalTo(1), "There should be 1 ClusterInternal.")
	g.Expect(len(uut.Certificates)).To(gomega.BeIdenticalTo(1), "There should be 1 CertificateInternal.")
	g.Expect(uut.Clusters[0].IngressNamespace).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressAppName).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressPort).To(gomega.BeIdenticalTo(80))
	g.Expect(uut.Certificates[0].IsWildcard).To(gomega.BeIdenticalTo(false))
}
