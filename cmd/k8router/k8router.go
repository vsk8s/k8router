/*
	`k8router` provides a simple Ingress watcher and HAProxy config templating service.
	It aims to enable user-facing transparent multi-cluster deployments in Kubernetes clusters
*/
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
	backendIPs       map[string][]string
	ingresses        map[string]map[string][]string
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
	Clusters []*ClusterConfig
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
	Cluster *ClusterConfig
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

	backendIPs = make(map[string][]string)
	ingresses = make(map[string]map[string][]string)
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
		changedHosts := make([]string, 0)

		// Get service IPs of the ingresses (we skip this in v1 of the proxy)
		// Then check the host spec and put the hosts in the HAProxy config
		switch change.Event.Type {
		case watch.Added:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				changedHosts = append(changedHosts, rule.Host)
			}
		case watch.Deleted:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = sliceDifference(backendIPs[rule.Host], change.Cluster.IPs)
				changedHosts = append(changedHosts, rule.Host)
			}
			delete(ingresses[change.Cluster.Name], ingress.Name)
		case watch.Modified:
			// First, we remove any existing hosts from the backend config
			hosts, found := ingresses[change.Cluster.Name][ingress.Name]
			if found {
				changedHosts = hosts
				for _, host := range ingresses[change.Cluster.Name][ingress.Name] {
					backendIPs[host] = sliceDifference(backendIPs[host], change.Cluster.IPs)
				}
				delete(ingresses[change.Cluster.Name], ingress.Name)
			}
			// Then we add all hosts just as in the add function
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				changedHosts = append(changedHosts, rule.Host)
			}
		case watch.Error:
			log.Printf("Error watching ingresses in cluster %s", change.Cluster.Name)
			return
		default:
			return
		}

		// template the corresponding ingress config
		for _, host := range changedHosts {
			log.Printf("%+v", ingresses)
			log.Printf("%+v", backendIPs)
			updateConfig(host)
		}
	}
}

func updateConfig(host string) {
	log.Printf("  %s", host)
	printedHost := strings.Replace(host, ".", "-", -1)

	ips, ok := backendIPs[host]
	if !ok {
		panic("ingress not found")
	}

	// Remove the files if no valid Ingresses found
	if len(ips) == 0 {
		err := os.Remove(fmt.Sprintf("71-%s.conf", host))
		if err != nil {
			panic(err)
		}
		err = os.Remove(fmt.Sprintf("75-%s.conf", host))
		if err != nil {
			panic(err)
		}
		// Now it is safe to delete this ingress from the dictionary
		delete(backendIPs, host)
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
	}{printedHost, backendIPs[host]})
	if err != nil {
		panic(err.Error())
	}

	// todo: restart haproxy
}

func watchIngresses(watcher watch.Interface, cluster *ClusterConfig, c chan Change) {
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

		ingresses[clusterConfig.ClusterName] = make(map[string][]string)
		runtimeConfig.Clusters = append(runtimeConfig.Clusters,
			&ClusterConfig{
				clusterConfig.ClusterName,
				client,
				clusterConfig.IngressServiceIPs,
			})
	}

	return runtimeConfig
}

func sliceDifference(ips, toRemove []string) []string {
	m := make(map[string]int)
	res := make([]string, 0)

	for _, ip := range toRemove {
		m[ip] += 1
	}

	for _, ip := range ips {
		if toRemove, found := m[ip]; !found || toRemove == 0 {
			res = append(res, ip)
		} else {
			m[ip] -= 1
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
