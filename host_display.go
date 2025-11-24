package main

import (
	"context"
	"net"
	"strings"
	"time"
)

// hostDisplayName returns either the original host or the reverse DNS name when the input was an IP.
// Uses a 500ms timeout for DNS lookups to avoid blocking on slow/non-existent PTR records.
// Can be disabled globally with -no-dns flag for faster startup.
func hostDisplayName(original string, ip *net.IPAddr) string {
	if ip == nil {
		return original
	}

	trimmed := strings.Trim(original, "[]")
	if net.ParseIP(trimmed) == nil {
		return original
	}

	// Skip DNS lookup if disabled
	if SkipDNS {
		return original
	}

	// Use a context with timeout to prevent long DNS lookup delays
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resolver := &net.Resolver{}
	names, err := resolver.LookupAddr(ctx, ip.IP.String())
	if err != nil || len(names) == 0 {
		return original
	}

	return strings.TrimSuffix(names[0], ".")
}
