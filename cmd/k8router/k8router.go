package main

import (
	"errors"
	"fmt"
	"github.com/jessevdk/go-flags"
	"gopkg.in/yaml.v2"
	v1beta1api "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
	"k8s.io/client-go/tools/clientcmd"
	"log"
	"net"
	"os"
	"strings"
	"text/template"
)

var (
	ingresses        map[string][]string
	frontendTemplate = template.Must(template.New("haproxy-frontend").Parse(haproxyFrontendTemplate))
	backendTemplate  = template.Must(template.New("haproxy-backend").Parse(haproxyBackendTemplate))

	opts CommandlineFlags
)

const (
	version = "v0.1.0"
)

type ClusterConfig = struct {
	Name   string
	Client *v1beta1.ExtensionsV1beta1Client
	IPs    []string
}

type RuntimeConfig = struct {
	Clusters []ClusterConfig
}

type Config = struct {
	ClusterConfigs []struct {
		ClusterName       string   `yaml:"name"`
		ConfigPath        string   `yaml:"k8s_config"`
		IngressServiceIPs []string `yaml:"ingress_ips"`
	} `yaml:"clusters"`
}

type Change = struct {
	Event   *watch.Event
	Cluster ClusterConfig
}

type CommandlineFlags = struct {
	ConfigFile string `short:"c" long:"config" description:"Configuration file to use" default:"/etc/k8router/config.yml"`
	Version    bool   `long:"version" description:"Output version info and exit"`
}

func main() {
	log.Printf("Starting k8router %s", version)
	log.Printf("(c) 2019 The Kubernauts")

	_, err := flags.Parse(&opts)
	if err != nil {
		return
	}

	if opts.Version {
		return
	}

	ingresses = map[string][]string{}
	rc := parseK8RouterConfig(opts.ConfigFile)
	changes := make(chan Change)

	log.Print("Read config and templates, connecting to clusters")

	for _, cluster := range rc.Clusters {
		ingressWatcher, err := cluster.Client.Ingresses("").Watch(metav1.ListOptions{})
		if err != nil {
			// do something gracefully here
			panic(err.Error())
		}

		go watchIngresses(ingressWatcher, cluster, changes)
	}

	log.Printf("Connected to the clusters, now applying changes")

	apply(changes)
}

func apply(changes <-chan Change) {
	for change := range changes {
		ingress, ok := change.Event.Object.(*v1beta1api.Ingress)
		if !ok {
			panic("not an ingress")
		}

		log.Printf("%s %s %s", change.Event.Type, change.Cluster.Name, ingress.Name)

		// Get service IPs of the ingresses (we skip this in v1 of the proxy)
		// Then check the host spec and put the hosts in the HAProxy config
		switch change.Event.Type {
		case watch.Added:
			for _, rule := range ingress.Spec.Rules {
				ingresses[rule.Host] = append(ingresses[rule.Host], change.Cluster.IPs...)
			}
		case watch.Deleted:
			for _, rule := range ingress.Spec.Rules {
				ingresses[rule.Host] = sliceDifference(ingresses[rule.Host], change.Cluster.IPs)
			}
		case watch.Modified:
			log.Printf("Ingress modified, this is not supported at the moment!")
			return
		case watch.Error:
			log.Printf("Error watching ingresses")
			return
		default:
			return
		}

		// template the corresponding ingress config
		for _, rule := range ingress.Spec.Rules {
			updateConfig(rule.Host)
		}
	}
}

func updateConfig(host string) {
	log.Printf("  %s", host)
	printedHost := strings.Replace(host, ".", "-", -1)

	ings, ok := ingresses[host]
	if !ok {
		panic("ingress not found")
	}

	// Remove the files if no valid Ingresses found
	if len(ings) == 0 {
		err := os.Remove(fmt.Sprintf("71-%s.conf", host))
		if err != nil {
			panic(err)
		}
		err = os.Remove(fmt.Sprintf("75-%s.conf", host))
		if err != nil {
			panic(err)
		}
		return
	}

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

	// todo: restart haproxy
}

func watchIngresses(watcher watch.Interface, cluster ClusterConfig, c chan Change) {
	for event := range watcher.ResultChan() {
		c <- Change{&event, cluster}
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

	for _, clusterConfig := range config.ClusterConfigs {
		if clusterConfig.ClusterName == "" {
			panic("cluster name may not be empty")
		}

		for _, ip := range clusterConfig.IngressServiceIPs {
			if res := net.ParseIP(ip); res == nil {
				panic(errors.New(fmt.Sprintf("invalid IP: %s", ip)))
			}
		}

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

		runtimeConfig.Clusters = append(runtimeConfig.Clusters,
			ClusterConfig{
				clusterConfig.ClusterName,
				client,
				clusterConfig.IngressServiceIPs,
			})
	}

	return runtimeConfig
}

func sliceDifference(ips, toRemove []string) (res []string) {
	m := make(map[string]bool)

	for _, ip := range toRemove {
		m[ip] = false
	}

	for _, ip := range ips {
		if alreadyDeleted, found := m[ip]; !found || alreadyDeleted {
			res = append(res, ip)
		} else {
			m[ip] = true
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
