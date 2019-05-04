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

// This struct takes care of assembling all cluster state and then using it to write a HAProxy config
type Handler struct {
	// Parsed template for HAProxy config
	template *template.Template
	// Reference to application configuration
	config config.Config
	// Channel with updates from clusters
	updates chan state.ClusterState
	// Channel internally used to stop our goroutine
	stop chan bool
	// Map of clusters to their current state
	clusterState map[string]state.ClusterState
	// Number of changes since the last time we wrote everything to disk
	numChanges int
	// Current cluster state prepared for templating
	templateInfo TemplateInfo
	// Debug use only: Can be used to be notified when stuff is written to disk
	debugFileEventChannel chan bool
}

// Initialize a new Handler
func Init(updates chan state.ClusterState, config config.Config) (*Handler, error) {
	parsedTemplate, err := template.ParseFiles(config.HAProxyTemplatePath)
	if err != nil {
		return nil, err
	}
	parsedTemplate.Funcs(template.FuncMap{"StringJoin": strings.Join})
	return &Handler{
		updates:      updates,
		numChanges:   0,
		template:     parsedTemplate,
		clusterState: make(map[string]state.ClusterState),
		config:       config,
		stop:         make(chan bool),
	}, nil
}

// Write a new config to disk
func (h *Handler) updateConfig() {
	log.Debug("Writing myConfigFile")

	// TODO: Respect file mode setting
	myConfigFile, err := os.OpenFile(h.config.HAProxyDropinPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.WithField("path", h.config.HAProxyDropinPath).WithError(err).Fatal(
			"Couldn't open haproxy dropin path for writing")
	}

	err = h.template.Execute(myConfigFile, h.templateInfo)
	if err != nil {
		log.WithError(err).Fatal("Couldn't template haproxy myConfigFile")
	}

	// TODO: Replace with systemd API
	if h.debugFileEventChannel == nil {
		// We're not debugging/testing
		err = exec.Command("sudo", "/bin/systemctl", "restart", "haproxy.service").Run()
		if err != nil {
			log.WithError(err).Fatal("Couldn't reload haproxy")
		}
	}
}

// Regenerate the templateInfo struct
func (h *Handler) rebuildConfig() {
	/* The HAProxy config we write works (simplified) like this:
	 *  * There is a frontend that splits request according to SNI
	 *  * For each certificate, we have a backend where those SNI request go to and another frontend with that cert
	 *  * Each of these frontends does "normal" host-style case distinction and then
	 *  * routes to a combination of backends
	 */

	// Step 1: For each Ingress, which backends does it have?
	hostToClusters := map[string][]string{}
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
		if _, ok := backendCombinationList[key]; !ok {
			// We haven't seen this particular backend combination yet
			var backends []Backend
			for _, cluster := range clusters {
				for _, backend := range h.clusterState[cluster].Backends {
					backends = append(backends, Backend{
						IP:   backend.IP,
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
		IPs:                    h.config.IPs,
	}

	// Step 3: Which SNIs do we have in our certs (first frontend and it's backends)
	localForwardPort := 12345 // TODO(uubk): Make configurable
	hostToCert := map[string]string{}
	for _, cert := range h.config.Certificates {
		// For each host: Figure out whether we actually have a backend there
		var actuallyUsedHosts []string
		isWildcard := false
		for _, host := range cert.Domains {
			if strings.Contains(host, "*") {
				isWildcard = true
				suffix := strings.Trim(host, "*")
				for actualHost := range hostToBackend {
					if strings.HasSuffix(actualHost, suffix) {
						actuallyUsedHosts = append(actuallyUsedHosts, actualHost)
						hostToCert[actualHost] = cert.Name
					}
				}
			} else {
				if _, ok := hostToBackend[host]; ok {
					actuallyUsedHosts = append(actuallyUsedHosts, host)
					hostToCert[host] = cert.Name
				}
			}
		}
		if len(actuallyUsedHosts) == 0 && !isWildcard {
			log.WithField("cert", cert.Name).Info("Skipping certificate as it seems to be unused.")
			continue
		}
		currentCert := SniDetail{
			Domains:          actuallyUsedHosts,
			IsWildcard:       isWildcard,
			Path:             cert.Cert,
			LocalForwardPort: localForwardPort,
		}
		cfg.SniList[cert.Name] = currentCert
		localForwardPort += 1
		if isWildcard {
			cfg.DefaultWildcardCert = cert.Name
		}
	}

	// Check whether we have hosts without certs
	for host := range hostToBackend {
		if _, ok := hostToCert[host]; !ok {
			log.WithField("host", host).Warning("Host skipped because it is not covered by any certificate!")
		}
	}

	h.templateInfo = cfg
}

// Main event loop
func (h *Handler) eventLoop() {
	updateTicks := time.NewTicker(1 * time.Second)
	for {
		select {
		case _ = <-h.stop:
			log.Debug("Returning from event loop after stop request")
			return
		case event := <-h.updates:
			h.clusterState[event.Name] = event
			h.numChanges++
		case _ = <-updateTicks.C:
			if h.numChanges > 0 {
				// There is something to do
				h.numChanges = 0
				log.WithField("clusterState", h.clusterState).Debug("Rebuilding config")
				h.rebuildConfig()
				log.WithField("templateInfo", h.templateInfo).Debug("Templating config")
				h.updateConfig()
				if h.debugFileEventChannel != nil {
					h.debugFileEventChannel <- true
				}
			}
		}
	}
}

// Start handling events
func (h *Handler) Start() {
	go h.eventLoop()
}

// Stop handling events
func (h *Handler) Stop() {
	h.stop <- true
}
