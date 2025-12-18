package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

type ProbingWrapper struct {
	host              string
	ip                *net.IPAddr
	hstring           string
	pinger            *probing.Pinger
	size              int
	stats             *PWStats
	privileged        bool
	mu                sync.RWMutex
	lastIntervalCheck time.Time
}

func (w *ProbingWrapper) Start() {
	var err error
	w.pinger, err = probing.NewPinger(w.ip.String())
	if err != nil {
		w.mu.Lock()
		w.stats.error_message = fmt.Sprintf("pinger init failed: %v", err)
		w.mu.Unlock()
		return
	}

	w.pinger.RecordRtts = false
	w.pinger.OnSend = w.onSend
	// pinger.OnSend = pingwrapper.OnRecv
	w.pinger.OnRecv = w.onRecv
	w.pinger.OnDuplicateRecv = w.onDuplicateRecv
	w.pinger.Size = w.size
	w.pinger.Debug = DebugMode

	// Set initial interval based on adaptive mode
	if w.stats.adaptive_interval {
		w.pinger.Interval = w.stats.GetPingInterval()
		w.lastIntervalCheck = time.Now()
	} else {
		w.pinger.Interval = time.Second
	}
	if runtime.GOOS == "linux" {
		w.pinger.SetDoNotFragment(true)
	}

	if runtime.GOOS == "windows" || os.Getuid() == 0 {
		w.pinger.SetPrivileged(true)
	} else {
		w.pinger.SetPrivileged(w.privileged)
	}

	// Use host as initial display name (DNS lookup happens later via periodic updates)
	displayHost := w.host

	w.hstring = fmt.Sprintf("%s (%s)", displayHost, w.ip.String())

	w.stats.SetHostRepr(displayHost)
	w.stats.iprepr = w.ip.IP.String()

	go func(w *ProbingWrapper) {
		err := w.pinger.Run()
		if err != nil {
			w.mu.Lock()
			w.stats.error_message = fmt.Sprintf("pinger stopped: %v", err)
			w.mu.Unlock()
			return
		}
	}(w)
}

func (w *ProbingWrapper) Stop() {
	w.pinger.Stop()
}

func (w *ProbingWrapper) onSend(pkt *probing.Packet) {
	w.mu.Lock()
	defer w.mu.Unlock()
	now := time.Now()

	// Dynamically adjust interval based on host status
	if w.stats.adaptive_interval {
		desiredInterval := w.stats.GetPingInterval()
		// Check every 5 seconds if we need to update the interval
		if now.Sub(w.lastIntervalCheck) > 5*time.Second {
			if w.pinger.Interval != desiredInterval {
				w.pinger.Interval = desiredInterval
			}
			w.lastIntervalCheck = now
		}
	}

	w.stats.lastsent = now.UnixNano()
}

func (w *ProbingWrapper) onRecv(pkt *probing.Packet) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.has_ever_received = true
	w.stats.lastrecv = time.Now().UnixNano()
	w.stats.lastrtt = pkt.Rtt
	w.stats.lastrtt_as_string = round(w.stats.lastrtt, 2).String()
}

func (w *ProbingWrapper) onDuplicateRecv(pkt *probing.Packet) {
	// p.lastread = fmt.Sprintf("%d bytes from %s: icmp_seq=%d time=%v ttl=%v (DUP!)", pkt.Nbytes, pkt.IPAddr, pkt.Seq, pkt.Rtt, pkt.TTL)
}

func (w *ProbingWrapper) Host() string {
	return w.hstring
}

func (w *ProbingWrapper) CalcStats(timeout_threshold int64) PWStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.ComputeState(timeout_threshold)
	return *w.stats
}

func (w *ProbingWrapper) Stats() *PWStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	// Return a copy to avoid race conditions on the pointer
	// But the interface expects *PWStats.
	// If we return the internal pointer, the caller must not modify it and we can't guarantee read safety if we unlock.
	// Ideally we should return a copy, but the signature is *PWStats.
	// For now, let's return a copy on the heap.
	s := *w.stats
	return &s
}

var divs = []time.Duration{
	time.Duration(1), time.Duration(10), time.Duration(100), time.Duration(1000)}

func (w *ProbingWrapper) SetHostRepr(h string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.SetHostRepr(h)
}

func round(d time.Duration, digits int) time.Duration {
	switch {
	case d > time.Second:
		d = d.Round(time.Second / divs[digits])
	case d > time.Millisecond:
		d = d.Round(time.Millisecond / divs[digits])
	case d > time.Microsecond:
		d = d.Round(time.Microsecond / divs[digits])
	}
	return d
}
