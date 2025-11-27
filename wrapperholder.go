package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

type WrapperHolder struct {
	ping_wrappers     []PingWrapperInterface
	options           Options
	transition_writer *TransitionWriter
	mu                sync.RWMutex
	dnsUpdateStop     chan struct{} // Signal to stop DNS update goroutine
	dnsUpdateRunning  bool
}

func (w *WrapperHolder) InitHosts(hosts []string, options Options, transition_writer *TransitionWriter) {
	w.options = options
	w.transition_writer = transition_writer
	w.setHosts(hosts)
}

func (w *WrapperHolder) setHosts(hosts []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ping_wrappers = make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		w.ping_wrappers[i] = NewPingWrapper(host, w.options, w.transition_writer)
	}
}

func (w *WrapperHolder) ReplaceHosts(hosts []string) {
	// Stop DNS updates while replacing hosts
	w.StopPeriodicDNSUpdates()

	w.mu.Lock()
	old := w.ping_wrappers
	w.ping_wrappers = make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		w.ping_wrappers[i] = NewPingWrapper(host, w.options, w.transition_writer)
	}
	newWrappers := w.ping_wrappers
	w.mu.Unlock()

	for _, pw := range old {
		pw.Stop()
	}

	// Staggered start for new wrappers
	for i, pw := range newWrappers {
		pw.Start()
		if i >= 10 && i < len(newWrappers)-1 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	// Restart DNS updates for new hosts
	w.StartPeriodicDNSUpdates()
}

func (w *WrapperHolder) Wrappers() []PingWrapperInterface {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]PingWrapperInterface, len(w.ping_wrappers))
	copy(out, w.ping_wrappers)
	return out
}

func (w *WrapperHolder) CalcStats(timeout_threshold int64) {
	for _, wrapper := range w.Wrappers() {
		wrapper.CalcStats(timeout_threshold)
	}
}

func (w *WrapperHolder) Start() {
	wrappers := w.Wrappers()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting %d ping wrappers (parallel DNS lookups, staggered start)\n", len(wrappers))
	}

	// Start wrappers in parallel goroutines to avoid blocking on DNS lookups
	// Use a semaphore to limit concurrency for ARP/ICMP storm prevention
	sem := make(chan struct{}, 20) // Allow 20 concurrent starts
	var wg sync.WaitGroup

	for i, ping_wrapper := range wrappers {
		wg.Add(1)
		go func(idx int, pw PingWrapperInterface) {
			defer wg.Done()
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(os.Stderr, "PANIC starting wrapper %d (%s): %v\n", idx, pw.Host(), r)
				}
			}()

			sem <- struct{}{}        // Acquire
			defer func() { <-sem }() // Release

			if DebugMode && idx > 0 && idx%50 == 0 {
				fmt.Fprintf(os.Stderr, "DEBUG: Starting wrapper %d/%d\n", idx, len(wrappers))
			}

			pw.Start()
		}(i, ping_wrapper)

		// Small delay to avoid overwhelming the system at startup
		if i >= 10 && i < len(wrappers)-1 && i%10 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	wg.Wait()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: All %d wrappers started successfully\n", len(wrappers))
	}
}

func (w *WrapperHolder) Stop() {
	// Stop DNS update goroutine first
	w.StopPeriodicDNSUpdates()

	for _, ping_wrapper := range w.Wrappers() {
		ping_wrapper.Stop()
	}
}

// StartPeriodicDNSUpdates starts a goroutine that performs DNS lookups for online hosts.
// Initial lookup happens after 3 seconds, then every 60 seconds.
// Only updates DNS names for hosts that are currently online.
func (w *WrapperHolder) StartPeriodicDNSUpdates() {
	w.mu.Lock()
	if w.dnsUpdateRunning {
		w.mu.Unlock()
		return
	}
	w.dnsUpdateStop = make(chan struct{})
	w.dnsUpdateRunning = true
	w.mu.Unlock()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting periodic DNS update goroutine (initial: 3s, interval: 60s)\n")
	}

	go func() {
		// Initial delay of 3 seconds (gives time for hosts to become online)
		initialTimer := time.NewTimer(3 * time.Second)
		defer initialTimer.Stop()

		select {
		case <-initialTimer.C:
			w.performDNSUpdates()
		case <-w.dnsUpdateStop:
			return
		}

		// Periodic updates every 60 seconds
		ticker := time.NewTicker(60 * time.Second)
		defer ticker.Stop()

		for {
			select {
			case <-ticker.C:
				w.performDNSUpdates()
			case <-w.dnsUpdateStop:
				return
			}
		}
	}()
}

// StopPeriodicDNSUpdates stops the DNS update goroutine
func (w *WrapperHolder) StopPeriodicDNSUpdates() {
	w.mu.Lock()
	defer w.mu.Unlock()

	if !w.dnsUpdateRunning {
		return
	}

	close(w.dnsUpdateStop)
	w.dnsUpdateRunning = false

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Stopped periodic DNS update goroutine\n")
	}
}

// performDNSUpdates updates DNS names for all online hosts
func (w *WrapperHolder) performDNSUpdates() {
	if SkipDNS {
		return
	}

	wrappers := w.Wrappers()
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
				updated++
			}
		}(wrapper)
	}

	wg.Wait()

	if DebugMode && updated > 0 {
		fmt.Fprintf(os.Stderr, "DEBUG: Updated DNS names for %d online hosts\n", updated)
	}
}
