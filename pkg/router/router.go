package router

import (
	"github.com/pkg/errors"
	"github.com/soseth/k8router/pkg/haproxy"
)

type Router struct {
	haproxyHandler *haproxy.Handler
	cfg *Config
}

func Init(cfgPath string) (*Router, error) {
	obj := Router{}
	cfg, err := FromFile(cfgPath)
	if err != nil {
		return nil, errors.Wrap(err, "config parse failed")
	}
	obj.cfg = cfg
	return &obj, nil
}