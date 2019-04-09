package router

import (
	"github.com/pkg/errors"
	"github.com/soseth/k8router/pkg/config"
	"github.com/soseth/k8router/pkg/haproxy"
)

type Router struct {
	haproxyHandler *haproxy.Handler
	cfg            *config.Config
}

func Init(cfgPath string) (*Router, error) {
	obj := Router{}
	cfg, err := config.FromFile(cfgPath)
	if err != nil {
		return nil, errors.Wrap(err, "config parse failed")
	}
	obj.cfg = cfg
	return &obj, nil
}
