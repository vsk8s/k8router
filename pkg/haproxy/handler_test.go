package haproxy

import (
	"bytes"
	"github.com/onsi/gomega"
	"github.com/vsk8s/k8router/pkg/config"
	"github.com/vsk8s/k8router/pkg/state"
	"net"
	"os"
	"path"
	"strings"
	"testing"
	"text/template"
)

func findFile(name string) string {
	cwd, err := os.Getwd()
	if err != nil {
		panic(err)
	}

	candidates := []string{
		path.Join(cwd, name),
		path.Join(path.Dir(cwd), name),
		path.Join(path.Dir(path.Dir(cwd)), name),
		path.Join(path.Dir(path.Dir(path.Dir(cwd))), name),
		path.Join(path.Dir(path.Dir(path.Dir(path.Dir(cwd)))), name),
		path.Join(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(cwd))))), name),
		path.Join(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(cwd)))))), name),
		path.Join(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(path.Dir(cwd))))))), name),
	}
	for _, candidate := range candidates {
		_, err = os.Stat(candidate)
		if err == nil {
			return candidate
		}
	}

	panic("Couldn't find file")
}

func dummyClusterState() state.ClusterState {
	ip := net.IPv4(127, 0, 0, 1)
	return state.ClusterState{
		Name: "default",
		Backends: []state.K8RouterBackend{
			{
				Name: "foobar",
				IP:   &ip,
			},
		},
		Ingresses: []state.K8RouterIngress{
			{
				Name: "example-ingress",
				Hosts: []string{
					"test.example.org",
				},
			},
			{
				Name: "example2-ingress",
				Hosts: []string{
					"foo.example.org",
				},
			},
		},
	}
}

// Test templating of the configuration *only*
func TestConfigGeneration(t *testing.T) {
	uut := Handler{
		clusterState: make(map[string]state.ClusterState),
	}
	ip := net.IPv4(127, 0, 0, 1)
	cert := config.CertificateInternal{
		Name: "dummycert",
		Domains: []string{
			"*.example.org",
		},
		Cert: "/etc/ssl/dummy.pem",
	}
	cert2 := config.CertificateInternal{
		Name: "dummycert2",
		Domains: []string{
			"doc.example.org",
			"foo.example.org",
		},
		Cert: "/etc/ssl/dummy.pem",
	}
	uut.config = config.Config{
		Certificates: []config.Certificate{
			{
				CertificateInternal: &cert,
			},
			{
				CertificateInternal: &cert2,
			},
		},
		IPs: []*net.IP{
			&ip,
		},
	}
	uut.clusterState["default"] = dummyClusterState()
	var err error
	uut.template = template.New("template")
	uut.template = uut.template.Funcs(template.FuncMap{"StringJoin": strings.Join})
	uut.template, err = uut.template.ParseFiles(findFile("template"))
	if err != nil {
		t.Error(err)
		return
	}
	uut.rebuildConfig()
	s := ""
	buf := bytes.NewBufferString(s)
	err = uut.template.Execute(buf, uut.templateInfo)
	if err != nil {
		t.Error(err)
		return
	}
	// TODO: Validate the configuration somehow.
	print(buf.String())
}

// Test whole class
func TestConfigEventLoop(t *testing.T) {
	g := gomega.NewGomegaWithT(t)
	dir := os.TempDir()
	dropinFile := path.Join(dir, "testfile")

	ip := net.IPv4(127, 0, 0, 1)
	cert := config.CertificateInternal{
		Name: "dummycert",
		Domains: []string{
			"*.example.org",
		},
		Cert: "/etc/ssl/dummy.pem",
	}
	cert2 := config.CertificateInternal{
		Name: "dummycert2",
		Domains: []string{
			"doc.example.org",
			"foo.example.org",
		},
		Cert: "/etc/ssl/dummy.pem",
	}
	cluster := config.ClusterInternal{
		Name: "default",
	}

	configObj := config.Config{
		HAProxyTemplatePath: findFile("template"),
		HAProxyDropinPath:   dropinFile,
		HAProxyDropinMode:   "775",
		Certificates: []config.Certificate{
			{
				CertificateInternal: &cert,
			},
			{
				CertificateInternal: &cert2,
			},
		},
		IPs: []*net.IP{
			&ip,
		},
		Clusters: []config.Cluster{
			{
				ClusterInternal: &cluster,
			},
		},
	}
	eventChannel := make(chan state.ClusterState)
	debugEventChannel := make(chan bool)

	uut, err := Init(eventChannel, configObj)
	g.Expect(err).To(gomega.BeNil(), "Unexpected initialization error")
	uut.debugFileEventChannel = debugEventChannel
	uut.Start()

	eventChannel <- dummyClusterState()
	// Wait until the config file has actually been written!
	_ = <-uut.debugFileEventChannel
	uut.Stop()

	// TODO: Validate the configuration somehow.
	fileInfo, err := os.Stat(dropinFile)
	g.Expect(err).To(gomega.BeNil(), "Unexpected error when inspecting generated file")
	g.Expect(fileInfo.Size()).To(gomega.BeNumerically(">=", 100), "Generated file should be at least 100 bytes")
}
