package main

import (
	"testing"
	"time"
)

func TestComputeState_InitialOfflineThenFirstOnline_NoLossNoHighlight(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	var current int64
	nowFunc = func() time.Time { return time.Unix(0, current) }

	const threshold = int64(2 * time.Second)

	p := &PWStats{}

	t0 := int64(1 * time.Second)
	current = t0
	p.ComputeState(threshold)
	if p.state {
		t.Fatalf("expected initial state offline")
	}
	if !p.skip_next_up_highlight {
		t.Fatalf("expected skip_next_up_highlight=true when starting offline")
	}
	if p.last_loss_nano != 0 || p.last_loss_duration != 0 {
		t.Fatalf("expected no loss recorded on startup, got nano=%d duration=%d", p.last_loss_nano, p.last_loss_duration)
	}

	// First ever reply: should not be highlighted and should not create a bogus "loss".
	t1 := t0 + int64(100*time.Millisecond)
	p.lastrecv = t1
	p.has_ever_received = true
	current = t1
	p.ComputeState(threshold)
	if !p.state {
		t.Fatalf("expected state online after first reply")
	}
	if p.last_up_transition != 0 {
		t.Fatalf("expected no highlight on first acquisition, got last_up_transition=%d", p.last_up_transition)
	}
	if p.last_loss_nano != 0 || p.last_loss_duration != 0 {
		t.Fatalf("expected no loss recorded on first acquisition, got nano=%d duration=%d", p.last_loss_nano, p.last_loss_duration)
	}

	// Go offline (no new replies for > threshold).
	t2 := t1 + int64(3*time.Second)
	current = t2
	p.ComputeState(threshold)
	if p.state {
		t.Fatalf("expected state offline after timeout")
	}
	if p.loss_reference_recv != t1 {
		t.Fatalf("expected loss_reference_recv=%d, got %d", t1, p.loss_reference_recv)
	}

	// Recover: should highlight (skip flag already consumed) and record outage duration based on stored reference.
	t3 := t2 + int64(100*time.Millisecond)
	p.lastrecv = t3
	current = t3
	p.ComputeState(threshold)
	if !p.state {
		t.Fatalf("expected state online after recovery")
	}
	if p.last_up_transition != t3 {
		t.Fatalf("expected highlight at recovery time %d, got %d", t3, p.last_up_transition)
	}
	if p.last_loss_nano != t3 {
		t.Fatalf("expected last_loss_nano=%d, got %d", t3, p.last_loss_nano)
	}
	wantDur := t3 - t1
	if p.last_loss_duration != wantDur {
		t.Fatalf("expected last_loss_duration=%d, got %d", wantDur, p.last_loss_duration)
	}
	if p.loss_reference_recv != 0 {
		t.Fatalf("expected loss_reference_recv cleared after recovery, got %d", p.loss_reference_recv)
	}
}

func TestComputeState_UptimeAccumulatesOnlyWhileOnline(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	var current int64
	nowFunc = func() time.Time { return time.Unix(0, current) }

	const threshold = int64(2 * time.Second)

	t0 := int64(10 * time.Second)
	p := &PWStats{lastrecv: t0, has_ever_received: true}

	current = t0
	p.ComputeState(threshold)
	if !p.state {
		t.Fatalf("expected baseline state online")
	}

	t1 := t0 + int64(500*time.Millisecond)
	current = t1
	p.ComputeState(threshold)

	t2 := t0 + int64(3*time.Second) // > threshold, so offline
	current = t2
	p.ComputeState(threshold)
	if p.state {
		t.Fatalf("expected state offline after timeout")
	}

	got := p.OnlineUptime(t2)
	want := time.Duration(t2 - t0)
	if got != want {
		t.Fatalf("expected uptime %s, got %s", want, got)
	}
}

