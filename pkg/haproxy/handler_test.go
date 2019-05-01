package haproxy

import (
	"bytes"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
	"net"
	"strings"
	"testing"
	"text/template"
)

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
	uut.clusterState["default"] = state.ClusterState{
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
	var err error
	uut.template = template.New("template")
	uut.template = uut.template.Funcs(template.FuncMap{"StringJoin": strings.Join})
	uut.template, err = uut.template.ParseFiles("../../template")
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
	print(buf.String())
}
