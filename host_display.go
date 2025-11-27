package main

import (
	"context"
	"fmt"
	"net"
	"os"
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

// updateHostDisplayName performs a reverse DNS lookup and updates the wrapper's hrepr field.
// This is used for periodic/delayed DNS updates instead of blocking at startup.
// Returns true if the name was updated, false otherwise.
func updateHostDisplayName(wrapper PingWrapperInterface) bool {
	if SkipDNS {
		return false
	}

	stats := wrapper.Stats()
	if stats == nil {
		return false
	}

	// Refresh computed fields so we work with up-to-date info
	stats.ComputeState(2_000_000_000)

	// Get IP from stats.iprepr (already resolved during wrapper creation)
	ipStr := stats.iprepr
	if ipStr == "" {
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG DNS: No iprepr for %s\n", wrapper.Host())
		}
		return false
	}

	// Parse IP address
	parsedIP := net.ParseIP(ipStr)
	if parsedIP == nil {
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG DNS: Failed to parse IP %s\n", ipStr)
		}
		return false
	}

	ipAddr := &net.IPAddr{IP: parsedIP}

	// Perform reverse DNS lookup
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	resolver := &net.Resolver{}
	names, err := resolver.LookupAddr(ctx, ipAddr.IP.String())
	if err != nil || len(names) == 0 {
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG DNS: No PTR record for %s (err: %v)\n", ipStr, err)
		}
		return false
	}

	dnsName := strings.TrimSuffix(names[0], ".")

	// For TCP wrappers, we need to preserve the tcp:// prefix and port
	currentRepr := stats.GetHostRepr()
	var newRepr string

	if strings.HasPrefix(currentRepr, "tcp://") {
		// Extract port from current representation
		parts := strings.Split(currentRepr, ":")
		if len(parts) >= 3 {
			port := parts[len(parts)-1]
			newRepr = fmt.Sprintf("tcp://%s:%s", dnsName, port)
		} else {
			return false
		}
	} else {
		// Regular ICMP ping - just use DNS name
		newRepr = dnsName
	}

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG DNS: %s -> %s (current: %s)\n", ipStr, newRepr, currentRepr)
	}

	// Only update if different from current representation
	if newRepr != currentRepr {
		stats.SetHostRepr(newRepr)
		return true
	}

	return false
}
