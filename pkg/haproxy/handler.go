package haproxy

import (
	log "github.com/sirupsen/logrus"
	"github.com/vsk8s/k8router/pkg/config"
	"github.com/vsk8s/k8router/pkg/state"
	"io/ioutil"
	"os"
	"os/exec"
	"sort"
	"strings"
	"text/template"
	"time"
)

// Handler assembles all ClusterStates and configure haproxy
type Handler struct {
	config config.Config

	updates chan state.ClusterState

	// cluster name to state
	clusterState map[string]state.ClusterState

	template *template.Template

	// Current state for templating
	templateInfo TemplateInfo

	haproxyNeedsUpdate bool

	// Channel to stop our goroutine
	stopper chan bool

	// Debug use only: Be notified when template is written to disk
	debugFileEventChannel chan bool
}

// Initialize a new Handler
func Initialize(updates chan state.ClusterState, config config.Config) (*Handler, error) {
	rawTemplateString, err := ioutil.ReadFile(config.HAProxyTemplatePath)
	if err != nil {
		return nil, err
	}
	parsedTemplate, err := template.New("template").Funcs(template.FuncMap{
		"StringJoin": strings.Join,
		"replace":    func(s, old, new string) string { return strings.Replace(s, old, new, -1) },
	}).Parse(string(rawTemplateString))
	if err != nil {
		return nil, err
	}
	return &Handler{
		updates:            updates,
		haproxyNeedsUpdate: false,
		template:           parsedTemplate,
		clusterState:       make(map[string]state.ClusterState),
		config:             config,
		stopper:            make(chan bool),
	}, nil
}

// Start the handler
func (h *Handler) Start() {
	go h.eventLoop()
}

// Stop the handler
func (h *Handler) Stop() {
	h.stopper <- true
}

func (h *Handler) eventLoop() {
	updateTicks := time.NewTicker(1 * time.Second)
	for {
		select {
		case _ = <-h.stopper:
			log.Debug("Returning from event loop after stop request")
			return
		case newState := <-h.updates:
			currentState := h.clusterState[newState.Name]
			if !state.IsClusterStateEquivalent(&currentState, &newState) {
				h.clusterState[newState.Name] = newState
				h.haproxyNeedsUpdate = true
			}
		case _ = <-updateTicks.C:
			if h.haproxyNeedsUpdate {
				h.haproxyNeedsUpdate = false
				log.WithField("clusterState", h.clusterState).Debug("Rebuilding config")
				h.regenerateTemplateInfo()
				log.WithField("templateInfo", h.templateInfo).Debug("Templating config")
				h.writeConfigToHAProxy()
				if h.debugFileEventChannel != nil {
					h.debugFileEventChannel <- true
				}
			}
		}
	}
}

func (h *Handler) regenerateTemplateInfo() {
	/* The HAProxy config we write works (simplified) like this:
	 *  * There is a frontend that splits request according to SNI
	 *  * We have a backend where all SNI requests go to and another frontend
	 *  * This frontend does "normal" host-style case distinction and then
	 *  * routes to a combination of backends
	 */

	hostToClusters := h.computeHostToClusterMap()
	hostToBackend, backendCombinationList := h.computeBackends(hostToClusters)
	hostToCert, sniList, defaultCert := h.computeCertsForHosts(hostToBackend)

	h.warnAboutMissingCerts(hostToBackend, hostToCert)

	h.templateInfo = TemplateInfo{
		SniList:                sniList,
		BackendCombinationList: backendCombinationList,
		HostToBackend:          hostToBackend,
		IPs:                    h.config.IPs,
		DefaultWildcardCert:    defaultCert,
	}
}

func (h *Handler) computeCertsForHosts(hostToBackend map[string]string) (map[string]string, map[string]SniDetail, string) {
	// TODO(uubk): Make configurable
	localForwardPort := 12345
	hostToCert := map[string]string{}
	sniList := map[string]SniDetail{}
	defaultCert := ""
	for _, cert := range h.config.Certificates {
		// For each host: Figure out whether we actually have a backend there
		var hostsUsingCurrentCert []string
		isWildcard := false
		for _, host := range cert.Domains {
			if strings.Contains(host, "*") {
				isWildcard = true
				domain := strings.Trim(host, "*")
				for host := range hostToBackend {
					if strings.HasSuffix(host, domain) {
						hostsUsingCurrentCert = append(hostsUsingCurrentCert, host)
						hostToCert[host] = cert.Name
					}
				}
			} else {
				if _, ok := hostToBackend[host]; ok {
					hostsUsingCurrentCert = append(hostsUsingCurrentCert, host)
					hostToCert[host] = cert.Name
				}
			}
		}
		currentCert := SniDetail{
			Domains:          hostsUsingCurrentCert,
			IsWildcard:       isWildcard,
			Path:             cert.Cert,
			LocalForwardPort: localForwardPort,
		}
		sniList[cert.Name] = currentCert
		localForwardPort++
		if isWildcard {
			defaultCert = cert.Name
		}
	}
	return hostToCert, sniList, defaultCert
}

func (h *Handler) computeBackends(hostToClusters map[string][]string) (map[string]string, map[string][]Backend) {
	hostToBackendCombination := map[string]string{}
	backendCombinationList := map[string][]Backend{}
	for host, clusters := range hostToClusters {
		sort.Strings(clusters)
		backendCombination := strings.Join(clusters, "-")
		if _, ok := backendCombinationList[backendCombination]; !ok {
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
			backendCombinationList[backendCombination] = backends
		}
		hostToBackendCombination[host] = backendCombination
	}
	return hostToBackendCombination, backendCombinationList
}

func (h *Handler) computeHostToClusterMap() map[string][]string {
	hostToClusters := map[string][]string{}
	for _, cluster := range h.clusterState {
		for _, ingress := range cluster.Ingresses {
			for _, host := range ingress.Hosts {
				hostToClusters[host] = append(hostToClusters[host], cluster.Name)
			}
		}
	}
	return hostToClusters
}

func (h *Handler) writeConfigToHAProxy() {
	log.Debug("Writing config")

	// TODO: Respect file mode setting
	myConfigFile, err := os.OpenFile(h.config.HAProxyDropinPath, os.O_TRUNC|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		log.WithField("path", h.config.HAProxyDropinPath).WithError(err).Fatal(
			"Couldn't open haproxy dropin path for writing")
	}

	err = h.template.Execute(myConfigFile, h.templateInfo)
	if err != nil {
		log.WithError(err).Fatal("Couldn't template haproxy config")
	}

	// TODO: Replace with systemd API
	if h.debugFileEventChannel == nil {
		// We're not debugging/testing
		err = exec.Command("sudo", "/bin/systemctl", "reload", "haproxy.service").Run()
		if err != nil {
			log.WithError(err).Fatal("Couldn't reload haproxy")
		}
	}
}

func (h *Handler) warnAboutMissingCerts(hostToBackend map[string]string, hostToCert map[string]string) {
	for host := range hostToBackend {
		if _, ok := hostToCert[host]; !ok {
			log.WithField("host", host).Warning("Host skipped because it is not covered by any certificate!")
		}
	}
}
