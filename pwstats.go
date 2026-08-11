package main

import (
	"encoding/json"
	"strings"
	"sync"
	"time"
)

type TransitionEvent struct {
	Host     string
	IP       string
	State    bool // true=up, false=down
	When     time.Time
	Duration time.Duration
}

type GlobalStatistics struct {
	mu                sync.RWMutex
	StartTime         time.Time
	RecentTransitions []TransitionEvent
}

func NewGlobalStatistics() *GlobalStatistics {
	return &GlobalStatistics{
		StartTime:         time.Now(),
		RecentTransitions: make([]TransitionEvent, 0, 50),
	}
}

func (s *GlobalStatistics) AddTransition(event TransitionEvent) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// Prepend in-place to avoid allocating a new slice on every transition.
	// Grow up to capacity 50, then shift existing entries right and insert at head.
	if len(s.RecentTransitions) < cap(s.RecentTransitions) {
		s.RecentTransitions = s.RecentTransitions[:len(s.RecentTransitions)+1]
	}
	copy(s.RecentTransitions[1:], s.RecentTransitions)
	s.RecentTransitions[0] = event
}

func (s *GlobalStatistics) GetTransitions(limit int) []TransitionEvent {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if limit <= 0 || limit > len(s.RecentTransitions) {
		limit = len(s.RecentTransitions)
	}
	// Copy to avoid races
	out := make([]TransitionEvent, limit)
	copy(out, s.RecentTransitions[:limit])
	return out
}

func (s *GlobalStatistics) GetStartTime() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.StartTime
}

// PWStats is the per-host statistics and state machine.
//
// Lifecycle fields (startup_time, last_compute) are initialized on the
// first call to ComputeState. State-machine fields (skip_next_up_highlight,
// loss_reference_recv, last_up_transition, has_ever_been_online,
// last_loss_nano, last_loss_duration) are tightly coupled — they must only
// be mutated by ComputeState, never by callers. See ComputeState's
// statechart in docs/ARCHITECTURE.md §3.
//
// Read-only fields populated by the wrapper: iprepr, hrepr, error_message,
// adaptive_interval, lastsent, lastrecv, lastrtt, lastrtt_as_string,
// has_ever_received.
//
// PWStats is small (~120 bytes) and is intentionally returned by VALUE
// from CalcStats and Stats to avoid race conditions across goroutines.
// Do not embed another struct's mutable state into PWStats.
type PWStats struct {
	lastsent               int64
	lastrecv               int64
	lastrtt                time.Duration
	lastrtt_as_string      string
	last_loss_nano         int64
	last_loss_duration     int64
	last_seen_nano         int64
	state                  bool
	has_ever_received      bool
	state_initialized      bool
	skip_next_up_highlight bool
	has_ever_been_online   bool
	loss_reference_recv    int64
	last_up_transition     int64
	startup_time           int64
	last_compute           int64
	uptime_nano            int64
	transition_writer      *TransitionWriter
	error_message          string
	hrepr                  string
	iprepr                 string
	adaptive_interval      bool
}

var nowFunc = time.Now

// GetHostRepr returns the host representation (display name)
func (p *PWStats) GetHostRepr() string {
	return p.hrepr
}

// SetHostRepr sets the host representation (display name)
func (p *PWStats) SetHostRepr(hrepr string) {
	p.hrepr = hrepr
}

