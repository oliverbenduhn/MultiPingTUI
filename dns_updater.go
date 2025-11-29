package main

import (
	"fmt"
	"os"
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
		stats := wrapper.CalcStats(2_000_000_000) // 2s threshold

		// Only update DNS for online hosts
		if !stats.state || stats.error_message != "" {
			continue
		}

		// Check cache
		d.cacheMu.RLock()
		entry, found := d.dnsCache[stats.iprepr]
		d.cacheMu.RUnlock()

		if found && time.Now().Before(entry.expiresAt) {
			// Cache hit, update wrapper if needed (though wrapper usually holds the state)
			// Ideally we would set the cached name on the wrapper here if it was lost,
			// but updateHostDisplayName does the lookup AND set.
			// We should modify updateHostDisplayName or do the check here.
			// For now, let's assume if it's in cache, the wrapper likely has it,
			// OR we can skip the lookup.
			// Actually, the wrapper stores the "hrepr".
			// If we skip calling updateHostDisplayName, we rely on the wrapper keeping it.
			// But if the wrapper doesn't have it yet (e.g. first run), we need to set it.
			// Let's modify the logic to use the cache.
			if stats.GetHostRepr() != "" {
				continue // Already has a name and cache is valid
			}
			// If wrapper has no name but we have it in cache, set it
			if entry.name != "" {
				wrapper.SetHostRepr(entry.name)
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
