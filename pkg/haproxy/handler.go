package haproxy

import (
	log "github.com/sirupsen/logrus"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
	"net"
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
	return &Handler{
		updates:    updates,
		numChanges: 0,
		template:   parsedTemplate,
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

	cfg := TemplateInfo{
		WildcardCertName: "",
	}

	// Step 1: Which SNIs do we have in our certs (first frontend and it's backends)
	for _, cert := range h.config.Certificates {
		cfg.SniMap[cert.Name] = cert.Domains
		if cert.IsWildcard {
			cfg.WildcardCertName = cert.Name
		}
	}

	// For each Ingress, which backends does it have?
	for _, cluster := range h.clusterState {
		for _, ingress := range cluster.Ingresses {
			for _, host := range ingress.Hosts {
				cfg.IngressInCluster[host] = append(cfg.IngressInCluster[host], cluster.Name)
			}
		}
	}
	backendToHost := map[string][]string{}
	hosts := map[string]bool{}
	for host, clusters := range cfg.IngressInCluster {
		hosts[host] = true
		sort.Strings(clusters)
		key := strings.Join(clusters, "-")
		if _, ok := cfg.BackendClusterCombinations[key] ; !ok {
			// We haven't seen this particular backend combination yet
			var backendIPs []*net.IP
			for _, cluster := range clusters {
				for _, backend := range h.clusterState[cluster].Backends {
					backendIPs = append(backendIPs, backend.IP)
				}
			}
			cfg.BackendClusterCombinations[key] = backendIPs
		}
		backendToHost[key] = append(backendToHost[key], host)
	}

	// Map of domains to certificates
	frontends := map[string]string{}
	for _, cert := range h.config.Certificates {
		for _, name := range cert.Domains {
			if hosts[name] {
				frontends[name] = cert.Name
			}
		}
		if cert.IsWildcard {
			frontends[""] = cert.Name
		}
		cfg.Certs[cert.Name] = cert.Cert
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