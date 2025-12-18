package main

import (
	"fmt"
	"net"
	"os"
	"strings"
	"sync"
	"time"
)

type dnsCacheEntry struct {
	name      string
	expiresAt time.Time
}

// DNSUpdater handles periodic DNS lookups for online hosts
type DNSUpdater struct {
	wrappersSource func() []PingWrapperInterface
	stopChan       chan struct{}
	running        bool
	mu             sync.Mutex
	dnsCache       map[string]dnsCacheEntry
	cacheMu        sync.RWMutex
}

// NewDNSUpdater creates a new DNSUpdater
func NewDNSUpdater(wrappersSource func() []PingWrapperInterface) *DNSUpdater {
	return &DNSUpdater{
		wrappersSource: wrappersSource,
		dnsCache:       make(map[string]dnsCacheEntry),
	}
}

// Start starts the periodic DNS update goroutine
func (d *DNSUpdater) Start() {
	d.mu.Lock()
	if d.running {
		d.mu.Unlock()
		return
	}
	d.stopChan = make(chan struct{})
	d.running = true
	d.mu.Unlock()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting periodic DNS update goroutine (initial: 3s, interval: 60s)\n")
	}

	go func() {
		// Initial delay of 3 seconds (gives time for hosts to become online)
		initialTimer := time.NewTimer(3 * time.Second)
		defer initialTimer.Stop()

		select {
		case <-initialTimer.C:
			d.performDNSUpdates()
		case <-d.stopChan:
			return
		}

		// Periodic updates every 60 seconds
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				d.performDNSUpdates()
			case <-d.stopChan:
				return
			}
		}
	}()
}

// Stop stops the DNS update goroutine
func (d *DNSUpdater) Stop() {
	d.mu.Lock()
	defer d.mu.Unlock()

	if !d.running {
		return
	}

	close(d.stopChan)
	d.running = false

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Stopped periodic DNS update goroutine\n")
	}
}

// performDNSUpdates updates DNS names for all online hosts
func (d *DNSUpdater) performDNSUpdates() {
	if SkipDNS {
		return
	}

	wrappers := d.wrappersSource()
	updated := 0

	// Use semaphore to limit concurrent DNS lookups
	sem := make(chan struct{}, 20)
	var wg sync.WaitGroup

	for _, wrapper := range wrappers {
		stats := wrapper.CalcStats(TimeoutThresholdNS)

		// Only update DNS for online hosts
		if !stats.state || stats.error_message != "" {
			continue
		}

		// Check cache
		d.cacheMu.RLock()
		entry, found := d.dnsCache[stats.iprepr]
		d.cacheMu.RUnlock()

		if found && time.Now().Before(entry.expiresAt) {
			// Cache hit: after a host list reload, wrappers may fall back to showing the raw IP again.
			// If the current display name still looks like an IP, restore the cached reverse-DNS name.
			if entry.name != "" {
				current := stats.GetHostRepr()
				if current == "" || looksLikeIPRepr(current) {
					if current != entry.name {
						wrapper.SetHostRepr(entry.name)
					}
					continue
				}
			}
			// If the wrapper already has a non-IP name, keep it and skip the lookup.
			if stats.GetHostRepr() != "" {
				continue
			}
			// Negative cache hit and still no name: skip lookup.
			if entry.name == "" {
				continue
			}
		}

		wg.Add(1)
		go func(pw PingWrapperInterface) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// We need to peek at the IP again
			s := pw.Stats()
			ip := s.iprepr

			// Double check cache inside goroutine
			d.cacheMu.RLock()
			e, f := d.dnsCache[ip]
			d.cacheMu.RUnlock()
			if f && time.Now().Before(e.expiresAt) && s.GetHostRepr() != "" {
				return
			}

			if updateHostDisplayName(pw) {
				d.mu.Lock()
				updated++
				d.mu.Unlock()

				// Update cache
				newStats := pw.Stats()
				d.cacheMu.Lock()
				d.dnsCache[ip] = dnsCacheEntry{
					name:      newStats.GetHostRepr(),
					expiresAt: time.Now().Add(1 * time.Hour), // 1 hour TTL
				}
				d.cacheMu.Unlock()
			} else {
				// Cache negative result for a shorter time
				d.cacheMu.Lock()
				d.dnsCache[ip] = dnsCacheEntry{
					name:      "",
					expiresAt: time.Now().Add(5 * time.Minute), // 5 min TTL for failures
				}
				d.cacheMu.Unlock()
			}
		}(wrapper)
	}

	wg.Wait()

	if DebugMode && updated > 0 {
		fmt.Fprintf(os.Stderr, "DEBUG: Updated DNS names for %d online hosts\n", updated)
	}
}

func looksLikeIPRepr(repr string) bool {
	r := repr
	// Allow tcp://host:port
	r = strings.TrimPrefix(r, "tcp://")
	// Strip brackets
	r = strings.Trim(r, "[]")
	// Strip port if present
	if host, _, err := net.SplitHostPort(r); err == nil {
		r = strings.Trim(host, "[]")
	}
	return net.ParseIP(r) != nil
}
