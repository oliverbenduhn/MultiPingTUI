package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// DNSUpdater handles periodic DNS lookups for online hosts
type DNSUpdater struct {
	wrappersSource func() []PingWrapperInterface
	stopChan       chan struct{}
	running        bool
	mu             sync.Mutex
}

// NewDNSUpdater creates a new DNSUpdater
func NewDNSUpdater(wrappersSource func() []PingWrapperInterface) *DNSUpdater {
	return &DNSUpdater{
		wrappersSource: wrappersSource,
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

		wg.Add(1)
		go func(pw PingWrapperInterface) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if updateHostDisplayName(pw) {
				d.mu.Lock()
				updated++
				d.mu.Unlock()
			}
		}(wrapper)
	}

	wg.Wait()

	if DebugMode && updated > 0 {
		fmt.Fprintf(os.Stderr, "DEBUG: Updated DNS names for %d online hosts\n", updated)
	}
}
