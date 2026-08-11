package main

import (
	"net"
	"regexp"
	"strconv"
	"strings"
)

// PingWrapperInterface is the contract every probe implementation must
// satisfy. See AGENTS.md §2.1 and docs/ARCHITECTURE.md §2 for the
// concurrency contract:
//   - Start() spawns the probe goroutine(s); must be called exactly once.
//   - Stop() is IDEMPOTENT — implement with sync.Once. Safe to call
//     before Start.
//   - CalcStats(timeout) holds the wrapper mutex, calls PWStats.ComputeState,
//     and returns a VALUE copy of the stats.
//   - Stats() holds the read mutex and returns a heap-allocated VALUE copy
//     of the stats as *PWStats (the interface requires a pointer; the
//     underlying data is fresh).
//   - SetHostRepr() is the only mutation path exposed to callers (used
//     by DNSUpdater). Holds the write mutex.
//
// All wrappers also embed a *PWStats; the factory in this file
// constructs the right wrapper based on the host-string scheme.
type PingWrapperInterface interface {
	Start()
	Stop()
	Host() string
	CalcStats(int64) PWStats
	Stats() *PWStats
	SetHostRepr(string)
}

// re_host_w_proto parses host strings into (scheme, family, host, port).
// Captured groups:
//   1: "tcp" or "ip" (the scheme)
//   2: "" | "4" | "6" (address family hint; "" = default)
//   3: hostname or IP, possibly wrapped in [] for IPv6
//   4: "" or numeric port (only meaningful for tcp://)
var re_host_w_proto = regexp.MustCompile(`^(tcp|ip)([46])?://(\[?.+?\]?)(?::(\d+))?$`)

// NewPingWrapper is the probe factory. It parses the host string, chooses
// the wrapper implementation, resolves the address, and constructs the
// stats object. If anything fails (bad scheme, DNS error, invalid port),
// it returns an ErrorWrapper so the rest of the program can start and
// the failure shows up per-host in the UI.
//
// The factory is also responsible for enabling adaptive intervals on the
// stats when options.adaptiveInterval is set (or when the host list
// contained a CIDR — that decision is made in main.go).
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

		stats := &PWStats{transition_writer: transition_writer}
		if options.adaptiveInterval != nil && *options.adaptiveInterval {
			stats.EnableAdaptiveInterval()
		}
		return &TCPPingWrapper{
			host:  found_host,
			ip:    ip,
			port:  found_port_int,
			stats: stats,
		}
	} else if *options.system {
		ip, err := resolveIPAddr(found_host, found_ip_family)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}
		stats := &PWStats{transition_writer: transition_writer}
		if options.adaptiveInterval != nil && *options.adaptiveInterval {
			stats.EnableAdaptiveInterval()
		}
		return &SystemPingWrapper{
			host:         host,
			ip:           ip,
			stats:        stats,
			ping_options: *options.system_ping_options,
		}
	} else {
		ip, err := resolveIPAddr(found_host, found_ip_family)
		if err != nil {
			return NewErrorWrapper(host, err.Error(), transition_writer)
		}
		stats := &PWStats{transition_writer: transition_writer}
		if options.adaptiveInterval != nil && *options.adaptiveInterval {
			stats.EnableAdaptiveInterval()
		}
		return &ProbingWrapper{
			host:       host,
			ip:         ip,
			privileged: *options.privileged,
			size:       *options.size,
			stats:      stats,
		}
	}
}

// resolveIPAddr resolves a hostname (or literal IP) into a *net.IPAddr,
// honouring the address-family hint (empty / "4" / "6"). Brackets around
// IPv6 literals are stripped before resolution.
func resolveIPAddr(host string, ip_family string) (*net.IPAddr, error) {
	host = strings.Trim(host, "[]")
	ipaddr, err := net.ResolveIPAddr("ip"+ip_family, host)
	if err != nil {
		return nil, err
	}
	return ipaddr, nil
}
