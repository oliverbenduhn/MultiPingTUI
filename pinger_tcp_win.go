//go:build windows

package main

// inspired from https://github.com/cloverstd/tcping/blob/master/ping/tcp/tcp.go

import (
	"context"
	"fmt"
	"net"
	"sync"
	"time"
)

type TCPPingWrapper struct {
	host              string
	ip                *net.IPAddr
	hstring           string
	port              int
	str_tgt           string
	stats             *PWStats
	loopTicker        *time.Ticker
	stopChan          chan struct{}
	stopOnce          sync.Once
	mu                sync.RWMutex
	lastIntervalCheck time.Time
}

func (w *TCPPingWrapper) Start() {
	w.mu.Lock()
	// Use host as initial display name (DNS lookup happens later via periodic updates)
	displayHost := w.host
	w.hstring = fmt.Sprintf("tcp://%v:%v (%v:%v)", displayHost, w.port, w.ip.String(), w.port)
	w.stats.SetHostRepr(fmt.Sprintf("tcp://%v:%v", displayHost, w.port))
	w.stats.iprepr = w.ip.IP.String()

	w.str_tgt = fmt.Sprintf("%v:%v", w.ip.String(), w.port)

	w.stopChan = make(chan struct{})
	w.stopOnce = sync.Once{}

	// Set initial interval based on adaptive mode
	initialInterval := time.Second
	if w.stats.adaptive_interval {
		initialInterval = w.stats.GetPingInterval()
		w.lastIntervalCheck = time.Now()
	}
	w.loopTicker = time.NewTicker(initialInterval)
	w.mu.Unlock()

	go func(w *TCPPingWrapper) {
		for {
			w.mu.Lock()
			// Dynamically adjust interval based on host status
			if w.stats.adaptive_interval {
				if time.Since(w.lastIntervalCheck) > 5*time.Second {
					desiredInterval := w.stats.GetPingInterval()
					if desiredInterval != initialInterval {
						w.loopTicker.Reset(desiredInterval)
						initialInterval = desiredInterval
					}
					w.lastIntervalCheck = time.Now()
				}
			}
			w.mu.Unlock()

			go func(t *TCPPingWrapper) {
				t.ping()
			}(w)
			select {
			case <-w.loopTicker.C:
			case <-w.stopChan:
				return
			}
		}
	}(w)

}

func (w *TCPPingWrapper) ping() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()

	start := time.Now()
	w.mu.Lock()
	w.stats.lastsent = start.UnixNano()
	w.mu.Unlock()

	var dialer net.Dialer
	conn, err := dialer.DialContext(ctx, "tcp", w.str_tgt)
	if err == nil {
		w.mu.Lock()
		w.stats.has_ever_received = true
		w.stats.lastrecv = time.Now().UnixNano()
		w.stats.lastrtt = time.Since(start)
		w.stats.lastrtt_as_string = round(w.stats.lastrtt, 2).String()
		w.mu.Unlock()
		conn.Close()
	}
}

func (w *TCPPingWrapper) Stop() {
	w.stopOnce.Do(func() {
		w.mu.Lock()
		if w.loopTicker != nil {
			w.loopTicker.Stop()
		}
		if w.stopChan != nil {
			close(w.stopChan)
		}
		w.mu.Unlock()
	})
}

func (w *TCPPingWrapper) Host() string {
	return w.hstring
}

func (w *TCPPingWrapper) CalcStats(timeout_threshold int64) PWStats {
	// RLock: see pinger_probing.CalcStats for the rationale.
	w.mu.RLock()
	defer w.mu.RUnlock()
	w.stats.ComputeState(timeout_threshold)
	return *w.stats
}

func (w *TCPPingWrapper) Stats() *PWStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := *w.stats
	return &s
}

func (w *TCPPingWrapper) SetHostRepr(h string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.SetHostRepr(h)
}

