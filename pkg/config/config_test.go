package config

import (
	"github.com/onsi/gomega"
	"io/ioutil"
	"net"
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
	if err == nil || err.Error() != "Cluster: kubeconfig missing" {
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
  - cert: /foo
    domains:
      - example.org
ips:
  - 127.0.0.1
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

	g.Expect(len(uut.Clusters)).To(gomega.BeIdenticalTo(1), "There should be 1 cluster.")
	g.Expect(len(uut.Certificates)).To(gomega.BeIdenticalTo(1), "There should be 1 certificate.")
	g.Expect(uut.Clusters[0].IngressNamespace).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressAppName).To(gomega.BeIdenticalTo("ingress-nginx"))
	g.Expect(uut.Clusters[0].IngressPort).To(gomega.BeIdenticalTo(80))
	g.Expect(len(uut.IPs)).To(gomega.BeIdenticalTo(1))
	g.Expect(*uut.IPs[0]).To(gomega.BeEquivalentTo(net.ParseIP("127.0.0.1")))
}
