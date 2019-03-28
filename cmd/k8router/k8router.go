/*
	`k8router` is an Ingress watcher and a HAProxy config templating service.
	It aims to enable user-facing transparent multi-cluster deployments in Kubernetes clusters.
*/
package main

import (
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

	t          *template.Template
	configFile string

	logger = logging.MustGetLogger("k8router")

	opts          CommandlineFlags
	config        Config
	defaultConfig = Config{
		GoTemplatePath: "./template",
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
	GoTemplatePath   string `yaml:"go_template"`
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


	configFile = config.HAProxyConfigDir + "71-k8router.conf"
	logger.Debugf("Config file: %s", configFile)
	logger.Debug("Trying to remove existing config file")
	err = os.Remove(configFile)
	if err != nil {
		// ignore the error
		logger.Debug("Could not delete potentially existing config, ignoring: " + err.Error())
	}

	logger.Debugf("Parsing template")
	t = mustParseTemplate(config.GoTemplatePath)

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

		// Get service IPs of the ingresses (we skip this in v1 of the proxy)
		// Then check the host spec and put the hosts in the HAProxy config
		switch change.Event.Type {
		case watch.Added:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				logger.Infof("Added IPs for cluster %s for host %s from ingress %s to maps", change.Cluster.Name, rule.Host, ingress.Name)
			}
		case watch.Deleted:
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = sliceDifference(backendIPs[rule.Host], change.Cluster.IPs)
				if len(backendIPs[rule.Host]) == 0 {
					delete(backendIPs, rule.Host)
				}
				logger.Infof("Deleted IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, rule.Host, ingress.Name)
			}
			delete(ingresses[change.Cluster.Name], ingress.Name)
		case watch.Modified:
			// First, we remove any existing hosts from the backend config
			hosts, found := ingresses[change.Cluster.Name][ingress.Name]
			if found {
				for _, host := range hosts {
					backendIPs[host] = sliceDifference(backendIPs[host], change.Cluster.IPs)
					if len(backendIPs[host]) == 0 {
						delete(backendIPs, host)
					}
					logger.Infof("Deleted IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, host, ingress.Name)
				}
				delete(ingresses[change.Cluster.Name], ingress.Name)
			}
			// Then we add all hosts just as in the add function
			for _, rule := range ingress.Spec.Rules {
				backendIPs[rule.Host] = append(backendIPs[rule.Host], change.Cluster.IPs...)
				ingresses[change.Cluster.Name][ingress.Name] = append(ingresses[change.Cluster.Name][ingress.Name], rule.Host)
				logger.Infof("Added IPs for cluster %s and host %s from ingress %s", change.Cluster.Name, rule.Host, ingress.Name)
			}
		case watch.Error:
			logger.Fatalf("Error watching ingresses in cluster %s", change.Cluster.Name)
			return
		default:
			logger.Fatalf("Something unexpected happened")
			return
		}

		logger.Debugf("ingresses: %+v", ingresses)
		logger.Debugf("backendIPs: %+v", backendIPs)

		updateConfig()
	}
}

func updateConfig() {
	logger.Debugf("Writing config")

	config, err := os.Create(configFile)
	if err != nil {
		logger.Fatal(err.Error())
	}

	err = t.Execute(config, struct {
		BackendIPs map[string][]string
	}{BackendIPs: backendIPs})
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

func mustParseTemplate(path string) *template.Template {
	t, err := template.ParseFiles(path)
	if err != nil {
		logger.Fatalf(err.Error())
	}
	return t
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
