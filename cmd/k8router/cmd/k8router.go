package cmd

import (
	"flag"
	log "github.com/sirupsen/logrus"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/haproxy"
	"github.com/soseth/k8router/pkg/router"
	"github.com/soseth/k8router/pkg/state"
)

type K8router struct {
	configPath string
	verbose    bool
}

func (k8r *K8router) setupArgs() {
	flag.StringVar(&k8r.configPath, "config", "config.yml", "path to configuration file")
	flag.BoolVar(&k8r.verbose, "verbose", false, "enable verbose logging")
}

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
	eventChan := make(chan state.ClusterState)
	for _, clusterCfg := range cfg.Clusters {
		cluster := router.ClusterFromConfig(clusterCfg, eventChan)
		cluster.Start()
	}

	handler, err := haproxy.Init(eventChan, *cfg)
	if err != nil {
		log.WithField("config", k8r.configPath).WithError(err).Fatal("Couldn't init haproxy handler!")
	}
	handler.Start()
}
