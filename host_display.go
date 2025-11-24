package main

import (
	"net"
	"strings"
)

// hostDisplayName returns either the original host or the reverse DNS name when the input was an IP.
func hostDisplayName(original string, ip *net.IPAddr) string {
	if ip == nil {
		return original
	}

	trimmed := strings.Trim(original, "[]")
	if net.ParseIP(trimmed) == nil {
		return original
	}

	names, err := net.LookupAddr(ip.IP.String())
	if err != nil || len(names) == 0 {
		return original
	}

	return strings.TrimSuffix(names[0], ".")
}
