/*
Package loadbalancer implements support for exposing Kubernetes Services of
LoadBalancer type. IPVS is used to create frontends on service ports and forward incoming
traffic to service IPs.
*/
package loadbalancer

import (
	"fmt"
	"net"
	"syscall"

	"github.com/coreos/go-iptables/iptables"
	"github.com/moby/ipvs"
	log "github.com/sirupsen/logrus"
	"github.com/vsk8s/k8router/pkg/state"
	v1 "k8s.io/api/core/v1"
)

// LoadBalancer balances load
type LoadBalancer struct {
	loadBalancerChannel chan state.LoadBalancerChange
	ips                 []*net.IP
	h                   *ipvs.Handle
	stopChannel         chan bool
}

// Initialize a LoadBalancer
func Initialize(ips []*net.IP, channel chan state.LoadBalancerChange) (*LoadBalancer, error) {
	handle, err := ipvs.New("")
	if err != nil {
		return nil, err
	}
	lb := &LoadBalancer{
		loadBalancerChannel: channel,
		ips:                 ips,
		h:                   handle,
		stopChannel:         make(chan bool),
	}
	return lb, nil
}

// Start a LoadBalancer
func (lb *LoadBalancer) Start() {
	go lb.eventLoop()
}

// Stop a LoadBalancer
func (lb *LoadBalancer) Stop() {
	lb.stopChannel <- true
}

func (lb *LoadBalancer) eventLoop() {
	for {
		select {
		case event := <-lb.loadBalancerChannel:
			if event.Created {
				lb.createRule(event.Service)
			} else {
				lb.deleteRule(event.Service)
			}
		case _ = <-lb.stopChannel:
			break
		}
	}
}

func (lb *LoadBalancer) createRule(service state.LoadBalancer) {
	log.WithField("service", service.Name).Info("Adding IPVS")

	// Create an IPVS destination ("real server") to be matched with the service above.
	dest := &ipvs.Destination{
		Address:       *service.IP,
		Port:          uint16(service.Port),
		AddressFamily: syscall.AF_INET,
		Weight:        1,
	}

	ipt, err := iptables.New()
	if err != nil {
		log.WithField("service", service.Name).WithError(err).Error("could not initialize iptables")
	}

	iptProto := ""
	if service.Protocol == v1.ProtocolTCP {
		iptProto = "tcp"
	} else {
		iptProto = "udp"
	}
	ipt.AppendUnique("filter", "INPUT", "-p", iptProto, "--dport", fmt.Sprintf("%d", service.Port), "-j", "ACCEPT")

	// FIXME: this results in IPv6 addresses to be paired with IPv4 service IPs.
	// split the config into v4 and v6
	for _, ip := range lb.ips {

		// Create an IPVS service ("virtual server").
		svc := &ipvs.Service{
			Address:       *ip,
			Protocol:      getProtocol(service.Protocol),
			Port:          uint16(service.Port),
			SchedName:     ipvs.RoundRobin,
			AddressFamily: syscall.AF_INET,
			Flags:         ipvs.ConnFwdMasq,
		}

		// Add the virtual server
		err := lb.h.NewService(svc)
		if err != nil {
			log.WithField("service", service.Name).WithError(err).Error("could not add IPVS virtual server")
		}

		// Add the real server
		err = lb.h.NewDestination(svc, dest)
		if err != nil {
			log.WithField("service", service.Name).WithError(err).Error("could not add IPVS real server")
		}

		log.WithField("service", service.Name).Info("added IPVS")
	}
}

func (lb *LoadBalancer) deleteRule(service state.LoadBalancer) {
	log.WithField("service", service.Name).Info("Deleting IPVS")

	ipt, err := iptables.New()
	if err != nil {
		log.WithField("service", service.Name).WithError(err).Error("could not initialize iptables")
	}

	iptProto := ""
	if service.Protocol == v1.ProtocolTCP {
		iptProto = "tcp"
	} else {
		iptProto = "udp"
	}
	ipt.DeleteIfExists("filter", "INPUT", "-p", iptProto, "--dport", fmt.Sprintf("%d", service.Port), "-j", "ACCEPT")
	for _, ip := range lb.ips {

		svc := &ipvs.Service{
			Address:       *ip,
			Protocol:      getProtocol(service.Protocol),
			Port:          uint16(service.Port),
			SchedName:     ipvs.RoundRobin,
			AddressFamily: syscall.AF_INET,
			Flags:         ipvs.ConnFwdMasq,
		}

		err := lb.h.DelService(svc)
		if err != nil {
			log.WithField("service", service.Name).WithError(err).Error("could not delete rule")
		}
	}
}

func getProtocol(protocol v1.Protocol) uint16 {
	if protocol == v1.ProtocolTCP {
		return syscall.IPPROTO_TCP
	} else if protocol == v1.ProtocolUDP {
		return syscall.IPPROTO_UDP
	} else {
		return syscall.IPPROTO_SCTP
	}
}
