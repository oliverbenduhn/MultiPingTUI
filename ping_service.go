package main

import (
	"fmt"
	"os"
	"sync"
	"time"
)

// PingService manages the lifecycle of ping wrappers
type PingService struct {
	repo             HostRepository
	options          Options
	transitionWriter *TransitionWriter
	dnsUpdater       *DNSUpdater
}

// NewPingService creates a new PingService
func NewPingService(repo HostRepository, options Options, tw *TransitionWriter) *PingService {
	ps := &PingService{
		repo:             repo,
		options:          options,
		transitionWriter: tw,
	}
	// Initialize DNSUpdater with a source function that gets wrappers from the repo
	ps.dnsUpdater = NewDNSUpdater(repo.GetAll)
	return ps
}

// InitHosts initializes the hosts and stores them in the repository
func (s *PingService) InitHosts(hosts []string) {
	wrappers := make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		wrappers[i] = NewPingWrapper(host, s.options, s.transitionWriter)
	}
	s.repo.UpdateAll(wrappers)
}

// Start starts all ping wrappers and the DNS updater
func (s *PingService) Start() {
	wrappers := s.repo.GetAll()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting %d ping wrappers (parallel DNS lookups, staggered start)\n", len(wrappers))
	}

	// Start wrappers in parallel goroutines to avoid blocking on DNS lookups
	sem := make(chan struct{}, 20) // Allow 20 concurrent starts
	var wg sync.WaitGroup

	for i, pw := range wrappers {
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
		}(i, pw)

		// Small delay to avoid overwhelming the system at startup
		if i >= 10 && i < len(wrappers)-1 && i%10 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	wg.Wait()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: All %d wrappers started successfully\n", len(wrappers))
	}

	s.dnsUpdater.Start()
}

// Stop stops all ping wrappers and the DNS updater
func (s *PingService) Stop() {
	s.dnsUpdater.Stop()
	for _, pw := range s.repo.GetAll() {
		pw.Stop()
	}
}

// ReplaceHosts replaces the current hosts with new ones, handling graceful shutdown/startup
func (s *PingService) ReplaceHosts(hosts []string) {
	// Stop DNS updates while replacing hosts
	s.dnsUpdater.Stop()

	oldWrappers := s.repo.GetAll()
	
	newWrappers := make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		newWrappers[i] = NewPingWrapper(host, s.options, s.transitionWriter)
	}
	
	// Update repository
	s.repo.UpdateAll(newWrappers)

	// Stop old wrappers
	for _, pw := range oldWrappers {
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
	s.dnsUpdater.Start()
}
