//go:build !windows

package main

// inspired from https://github.com/cloverstd/tcping/blob/master/ping/tcp/tcp.go

import (
	"context"
	"fmt"
	"net"
	"strings"
	"sync"
	"time"

	tcpshaker "github.com/tevino/tcp-shaker"
)

type TCPPingWrapper struct {
	host              string
	ip                *net.IPAddr
	hstring           string
	port              int
	str_tgt           string
	stats             *PWStats
	stopCheckLoop     bool
	loopTicker        *time.Ticker
	mu                sync.RWMutex
	lastIntervalCheck time.Time
}

func (w *TCPPingWrapper) Start() {
	// Use host as initial display name (DNS lookup happens later via periodic updates)
	displayHost := w.host
	w.hstring = fmt.Sprintf("tcp://%v:%v (%v:%v)", displayHost, w.port, w.ip.String(), w.port)
	w.stats.SetHostRepr(fmt.Sprintf("tcp://%v:%v", displayHost, w.port))
	w.stats.iprepr = w.ip.IP.String()

	if strings.Contains(w.stats.iprepr, ":") {
		w.str_tgt = fmt.Sprintf("[%v]:%v", w.ip.String(), w.port)
		w.hstring = fmt.Sprintf("tcp://%v:%v ([%v]:%v)", displayHost, w.port, w.ip.String(), w.port)
	} else {
		w.str_tgt = fmt.Sprintf("%v:%v", w.ip.String(), w.port)
	}

	w.stopCheckLoop = false

	// Set initial interval based on adaptive mode
	initialInterval := time.Second
	if w.stats.adaptive_interval {
		initialInterval = w.stats.GetPingInterval()
		w.lastIntervalCheck = time.Now()
	}
	w.loopTicker = time.NewTicker(initialInterval)

	go func(w *TCPPingWrapper) {
		for !w.stopCheckLoop {
			// Dynamically adjust interval based on host status
			if w.stats.adaptive_interval {
				w.mu.Lock()
				if time.Now().Sub(w.lastIntervalCheck) > 5*time.Second {
					desiredInterval := w.stats.GetPingInterval()
					if desiredInterval != initialInterval {
						w.loopTicker.Reset(desiredInterval)
						initialInterval = desiredInterval
					}
					w.lastIntervalCheck = time.Now()
				}
				w.mu.Unlock()
			}

			go func(t *TCPPingWrapper) {
				t.spawnChecker()
			}(w)
			<-w.loopTicker.C
		}
	}(w)

}

func (w *TCPPingWrapper) spawnChecker() {
	checker := tcpshaker.NewChecker()

	ctx, stopChecker := context.WithCancel(context.Background())
	defer stopChecker()
	go func() {
		if err := checker.CheckingLoop(ctx); err != nil {
			fmt.Println("checking loop stopped due to fatal error: ", err)
		}
	}()
	<-checker.WaitReady()
	start := time.Now()
	w.mu.Lock()
	w.stats.lastsent = time.Now().UnixNano()
	w.mu.Unlock()
	err := checker.CheckAddr(w.str_tgt, time.Second)
	if err == nil {
		w.mu.Lock()
		w.stats.has_ever_received = true
		w.stats.lastrecv = time.Now().UnixNano()
		w.stats.lastrtt = time.Since(start)
		w.stats.lastrtt_as_string = round(w.stats.lastrtt, 2).String()
		w.mu.Unlock()
	}
}

func (w *TCPPingWrapper) Stop() {
	w.stopCheckLoop = true
	w.loopTicker.Stop()
}

func (w *TCPPingWrapper) Host() string {
	return w.hstring
}

func (w *TCPPingWrapper) CalcStats(timeout_threshold int64) PWStats {
	w.mu.Lock()
	defer w.mu.Unlock()
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