// ComputeState is the single point of truth for a host's up/down state.
// It is called by every wrapper's CalcStats and must be the only function
// that mutates the state-machine fields (see PWStats doc comment).
//
// Inputs:
//   - timeout_threshold: in nanoseconds. The host is "online" iff its
//     most recent successful receive was within this many nanoseconds.
//
// Behavior summary (full statechart in docs/ARCHITECTURE.md §3):
//   - First call initializes startup_time and last_compute without
//     emitting a transition; skip_next_up_highlight is set true iff the
//     host starts offline, so the very first "down to up" is silent.
//   - On up→down: capture loss_reference_recv = lastrecv so we can
//     measure the outage duration on recovery.
//   - On down→up (when skip_next_up_highlight is false): record
//     last_loss_nano = now, last_loss_duration = now - loss_reference_recv,
//     and set last_up_transition = now so the UI can highlight.
//   - On every state change: emit one NDJSON line to transition_writer.
//
// Uptime is accumulated only while state was true since the previous
// compute; OnlineUptime adds the live interval since last_compute when
// state is currently true.
func (p *PWStats) ComputeState(timeout_threshold int64) {
	now := nowFunc().UnixNano()
	if p.startup_time == 0 {
		p.startup_time = now
	}
	if p.last_compute == 0 {
		p.last_compute = now
	}

	prevState := p.state
	prevSeen := p.state_initialized
	prevEverOnline := p.has_ever_been_online

	if p.lastrecv > 0 {
		delta := now - p.lastrecv
		if delta < 0 {
			delta = 0
		}
		p.last_seen_nano = delta
	} else {
		delta := now - p.startup_time
		if delta < 0 {
			delta = 0
		}
		p.last_seen_nano = delta
	}
	new_state := p.lastrecv > 0 && p.last_seen_nano < timeout_threshold

	if !prevSeen {
		// First observation initializes baseline without marking transitions or highlights
		p.state_initialized = true
		// Only skip highlighting when we start offline (first "down→up" is just initial acquisition).
		p.skip_next_up_highlight = !new_state
		p.has_ever_been_online = new_state
		p.state = new_state
		p.last_compute = now
		return
	}

	// accumulate uptime only while state was online since last compute
	if prevState {
		elapsed := now - p.last_compute
		if elapsed > 0 {
			p.uptime_nano += elapsed
		}
	}

	if prevState && !new_state {
		// Host went down (up→down transition). Keep the last successful receive time so we can
		// compute the outage duration even though lastrecv will be overwritten on recovery.
		if p.lastrecv > 0 {
			p.loss_reference_recv = p.lastrecv
		}
	}

	if !prevState && new_state {
		// Host came back online (down→up transition)
		if p.skip_next_up_highlight {
			// This is the first transition after startup - don't highlight it
			p.skip_next_up_highlight = false
		} else {
			// Normal transition - highlight it blue for 20 seconds
			p.last_up_transition = now
		}
		// Record loss event only if we have been online before and we have a valid reference receive.
		if prevEverOnline && p.loss_reference_recv > 0 && now > p.loss_reference_recv {
			p.last_loss_nano = now
			p.last_loss_duration = now - p.loss_reference_recv
		}
		p.loss_reference_recv = 0
	}
	if p.state != new_state {
		var sb strings.Builder

		var transition string
		if new_state {
			transition = "down to up"
		} else {
			transition = "up to down"
		}

		jsonString, _ := json.Marshal(
			struct {
				Timestamp  string
				UnixNano   int64
				Host       string
				Ip         string
				Transition string
				State      bool
			}{
				time.Unix(0, now).String(),
				now,
				p.GetHostRepr(),
				p.iprepr,
				transition,
				new_state,
			},
		)
		sb.Write(jsonString)
		sb.WriteString("\n")
		if p.transition_writer != nil {
			p.transition_writer.WriteString(sb.String())
		}
	}

	p.state = new_state
	if new_state {
		p.has_ever_been_online = true
	}
	p.last_compute = now
}

func (p PWStats) OnlineUptime(now int64) time.Duration {
	total := p.uptime_nano
	if p.state {
		total += now - p.last_compute
	}
	if total < 0 {
		total = 0
	}
	return time.Duration(total)
}

// GetPingInterval returns the appropriate ping interval based on host history.
// Hosts that have never been online are pinged less frequently to save resources.
// GetPingInterval returns the ping interval for the current host.
// When adaptive_interval is enabled, hosts that have never been online
// are pinged every 10 s; as soon as one responds, the wrapper switches
// the underlying pinger's interval to 1 s. Without adaptive mode, the
// interval is a constant 1 s.
//
// main.go raises the global TimeoutThresholdNS to 12 s when adaptive
// mode is active so that a 10 s interval + slow first reply does not
// cause false offline flapping.
func (p *PWStats) GetPingInterval() time.Duration {
	if !p.adaptive_interval {
		return time.Second // default interval when adaptive mode is disabled
	}

	if p.has_ever_been_online {
		return time.Second // normal interval for hosts that have been seen online
	}

	return 10 * time.Second // slower interval for hosts never seen online
}

// EnableAdaptiveInterval enables adaptive ping intervals for this host
func (p *PWStats) EnableAdaptiveInterval() {
	p.adaptive_interval = true
}
