package main

import (
	"sync"
	"time"
)

// ErrorWrapper is a safe wrapper used when a host cannot be initialized (e.g. DNS failure).
// It never panics/fatals and surfaces the reason via PWStats.error_message.
type ErrorWrapper struct {
	host  string
	stats *PWStats
	mu    sync.RWMutex
}

func NewErrorWrapper(host string, errMsg string, tw *TransitionWriter) *ErrorWrapper {
	st := &PWStats{
		transition_writer: tw,
		error_message:     errMsg,
		hrepr:             host,
		iprepr:            "",
		state:             false,
	}
	// Seed compute timestamps so "Last seen" doesn't look huge.
	now := time.Now().UnixNano()
	st.startup_time = now
	st.last_compute = now
	return &ErrorWrapper{host: host, stats: st}
}

func (w *ErrorWrapper) Start() {}
func (w *ErrorWrapper) Stop()  {}

func (w *ErrorWrapper) Host() string { return w.host }

func (w *ErrorWrapper) CalcStats(timeoutThreshold int64) PWStats {
	// RLock: ComputeState protects its own state machine via smMu.
	w.mu.RLock()
	defer w.mu.RUnlock()
	w.stats.ComputeState(timeoutThreshold)
	return *w.stats
}

func (w *ErrorWrapper) Stats() *PWStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := *w.stats
	return &s
}

func (w *ErrorWrapper) SetHostRepr(h string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.SetHostRepr(h)
}
