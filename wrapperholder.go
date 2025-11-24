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
	for _, ping_wrapper := range w.Wrappers() {
		ping_wrapper.Stop()
	}
}
