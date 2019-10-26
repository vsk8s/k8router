package loadbalancer

import (
	"fmt"
	log "github.com/sirupsen/logrus"
	"github.com/vsk8s/k8router/pkg/state"
	v1 "k8s.io/api/core/v1"
	"net"
	"os/exec"
	"strings"
)

// LoadBalancer balances load
type LoadBalancer struct {
	loadBalancerChannel chan state.LoadBalancerChange

	ips []*net.IP

	stopChannel chan bool
}

// Initialize a LoadBalancer
func Initialize(ips []*net.IP, channel chan state.LoadBalancerChange) *LoadBalancer {
	return &LoadBalancer{loadBalancerChannel: channel,
		ips:         ips,
		stopChannel: make(chan bool)}
}

// Start a LoadBalancer
func (h *LoadBalancer) Start() {
	go h.eventLoop()
}

// Stop a LoadBalancer
func (h *LoadBalancer) Stop() {
	h.stopChannel <- true
}

func (h *LoadBalancer) eventLoop() {
	for {
		select {
		case event := <-h.loadBalancerChannel:
			if event.Created {
				h.createRule(event.Service)
			} else {
				h.deleteRule(event.Service)
			}
		case _ = <-h.stopChannel:
			break
		}
	}
}

func (h *LoadBalancer) createRule(service state.LoadBalancer) {
	log.WithField("service", service.Name).Info("Adding IPVS")
	for _, ip := range h.ips {

		serviceIP := formatServiceIP(ip, service.Port)
		protocol := protocolToFlag(service.Protocol)
		log.Debugf("Running command 'ipvsadm %s %s %s %s %s'",
			"-A",
			protocol,
			serviceIP,
			"-s",
			"rr")
		err := exec.Command("ipvsadm",
			"-A",
			protocol,
			serviceIP,
			"-s",
			"rr",
		).Run()

		if err != nil {
			log.WithError(err).Error("Couldn't add service")
		}
		log.Debugf("Running command 'ipvsadm %s %s %s %s %s %s'",
			"-a",
			protocol,
			serviceIP,
			"-r",
			fmt.Sprintf("%s:%d", service.IP, service.Port),
			"-m")
		err = exec.Command("ipvsadm",
			"-a",
			protocol,
			serviceIP,
			"-r",
			fmt.Sprintf("%s:%d", service.IP, service.Port),
			"-m",
		).Run()
		if err != nil {
			log.WithField("service", service.Name).WithError(err).Error("Couldn't add rule")
		}
	}
}

func formatServiceIP(ip *net.IP, port int32) string {
	var serviceIP string
	if strings.Contains(ip.String(), ":") {
		serviceIP = fmt.Sprintf("[%s]:%d", ip.String(), port)
	} else {
		serviceIP = fmt.Sprintf("%s:%d", ip.String(), port)
	}
	return serviceIP
}

func (h *LoadBalancer) deleteRule(service state.LoadBalancer) {
	log.WithField("service", service.Name).Info("Deleting IPVS")
	for _, ip := range h.ips {

		serviceIP := formatServiceIP(ip, service.Port)
		protocol := protocolToFlag(service.Protocol)

		log.Debugf("Running command 'ipvsadm %s %s %s'",
			"-D",
			protocol,
			serviceIP)
		err := exec.Command("ipvsadm",
			"-D",
			protocol,
			serviceIP,
		).Run()

		if err != nil {
			log.WithField("service", service.Name).WithError(err).Error("Couldn't delete rule")
		}
	}
}

func protocolToFlag(protocol v1.Protocol) string {
	if protocol == v1.ProtocolTCP {
		return "-t"
	} else if protocol == v1.ProtocolUDP {
		return "-u"
	} else {
		return "--sctp-service"
	}
}
