package cmd

import (
	"flag"
	log "github.com/sirupsen/logrus"
	"github.com/SOSETH/k8router/pkg/config"
	"github.com/SOSETH/k8router/pkg/haproxy"
	"github.com/SOSETH/k8router/pkg/router"
	"github.com/SOSETH/k8router/pkg/state"
	"os"
	"os/signal"
)

// K8Router main object, just contains command line arguments
type K8router struct {
	configPath string
	verbose    bool
}

// Add command line flags
func (k8r *K8router) setupArgs() {
	flag.StringVar(&k8r.configPath, "config", "config.yml", "path to configuration file")
	flag.BoolVar(&k8r.verbose, "verbose", false, "enable verbose logging")
}

// Run the application
func (k8r *K8router) Run() {
	k8r.setupArgs()
	flag.Parse()

	if k8r.verbose {
		log.SetLevel(log.DebugLevel)
	} else {
		log.SetLevel(log.InfoLevel)
	}

	cfg, err := config.FromFile(k8r.configPath)
	if err != nil {
		log.WithField("config", k8r.configPath).WithError(err).Fatal("Couldn't load config file!")
	}
	log.Debug("Config loaded")

	eventChan := make(chan state.ClusterState)
	for _, clusterCfg := range cfg.Clusters {
		log.WithField("cluster", clusterCfg.Name).Debug("Starting cluster handler")
		cluster := router.ClusterFromConfig(clusterCfg, eventChan)
		cluster.Start()
	}
	log.Debug("All cluster handlers loaded")

	handler, err := haproxy.Init(eventChan, *cfg)
	if err != nil {
		log.WithField("config", k8r.configPath).WithError(err).Fatal("Couldn't init haproxy handler!")
	}
	handler.Start()
	log.Debug("HAProxy handler loaded")

	// Block until exit
	exitSigChan := make(chan os.Signal, 1)
	signal.Notify(exitSigChan, os.Interrupt)
	<-exitSigChan
}
