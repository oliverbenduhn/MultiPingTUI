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
	wrapperFactory   func(host string, options Options, tw *TransitionWriter) PingWrapperInterface
	mu               sync.Mutex
	running          bool
}

// NewPingService creates a new PingService
func NewPingService(repo HostRepository, options Options, tw *TransitionWriter) *PingService {
	ps := &PingService{
		repo:             repo,
		options:          options,
		transitionWriter: tw,
		wrapperFactory:   NewPingWrapper,
	}
	// Initialize DNSUpdater with a source function that gets wrappers from the repo
	ps.dnsUpdater = NewDNSUpdater(repo.GetAll)
	return ps
}

// InitHosts initializes the hosts and stores them in the repository
func (s *PingService) InitHosts(hosts []string) {
	wrappers := make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		wrappers[i] = s.wrapperFactory(host, s.options, s.transitionWriter)
	}
	s.repo.UpdateAll(wrappers)
}

// Start starts all ping wrappers and the DNS updater
func (s *PingService) Start() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.running {
		return
	}
	s.running = true

	wrappers := s.repo.GetAll()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting %d ping wrappers (parallel DNS lookups, staggered start)\n", len(wrappers))
	}

	startWrappers(wrappers)

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: All %d wrappers started successfully\n", len(wrappers))
	}

	s.dnsUpdater.Start()
}

// Stop stops all ping wrappers and the DNS updater
func (s *PingService) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false

	s.dnsUpdater.Stop()
	for _, pw := range s.repo.GetAll() {
		pw.Stop()
	}
}

// ReplaceHosts swaps the host list atomically. The wrapperFactory was injected
// at NewPingService time so this stays decoupled from concrete backend types.
//
// Sequence under s.mu:
//  1. Snapshot running state, flip running=false
//  2. Stop DNS updater (its callbacks reach into repo)
//  3. Build new wrappers via factory — this can do DNS lookups; do it BEFORE
//     stopping the old ones to avoid a window where no wrappers exist
//  4. Stop old wrappers (no longer the source of truth)
//  5. Repo.UpdateAll — atomic swap visible to readers
//  6. If wasRunning, restart everything
//
// If not running, this just swaps the repo contents without spawning goroutines.
func (s *PingService) ReplaceHosts(hosts []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wasRunning := s.running
	if wasRunning {
		s.running = false
	}

	// Stop DNS updates while replacing hosts
	if wasRunning {
		s.dnsUpdater.Stop()
	}

	oldWrappers := s.repo.GetAll()

	newWrappers := make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		newWrappers[i] = s.wrapperFactory(host, s.options, s.transitionWriter)
	}

	// Stop old wrappers
	if wasRunning {
		for _, pw := range oldWrappers {
			pw.Stop()
		}
	}

	// Update repository
	s.repo.UpdateAll(newWrappers)

	// Restart DNS updates for new hosts
	if wasRunning {
		startWrappers(newWrappers)
		s.running = true
		s.dnsUpdater.Start()
	}
}

func startWrappers(wrappers []PingWrapperInterface) {
	// Start wrappers in parallel goroutines to avoid blocking on DNS lookups.
	sem := make(chan struct{}, 20)
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

			sem <- struct{}{}
			defer func() { <-sem }()

			if DebugMode && idx > 0 && idx%50 == 0 {
				fmt.Fprintf(os.Stderr, "DEBUG: Starting wrapper %d/%d\n", idx, len(wrappers))
			}

			pw.Start()
		}(i, pw)

		if i >= 10 && i < len(wrappers)-1 && i%10 == 0 {
			time.Sleep(1 * time.Millisecond)
		}
	}

	wg.Wait()
}
