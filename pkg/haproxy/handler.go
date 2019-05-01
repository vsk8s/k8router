package haproxy

import (
	log "github.com/sirupsen/logrus"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"time"
)

type Handler struct {
	template *template.Template
	config   config.Config
	updates  chan state.ClusterState
	clusterState map[string]state.ClusterState
	numChanges int
	templateInfo TemplateInfo
}

func Init(updates chan state.ClusterState, config config.Config) (*Handler, error) {
	parsedTemplate, err := template.ParseFiles(config.HAProxyTemplatePath)
	if err != nil {
		return nil, err
	}
	parsedTemplate.Funcs(template.FuncMap{"StringJoin": strings.Join})
	return &Handler{
		updates:    updates,
		numChanges: 0,
		template:   parsedTemplate,
		clusterState: make(map[string]state.ClusterState),
	}, nil
}

func (h *Handler) updateConfig() {
	log.Debug("Writing myConfigFile")

	// TODO: Respect file mode setting
	myConfigFile, err := os.OpenFile(h.config.HAProxyDropinPath, os.O_TRUNC|os.O_CREATE, 0644)
	if err != nil {
		log.WithError(err).Fatal("Couldn't open haproxy myConfigFile for writing")
	}

	err = h.template.Execute(myConfigFile, h.templateInfo)
	if err != nil {
		log.WithError(err).Fatal("Couldn't template haproxy myConfigFile")
	}

	// TODO: Replace with systemd API
	err = exec.Command("sudo", "/bin/systemctl", "restart", "haproxy.service").Run()
	if err != nil {
		log.WithError(err).Fatal("Couldn't reload haproxy")
	}
}

func (h* Handler) rebuildConfig() {
	/* The HAProxy config we write works (simplified) like this:
	 *  * There is a frontend that splits request according to SNI
	 *  * For each certificate, we have a backend where those SNI request go to and another frontend with that cert
	 *  * Each of these frontends does "normal" host-style case distinction and then
	 *  * routes to a combination of backends
	 */

	// Step 1: For each Ingress, which backends does it have?
	hostToClusters := make(map[string][]string)
	for _, cluster := range h.clusterState {
		for _, ingress := range cluster.Ingresses {
			for _, host := range ingress.Hosts {
				hostToClusters[host] = append(hostToClusters[host], cluster.Name)
			}
		}
	}

	// Step 2: Make a map of backend combinations to ips and of hosts to backend combinations
	hostToBackend := map[string]string{}
	backendCombinationList := map[string][]Backend{}
	hosts := map[string]bool{}
	for host, clusters := range hostToClusters {
		hosts[host] = true
		sort.Strings(clusters)
		key := strings.Join(clusters, "-")
		if _, ok := backendCombinationList[key] ; !ok {
			// We haven't seen this particular backend combination yet
			var backends []Backend
			for _, cluster := range clusters {
				for _, backend := range h.clusterState[cluster].Backends {
					backends = append(backends, Backend{
						IP: backend.IP,
						Name: backend.Name,
					})
				}
			}
			backendCombinationList[key] = backends
		}
		hostToBackend[host] = key
	}

	cfg := TemplateInfo{
		SniList:                make(map[string]SniDetail),
		BackendCombinationList: backendCombinationList,
		HostToBackend:          hostToBackend,
	}

	// Step 3: Which SNIs do we have in our certs (first frontend and it's backends)
	localForwardPort := 12345; // TODO(uubk): Make configurable
	for _, cert := range h.config.Certificates {
		// For each host: Figure out whether we actually have a backend there
		var actuallyUsedHosts []string
		for _, host := range cert.Domains {
			if strings.Contains(host, "*") {
				suffix := strings.Trim(host, "*")
				for actualHost := range hostToBackend {
					if strings.HasSuffix(actualHost, suffix) {
						actuallyUsedHosts = append(actuallyUsedHosts, actualHost)
					}
				}
			} else {
				if _, ok := hostToBackend[host]; ok {
					actuallyUsedHosts = append(actuallyUsedHosts, host)
				}
			}
		}
		currentCert := SniDetail{
			Domains: actuallyUsedHosts,
			IsWildcard: cert.IsWildcard,
			Path: cert.Cert, //TODO(uubk): Fix
			LocalForwardPort: localForwardPort,
		}
		cfg.SniList[cert.Name] = currentCert
		localForwardPort+=1
		if cert.IsWildcard {
			cfg.DefaultWildcardCert = cert.Name
		}
	}
	h.templateInfo = cfg
}

func (h* Handler) eventLoop() {
	updateTicks := time.NewTicker(1 * time.Second)
	for {
		select {
		case event := <- h.updates:
			h.clusterState[event.Name] = event
			h.numChanges++
		case _ = <- updateTicks.C:
			if h.numChanges > 0 {
				// There is something to do
				h.numChanges = 0
				h.rebuildConfig()
				h.updateConfig()
			}
		}
	}
}