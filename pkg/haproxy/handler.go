package haproxy

import (
	log "github.com/sirupsen/logrus"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/state"
	"os"
	"os/exec"
	"text/template"
)

type Handler struct {
	template *template.Template
	config   config.Config
	updates  chan state.ClusterState
}

func Init(updates chan state.ClusterState, config config.Config) *Handler {
	return &Handler{
		updates: updates,
	}
}

func (h *Handler) UpdateConfig(backendIPs map[string][]string, ingresses map[string]map[string][]string) {
	log.Debug("Writing config")

	// TODO: Respect file mode setting
	config, err := os.OpenFile(h.config.HAProxyDropinPath, os.O_TRUNC|os.O_CREATE, 0644)
	if err != nil {
		log.WithError(err).Fatal("Couldn't open haproxy config for writing")
	}

	err = h.template.Execute(config, struct {
		BackendIPs map[string][]string
		Ingresses  map[string]map[string][]string
	}{
		BackendIPs: backendIPs,
		Ingresses:  ingresses,
	})
	if err != nil {
		log.WithError(err).Fatal("Couldn't template haproxy config")
	}

	// TODO: Replace with systemd API
	err = exec.Command("sudo", "/bin/systemctl", "restart", "haproxy.service").Run()
	if err != nil {
		log.WithError(err).Fatal("Couldn't reload haproxy")
	}
}
