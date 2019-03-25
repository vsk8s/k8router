/*
	`k8router` is an Ingress watcher and a HAProxy config templating service.
	It aims to enable user-facing transparent multi-cluster deployments in Kubernetes clusters.
*/
package main

import (
	"fmt"
	"github.com/imdario/mergo"
	"github.com/jessevdk/go-flags"
	"github.com/op/go-logging"
	"gopkg.in/yaml.v2"
	v1beta1api "k8s.io/api/extensions/v1beta1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes/typed/extensions/v1beta1"
	"k8s.io/client-go/tools/clientcmd"
	"net"
	"os"
	"os/exec"
	"strings"
	"text/template"
)

var (
	backendIPs map[string][]string
	ingresses  map[string]map[string][]string

	frontendTemplate = template.Must(template.New("haproxy-frontend").Parse(haproxyFrontendTemplate))
	backendTemplate  = template.Must(template.New("haproxy-backend").Parse(haproxyBackendTemplate))

	logger = logging.MustGetLogger("k8router")

	opts          CommandlineFlags
	config        Config
	defaultConfig = Config{
		HAProxyConfigDir: "./",
	}
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
	HAProxyConfigDir string `yaml:"haproxy_config_dir"`
	ClusterConfigs   []struct {
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

	Debug   bool `short:"d" long:"debug" description:"Turn on debug output"`
	Verbose bool `short:"v" long:"verbose" description:"Turn on verbose output"`
	Silent  bool `short:"s" long:"silent" description:"Only log warnings and errors"`

	Version bool `long:"version" description:"Output version info and exit"`
}

func main() {
	_, err := flags.Parse(&opts)
	if err != nil {
		logger.Fatalf("Error parsing flags")
	}
	setupLogger()

	logger.Noticef("Starting k8router %s", version)
	logger.Notice("(C) 2019 The Kubernauts")

	if opts.Version {
		return
	}

	backendIPs = make(map[string][]string)
	ingresses = make(map[string]map[string][]string)
	changes := make(chan Change)
	rc := parseK8RouterConfig(opts.ConfigFile)

	logger.Info("Read config and templates, connecting to clusters")

	for _, cluster := range rc.Clusters {
		ingressWatcher, err := cluster.Client.Ingresses("").Watch(metav1.ListOptions{})
		if err != nil {
			// do something gracefully here
			logger.Fatalf("Could not watch ingresses: %s", err.Error())
		}

		go watchIngresses(ingressWatcher, cluster, changes)
	}

	logger.Info("Connected to the clusters, now applying changes")

	apply(changes)
}

func apply(changes <-chan Change) {
	for change := range changes {
		ingress, ok := change.Event.Object.(*v1beta1api.Ingress)
		if !ok {
			logger.Fatalf("Given object wasn't an ingress!")
		}

		logger.Noticef("%s %s %s", change.Event.Type, change.Cluster.Name, ingress.Name)
		changedHosts := make([]string, 0)

		// Get service IPs of the ingresses (we skip this in v1 of the proxy)
		// Then check the host spec and put the hosts in the HAProxy config
		switch change.Event.Type {
		case watch.Added:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				changedHosts = append(changedHosts, rule.Host)
				logger.Infof("Added IPs for cluster %s for host %s from ingress %s to maps", change.Cluster.Name, rule.Host, ingress.Name)
			}
		case watch.Deleted:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = sliceDifference(backendIPs[rule.Host], change.Cluster.IPs)
				changedHosts = append(changedHosts, rule.Host)
				logger.Infof("Deleted IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, rule.Host, ingress.Name)
			}
			delete(ingresses[change.Cluster.Name], ingress.Name)
		case watch.Modified:
			// First, we remove any existing hosts from the backend config
			hosts, found := ingresses[change.Cluster.Name][ingress.Name]
			if found {
				changedHosts = hosts
				for _, host := range ingresses[change.Cluster.Name][ingress.Name] {
					backendIPs[host] = sliceDifference(backendIPs[host], change.Cluster.IPs)
					logger.Infof("Deleted IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, host, ingress.Name)
				}
				delete(ingresses[change.Cluster.Name], ingress.Name)
			}
			// Then we add all hosts just as in the add function
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				changedHosts = append(changedHosts, rule.Host)
				logger.Infof("Added IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, rule.Host, ingress.Name)
			}
		case watch.Error:
			logger.Fatalf("Error watching ingresses in cluster %s", change.Cluster.Name)
			return
		default:
			return
		}

		// template the corresponding ingress config
		for _, host := range changedHosts {
			updateConfig(host)
			logger.Debugf("ingresses: %+v", ingresses)
			logger.Debugf("backendIPs: %+v", backendIPs)
		}
	}
}

func updateConfig(host string) {
	logger.Debugf("Writing config for host %s", host)
	printedHost := strings.Replace(host, ".", "-", -1)

	ips, ok := backendIPs[host]
	if !ok {
		panic("ingress not found")
	}

	// Remove the files if no valid Ingresses found
	if len(ips) == 0 {
		err := os.Remove(fmt.Sprintf(config.HAProxyConfigDir+"71-%s.conf", host))
		if err != nil {
			logger.Fatal(err.Error())
		}
		err = os.Remove(fmt.Sprintf(config.HAProxyConfigDir+"75-%s.conf", host))
		if err != nil {
			logger.Fatal(err.Error())
		}
		// Now it is safe to delete this host from the dictionary
		delete(backendIPs, host)
		logger.Infof("Deleted host %s from the backendIPs dictionary", host)
		return
	}

	frontendConfig, err := os.Create(fmt.Sprintf(config.HAProxyConfigDir+"71-%s.conf", host))
	if err != nil {
		logger.Fatal(err.Error())
	}

	err = frontendTemplate.Execute(frontendConfig, struct {
		Host       string
		ActualHost string
	}{printedHost, host})
	if err != nil {
		logger.Fatal(err.Error())
	}

	backendConfig, err := os.Create(fmt.Sprintf(config.HAProxyConfigDir+"75-%s.conf", host))
	if err != nil {
		logger.Fatal(err.Error())
	}
	err = backendTemplate.Execute(backendConfig, struct {
		Host string
		IPs  []string
	}{printedHost, backendIPs[host]})
	if err != nil {
		logger.Fatal(err.Error())
	}

	exec.Command("/bin/systemctl", "restart", "haproxy.service")
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
		logger.Fatal(err.Error())
	}

	decoder := yaml.NewDecoder(content)
	err = decoder.Decode(&config)
	if err != nil {
		logger.Fatal(err.Error())
	}

	err = mergo.Merge(&config, defaultConfig)
	if err != nil {
		logger.Fatal(err.Error())
	}

	if !strings.HasSuffix(config.HAProxyConfigDir, "/") {
		config.HAProxyConfigDir = config.HAProxyConfigDir + "/"
	}

	for _, clusterConfig := range config.ClusterConfigs {
		if clusterConfig.ClusterName == "" {
			logger.Fatal("Cluster name may not be empty")
		}

		for _, ip := range clusterConfig.IngressServiceIPs {
			if res := net.ParseIP(ip); res == nil {
				logger.Fatalf("Invalid IP in cluster %s: %s", clusterConfig.ClusterName, ip)
			}
		}

		kubeConfig, err := clientcmd.LoadFromFile(clusterConfig.ConfigPath)
		if err != nil {
			logger.Fatal(err.Error())
		}

		restConfig, err := clientcmd.NewDefaultClientConfig(*kubeConfig, &clientcmd.ConfigOverrides{}).ClientConfig()
		if err != nil {
			logger.Fatal(err.Error())
		}

		client, err := v1beta1.NewForConfig(restConfig)
		if err != nil {
			logger.Fatal(err.Error())
		}

		ingresses[clusterConfig.ClusterName] = make(map[string][]string)
		runtimeConfig.Clusters = append(runtimeConfig.Clusters,
			&ClusterConfig{
				clusterConfig.ClusterName,
				client,
				clusterConfig.IngressServiceIPs,
			})
		// todo: somehow test connection?
	}

	return runtimeConfig
}

