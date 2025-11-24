package main

import "sync"

type WrapperHolder struct {
	ping_wrappers     []PingWrapperInterface
	options           Options
	transition_writer *TransitionWriter
	mu                sync.RWMutex
}

func (w *WrapperHolder) InitHosts(hosts []string, options Options, transition_writer *TransitionWriter) {
	w.options = options
	w.transition_writer = transition_writer
	w.setHosts(hosts)
}

func (w *WrapperHolder) setHosts(hosts []string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.ping_wrappers = make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		w.ping_wrappers[i] = NewPingWrapper(host, w.options, w.transition_writer)
	}
}

func (w *WrapperHolder) ReplaceHosts(hosts []string) {
	w.mu.Lock()
	old := w.ping_wrappers
	w.ping_wrappers = make([]PingWrapperInterface, len(hosts))
	for i, host := range hosts {
		w.ping_wrappers[i] = NewPingWrapper(host, w.options, w.transition_writer)
	}
	newWrappers := w.ping_wrappers
	w.mu.Unlock()

	for _, pw := range old {
		pw.Stop()
	}
	for _, pw := range newWrappers {
		pw.Start()
	}
}

func (w *WrapperHolder) Wrappers() []PingWrapperInterface {
	w.mu.RLock()
	defer w.mu.RUnlock()
	out := make([]PingWrapperInterface, len(w.ping_wrappers))
	copy(out, w.ping_wrappers)
	return out
}

func (w *WrapperHolder) CalcStats(timeout_threshold int64) {
	for _, wrapper := range w.Wrappers() {
		wrapper.CalcStats(timeout_threshold)
	}
}

func (w *WrapperHolder) Start() {
	for _, ping_wrapper := range w.Wrappers() {
		ping_wrapper.Start()
	}
}

func (w *WrapperHolder) Stop() {
	for _, ping_wrapper := range w.Wrappers() {
		ping_wrapper.Stop()
	}
}
