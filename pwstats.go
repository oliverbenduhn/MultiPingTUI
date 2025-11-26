package main

import (
	"encoding/json"
	"strings"
	"time"
)

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
	last_up_transition     int64
	startup_time           int64
	last_compute           int64
	uptime_nano            int64
	transition_writer      *TransitionWriter
	error_message          string
	hrepr                  string
	iprepr                 string
}

func (p *PWStats) ComputeState(timeout_threshold int64) {
	now := time.Now().UnixNano()
	if p.startup_time == 0 {
		p.startup_time = now
	}
	if p.last_compute == 0 {
		p.last_compute = now
	}

	prevState := p.state
	prevSeen := p.state_initialized

	old_last_seen := p.last_seen_nano
	p.last_seen_nano = now - p.lastrecv
	new_state := p.last_seen_nano < timeout_threshold
	// TODO: Algo to review completely

	if !prevSeen {
		// First observation initializes baseline without marking transitions or highlights
		p.state_initialized = true
		p.skip_next_up_highlight = true
		p.state = new_state
		p.last_compute = now
		return
	}

	// accumulate uptime only while state was online since last compute
	if prevState {
		p.uptime_nano += now - p.last_compute
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
		// Always record the loss event (timestamp and duration)
		p.last_loss_nano = now
		p.last_loss_duration = old_last_seen
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
				p.hrepr,
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
