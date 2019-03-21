package main

import (
	"fmt"
	"gopkg.in/yaml.v2"
	v1beta1api "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"os"
	"strings"
	"text/template"
)

var (
	ingresses        map[string][]string
	frontendTemplate = template.Must(template.New("haproxy-frontend").Parse(haproxyFrontendTemplate))
	backendTemplate  = template.Must(template.New("haproxy-backend").Parse(haproxyBackendTemplate))
)

type ClusterConfig = struct {
	Client            *v1beta1.ExtensionsV1beta1Client
	IngressServiceIPs []string
}

type RuntimeConfig = struct {
	Clusters []ClusterConfig
}

type Config = struct {
	ClusterConfigs []struct {
		ConfigPath        string   `yaml:"k8s_config"`
		IngressServiceIPs []string `yaml:"ingress_ips"`
	} `yaml:"clusters"`
}

type Change = struct {
	Event *watch.Event
	IPs   []string
}

func main() {
	ingresses = map[string][]string{}
	rc := parseK8RouterConfig("./config")
	changes := make(chan Change)

	log.Printf("%+v %+v", frontendTemplate, backendTemplate)

	for _, cluster := range rc.Clusters {
		ingressWatcher, err := cluster.Client.Ingresses("").Watch(metav1.ListOptions{})
		if err != nil {
			// do something gracefully here
			panic(err.Error())
		}

		go watchIngresses(ingressWatcher, cluster.IngressServiceIPs, changes)
	}

	apply(changes)
}

func apply(changes <-chan Change) {
	for change := range changes {
		ingress, ok := change.Event.Object.(*v1beta1api.Ingress)
		if !ok {
			panic("not an ingress")
		}

		// Get service IPs of the ingresses (we skip this in v1 of the proxy)
		// Then check the host spec and put the hosts in the HAProxy config
		switch change.Event.Type {
		case watch.Added:
			for _, rule := range ingress.Spec.Rules {
				ingresses[rule.Host] = append(ingresses[rule.Host], change.IPs...)
			}
		case watch.Deleted:
			for _, rule := range ingress.Spec.Rules {
				ingresses[rule.Host] = sliceDifference(ingresses[rule.Host], change.IPs)
			}
		default:
			// something else, do nothing?
			return
		}

		// template the corresponding ingress config
		for _, rule := range ingress.Spec.Rules {
			updateConfig(rule.Host)
		}
	}
}

func updateConfig(host string) {
	log.Printf("%s: %+v", host, ingresses[host])
	printedHost := strings.Replace(host, ".", "-", -1)

	frontendConfig, err := os.Create(fmt.Sprintf("71-%s.conf", host))
	if err != nil {
		panic(err.Error())
	}

	err = frontendTemplate.Execute(frontendConfig, struct {
		Host       string
		ActualHost string
	}{printedHost, host})
	if err != nil {
		panic(err.Error())
	}

	backendConfig, err := os.Create(fmt.Sprintf("75-%s.conf", host))
	if err != nil {
		panic(err.Error())
	}
	err = backendTemplate.Execute(backendConfig, struct {
		Host string
		IPs  []string
	}{printedHost, ingresses[host]})
	if err != nil {
		panic(err.Error())
	}
}

func watchIngresses(watcher watch.Interface, ips []string, c chan Change) {
	for event := range watcher.ResultChan() {
		c <- Change{&event, ips}
	}
}

func parseK8RouterConfig(path string) RuntimeConfig {
	runtimeConfig := RuntimeConfig{}
	content, err := os.Open(path)
	if err != nil {
		panic(err.Error())
	}

	decoder := yaml.NewDecoder(content)
	config := Config{}
	err = decoder.Decode(&config)
	if err != nil {
		panic(err.Error())
	}

	// TODO: Validate the ingress ips here (check that they are actual ips)
	for _, clusterConfig := range config.ClusterConfigs {
		kubeConfig, err := clientcmd.LoadFromFile(clusterConfig.ConfigPath)
		if err != nil {
			panic(err.Error())
		}

		restConfig, err := clientcmd.NewDefaultClientConfig(*kubeConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			panic(err.Error())
		}

		client, err := v1beta1.NewForConfig(restConfig)
		if err != nil {
			panic(err.Error())
		}

		runtimeConfig.Clusters = append(runtimeConfig.Clusters, ClusterConfig{client, clusterConfig.IngressServiceIPs})
	}

	return runtimeConfig
}

func sliceDifference(ips, toRemove []string) (res []string) {
	m := make(map[string]bool)

	for _, ip := range toRemove {
		m[ip] = true
	}

	for _, ip := range ips {
		if _, ok := m[ip]; !ok {
			res = append(res, ip)
		}
	}
	return res
}

const haproxyFrontendTemplate = `    acl {{ .Host }} req_ssl_sni -i {{ .ActualHost }}
    use_backend some-backend if {{ .Host }}
`

const haproxyBackendTemplate = `{{- $host := .Host -}}
backend {{ .Host }}
    mode http
    balance leastconn
    stick-table type ip size 20k peers my-peer
    stick on src
    option httpchk GET / more-httpchk-option {{ .Host }}
{{- range $i, $ip := .IPs }}
    server worker-{{ $host }}-{{ $i }} {{ $ip }}:80 check send-proxy
{{- end }}
`