// Compute the difference of two slices as and bs. The difference as - bs is
// intuitively defined as taking the elements of as and, for each element in bs,
// remove one corresponding element in as if it exists. See the test cases for
// some examples.
func sliceDifference(as, bs []string) []string {
	toDelete := make(map[string]int)
	res := make([]string, 0)

	for _, b := range bs {
		toDelete[b] += 1
	}

	for _, a := range as {
		if n, found := toDelete[a]; !found || n == 0 {
			res = append(res, a)
		} else {
			toDelete[a] -= 1
		}
	}
	return res
}

func setupLogger() {
	format := logging.MustStringFormatter(
		`[%{level:.8s}] (%{shortfunc}) - %{message}`,
	)
	backend := logging.NewBackendFormatter(logging.NewLogBackend(os.Stderr, "", 0), format)
	leveledBackend := logging.AddModuleLevel(backend)
	if opts.Debug {
		leveledBackend.SetLevel(logging.DEBUG, "")
	} else if opts.Verbose {
		leveledBackend.SetLevel(logging.INFO, "")
	} else if opts.Silent {
		leveledBackend.SetLevel(logging.WARNING, "")
	} else {
		leveledBackend.SetLevel(logging.NOTICE, "")
	}
	logging.SetBackend(leveledBackend)
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
