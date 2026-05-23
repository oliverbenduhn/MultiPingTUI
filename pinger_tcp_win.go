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
	host       string
	ip         *net.IPAddr
	hstring    string
	port       int
	str_tgt    string
	stats      *PWStats
	loopTicker *time.Ticker
	stopChan   chan struct{}
	stopOnce   sync.Once
	mu         sync.RWMutex
}

func (w *TCPPingWrapper) Start() {
	// Use host as initial display name (DNS lookup happens later via periodic updates)
	displayHost := w.host
	w.hstring = fmt.Sprintf("tcp://%v:%v (%v:%v)", displayHost, w.port, w.ip.String(), w.port)
	w.stats.SetHostRepr(fmt.Sprintf("tcp://%v:%v", displayHost, w.port))
	w.stats.iprepr = w.ip.IP.String()

	w.str_tgt = fmt.Sprintf("%v:%v", w.ip.String(), w.port)

	w.stopChan = make(chan struct{})
	w.stopOnce = sync.Once{}
	w.loopTicker = time.NewTicker(time.Second)

	go func(w *TCPPingWrapper) {
		for {
			go func(t *TCPPingWrapper) {
				t.spawnChecker()
			}(w)
			select {
			case <-w.loopTicker.C:
			case <-w.stopChan:
				return
			}
		}
	}(w)

}

func (w *TCPPingWrapper) spawnChecker() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	go func() {
		time.Sleep(time.Second)
		cancel()
	}()

	start := time.Now()

	var conn net.Conn
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
		if w.loopTicker != nil {
			w.loopTicker.Stop()
		}
		if w.stopChan != nil {
			close(w.stopChan)
		}
	})
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
