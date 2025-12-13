package main

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

type PingWrapperInterface interface {
	Start()
	Stop()
	Host() string
	CalcStats(int64) PWStats
	Stats() *PWStats
	SetHostRepr(string)
}

var re_host_w_proto = regexp.MustCompile(`^(tcp|ip)([46])?://(\[?.+?\]?)(?::(\d+))?$`)

func NewPingWrapper(host string, options Options, transition_writer *TransitionWriter) PingWrapperInterface {

	host_findings := re_host_w_proto.FindAllStringSubmatch(host, -1)

	var found_proto, found_ip_family, found_host, found_port string
	var found_port_int int

	if len(host_findings) > 0 {
		found_proto = host_findings[0][1]
		found_ip_family = host_findings[0][2]
		found_host = host_findings[0][3]
		found_port = host_findings[0][4]
	} else {
		found_host = host
	}

	if found_proto == "tcp" {

		if found_port == "" {
			return NewErrorWrapper(host, "tcp probing requested but no port given", transition_writer)
		}
		port, err := strconv.Atoi(found_port)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}
		if port <= 0 || port > 65535 {
			return NewErrorWrapper(host, "tcp probing port invalid", transition_writer)
		}
		found_port_int = port

		ip, err := resolveIPAddr(found_host, found_ip_family)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}

		return &TCPPingWrapper{
			host:  found_host,
			ip:    ip,
			port:  found_port_int,
			stats: &PWStats{transition_writer: transition_writer},
		}
	} else if *options.system {
		ip, err := resolveIPAddr(found_host, found_ip_family)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}
		return &SystemPingWrapper{
			host:         host,
			ip:           ip,
			stats:        &PWStats{transition_writer: transition_writer},
			ping_options: *options.system_ping_options,
		}
	} else {
		ip, err := resolveIPAddr(found_host, found_ip_family)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}
		return &ProbingWrapper{
			host:       host,
			ip:         ip,
			privileged: *options.privileged,
			size:       *options.size,
			stats:      &PWStats{transition_writer: transition_writer},
		}
	}
}

func resolveIPAddr(host string, ip_family string) (*net.IPAddr, error) {
	host = strings.Trim(host, "[]")
	ipaddr, err := net.ResolveIPAddr("ip"+ip_family, host)
	if err != nil {
		return nil, err
	}
	return ipaddr, nil
}
