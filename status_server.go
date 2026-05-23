package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sort"
	"strings"
	"sync"
	"time"
)

// HostStatus represents the public status information for a host.
type HostStatus struct {
	Key              string `json:"key"`
	Host             string `json:"host"`
	IP               string `json:"ip"`
	Online           bool   `json:"online"`
	RTT              string `json:"rtt"`
	LastReply        string `json:"last_reply"`
	OnlineTime       string `json:"online_time"`
	LastLossAgo      string `json:"last_loss_ago,omitempty"`
	LastLossDuration string `json:"last_loss_duration,omitempty"`
	Error            string `json:"error,omitempty"`
}

type ServerView struct {
	Filter FilterMode      `json:"filter"`
	Sort   SortMode        `json:"sort"`
	Rate   UpdateRate      `json:"rate"`
	Hidden map[string]bool `json:"hidden"`
	Cols   []int           `json:"cols"`
}

type ViewState struct {
	View     ServerView   `json:"view"`
	Statuses []HostStatus `json:"statuses"`
	Updated  time.Time    `json:"updated"`
}

type StatsProvider func(PingWrapperInterface) PWStats

type StatusServer struct {
	repo          HostRepository
	srv           *http.Server
	statsProvider StatsProvider
	view          ServerView
	viewMu        sync.RWMutex

	traceMu     sync.RWMutex
	traces      map[string]*webTraceState
	globalStats *GlobalStatistics
	traceSem    chan struct{}
}

type webTraceState struct {
	running    bool
	seq        int
	startedAt  time.Time
	finishedAt time.Time
	target     string
	output     string
	err        string
	took       time.Duration
}

type traceRequest struct {
	Key string `json:"key"`
}

type traceResponse struct {
	Key        string    `json:"key"`
	Running    bool      `json:"running"`
	Target     string    `json:"target,omitempty"`
	StartedAt  time.Time `json:"started_at,omitempty"`
	FinishedAt time.Time `json:"finished_at,omitempty"`
	TookMs     int64     `json:"took_ms,omitempty"`
	Error      string    `json:"error,omitempty"`
	Output     string    `json:"output,omitempty"`
}

const maxTraceStates = 256

func StartStatusServer(repo HostRepository, provider StatsProvider, initialView ServerView, port int, globalStats *GlobalStatistics) (*StatusServer, error) {
	if port <= 0 {
		return nil, nil
	}

	server := &StatusServer{
		repo:          repo,
		statsProvider: provider,
		view:          initialView,
		traces:        make(map[string]*webTraceState),
		globalStats:   globalStats,
		traceSem:      make(chan struct{}, 2),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.htmlHandler)
	mux.HandleFunc("/dashboard", server.dashboardHtmlHandler)
	mux.HandleFunc("/api/dashboard", server.dashboardApiHandler)
	mux.HandleFunc("/text", server.textHandler)
	mux.HandleFunc("/json", server.jsonHandler)
	mux.HandleFunc("/view", server.viewHandler)
	mux.HandleFunc("/state", server.stateHandler)
	mux.HandleFunc("/trace", server.traceHandler)

	listener, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return nil, err
	}

	server.srv = &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		// Very aggressive timeouts to prevent goroutine leaks
		IdleTimeout:    5 * time.Second,
		ReadTimeout:    3 * time.Second,
		WriteTimeout:   10 * time.Second,
		MaxHeaderBytes: 1 << 20, // 1 MB
	}
	// Disable keep-alives completely to prevent lingering connReader goroutines
	server.srv.SetKeepAlivesEnabled(false)

	go func() {
		err := server.srv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "status server error: %v\n", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "Status server listening on http://%s (/: live view, /json: JSON, /text: plain text)\n", server.srv.Addr)

	return server, nil
}

func (s *StatusServer) Stop() {
	if s == nil || s.srv == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = s.srv.Shutdown(ctx)
}

func allowMethods(w http.ResponseWriter, r *http.Request, methods ...string) bool {
	for _, method := range methods {
		if r.Method == method {
			return true
		}
	}
	w.Header().Set("Allow", strings.Join(methods, ", "))
	http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	return false
}

func (s *StatusServer) traceHandler(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")

	switch r.Method {
	case http.MethodGet:
		key := strings.TrimSpace(r.URL.Query().Get("key"))
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}
		resp := s.snapshotTrace(key)
		_ = json.NewEncoder(w).Encode(resp)
		return

	case http.MethodPost:
		defer r.Body.Close()
		var req traceRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		key := strings.TrimSpace(req.Key)
		if key == "" {
			http.Error(w, "missing key", http.StatusBadRequest)
			return
		}

		wrapper, ok := s.findWrapperByKey(key)
		if !ok {
			http.Error(w, "unknown key", http.StatusNotFound)
			return
		}

		stats := s.statsProvider(wrapper)
		target := strings.TrimSpace(stats.iprepr)
		if target == "" {
			target = deriveTraceTarget(wrapper.Host())
		}
		if target == "" {
			http.Error(w, "unable to derive target", http.StatusBadRequest)
			return
		}

		// Try to acquire slot in trace semaphore non-blocking.
		select {
		case s.traceSem <- struct{}{}:
			// Acquired slot
		default:
			http.Error(w, "Too many concurrent traceroutes. Please try again later.", http.StatusTooManyRequests)
			return
		}

		seq := s.startTrace(key, target)
		timeout := 20 * time.Second
		go func(hostKey string, hostTarget string, hostSeq int) {
			defer func() { <-s.traceSem }()
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			res := runTraceroute(ctx, hostKey, hostTarget, hostSeq)
			s.finishTrace(res)
		}(key, target, seq)

		resp := s.snapshotTrace(key)
		_ = json.NewEncoder(w).Encode(resp)
		return

	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (s *StatusServer) findWrapperByKey(key string) (PingWrapperInterface, bool) {
	for _, w := range s.repo.GetAll() {
		if w.Host() == key {
			return w, true
		}
	}
	return nil, false
}

func deriveTraceTarget(host string) string {
	h := strings.TrimSpace(host)
	if h == "" {
		return ""
	}
	if idx := strings.Index(h, "://"); idx >= 0 {
		h = h[idx+3:]
	}
	// If it's host:port, strip port.
	if strings.HasPrefix(h, "[") {
		if hostPart, _, err := net.SplitHostPort(h); err == nil {
			return strings.Trim(hostPart, "[]")
		}
		// Might be a bare IPv6 without port.
		return strings.Trim(h, "[]")
	}
	if hostPart, _, err := net.SplitHostPort(h); err == nil {
		return hostPart
	}
	return h
}

func (s *StatusServer) getOrCreateTraceState(key string) *webTraceState {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	if s.traces == nil {
		s.traces = make(map[string]*webTraceState)
	}
	if st, ok := s.traces[key]; ok {
		return st
	}
	st := &webTraceState{}
	s.traces[key] = st
	s.pruneTraceStatesLocked(key)
	return st
}

func (s *StatusServer) pruneTraceStatesLocked(keepKey string) {
	if len(s.traces) <= maxTraceStates {
		return
	}

	var deleteKey string
	var deleteTime time.Time
	for key, st := range s.traces {
		if key == keepKey || st == nil || st.running {
			continue
		}
		t := st.finishedAt
		if t.IsZero() {
			t = st.startedAt
		}
		if deleteKey == "" || t.Before(deleteTime) {
			deleteKey = key
			deleteTime = t
		}
	}
	if deleteKey != "" {
		delete(s.traces, deleteKey)
	}
}

func (s *StatusServer) startTrace(key, target string) int {
	// Hold the lock for the entire operation to avoid TOCTOU between
	// getOrCreateTraceState releasing the lock and us re-acquiring it.
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	if s.traces == nil {
		s.traces = make(map[string]*webTraceState)
	}
	st, ok := s.traces[key]
	if !ok {
		st = &webTraceState{}
		s.traces[key] = st
		s.pruneTraceStatesLocked(key)
	}
	st.seq++
	st.running = true
	st.startedAt = time.Now()
	st.finishedAt = time.Time{}
	st.target = target
	st.output = ""
	st.err = ""
	st.took = 0
	return st.seq
}

func (s *StatusServer) finishTrace(res tracerouteResult) {
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
	st, ok := s.traces[res.host]
	if !ok {
		return
	}
	if res.seq != st.seq {
		return
	}
	st.running = false
	st.finishedAt = time.Now()
	st.target = res.target
	st.output = strings.TrimRight(res.output, "\n")
	if res.err != nil {
		st.err = res.err.Error()
	} else {
		st.err = ""
	}
	st.took = res.took
}

func (s *StatusServer) snapshotTrace(key string) traceResponse {
	s.traceMu.RLock()
	defer s.traceMu.RUnlock()
	st, ok := s.traces[key]
	if !ok || st == nil {
		return traceResponse{Key: key}
	}
	resp := traceResponse{
		Key:        key,
		Running:    st.running,
		Target:     st.target,
		StartedAt:  st.startedAt,
		FinishedAt: st.finishedAt,
		Output:     st.output,
		Error:      st.err,
	}
	if st.took > 0 {
		resp.TookMs = st.took.Milliseconds()
	}
	return resp
}

func (s *StatusServer) jsonHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	statuses := s.collectStatuses()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	if err := json.NewEncoder(w).Encode(statuses); err != nil {
		http.Error(w, "failed to encode status", http.StatusInternalServerError)
	}
}

func (s *StatusServer) stateHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	state := ViewState{
		View:     s.snapshotView(),
		Statuses: s.collectStatuses(),
		Updated:  time.Now(),
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	if err := json.NewEncoder(w).Encode(state); err != nil {
		http.Error(w, "failed to encode state", http.StatusInternalServerError)
	}
}

type viewPatch struct {
	Filter *FilterMode     `json:"filter,omitempty"`
	Sort   *SortMode       `json:"sort,omitempty"`
	Rate   *UpdateRate     `json:"rate,omitempty"`
	Cols   []int           `json:"cols,omitempty"`
	Hidden map[string]bool `json:"hidden,omitempty"`
}

const (
	maxViewPatchBytes  = 1 << 20
	maxHiddenViewItems = 10000
	maxHiddenKeyLength = 512
)

func (s *StatusServer) viewHandler(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		view := s.snapshotView()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "close")
		if err := json.NewEncoder(w).Encode(view); err != nil {
			http.Error(w, "failed to encode view", http.StatusInternalServerError)
		}
		return
	case http.MethodPost:
		defer r.Body.Close()
		var patch viewPatch
		body := http.MaxBytesReader(w, r.Body, maxViewPatchBytes)
		if err := json.NewDecoder(body).Decode(&patch); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		s.viewMu.Lock()
		if patch.Filter != nil && validFilterMode(*patch.Filter) {
			s.view.Filter = *patch.Filter
		}
		if patch.Sort != nil && validSortMode(*patch.Sort) {
			s.view.Sort = *patch.Sort
		}
		if patch.Rate != nil && validUpdateRate(*patch.Rate) {
			s.view.Rate = *patch.Rate
		}
		if patch.Hidden != nil {
			s.view.Hidden = s.sanitizeHiddenHosts(patch.Hidden)
		}
		if patch.Cols != nil {
			s.view.Cols = normalizeColumns(patch.Cols)
		}
		s.viewMu.Unlock()

		view := s.snapshotView()
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Cache-Control", "no-store")
		w.Header().Set("Connection", "close")
		_ = json.NewEncoder(w).Encode(view)
		return
	default:
		w.Header().Set("Allow", "GET, POST")
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
}

func (s *StatusServer) sanitizeHiddenHosts(hidden map[string]bool) map[string]bool {
	out := make(map[string]bool)
	if len(hidden) == 0 {
		return out
	}

	known := make(map[string]bool)
	for _, wrapper := range s.repo.GetAll() {
		known[wrapper.Host()] = true
	}

	for key, value := range hidden {
		if len(out) >= maxHiddenViewItems {
			break
		}
		key = strings.TrimSpace(key)
		if key == "" || len(key) > maxHiddenKeyLength || !value {
			continue
		}
		if !known[key] {
			continue
		}
		out[key] = true
	}
	return out
}

func (s *StatusServer) textHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	statuses := s.collectStatuses()
	cols := s.columnsFromView()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	for _, st := range statuses {
		fmt.Fprintln(w, s.renderColumns(st, cols))
	}
}

func (s *StatusServer) htmlHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	cols := s.columnsFromView()
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>MultiPingTUI Status</title>
  <style>
    :root {
      color-scheme: dark;
      --bg-primary: #0D1117;
      --bg-panel: #161B22;
      --text-primary: #C9D1D9;
      --text-muted: #8B949E;
      --green: #3FB950;
      --yellow: #E2B93D;
      --red: #F85149;
      --blue: #58A6FF;
      --purple: #BC8CFF;
    }
    * { margin: 0; padding: 0; box-sizing: border-box; }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", "Noto Sans", Helvetica, Arial, sans-serif, "Apple Color Emoji", "Segoe UI Emoji";
      background: var(--bg-primary);
      color: var(--text-primary);
      padding: 24px;
      line-height: 1.5;
    }
    header {
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--bg-panel);
    }
    .nav-links {
      display: flex;
      gap: 20px;
      margin-top: 12px;
    }
    .nav-link {
      color: var(--text-muted);
      text-decoration: none;
      font-weight: 600;
      font-size: 14px;
      padding-bottom: 4px;
      border-bottom: 2px solid transparent;
    }
    .nav-link:hover {
      color: var(--text-primary);
    }
    .nav-link.active {
      color: var(--text-primary);
      border-bottom-color: var(--blue);
    }
    h1 {
      font-size: 24px;
      font-weight: 600;
      margin-bottom: 8px;
      color: var(--text-primary);
    }
    .controls {
      margin-top: 12px;
      display: flex;
      flex-wrap: wrap;
      gap: 12px 18px;
      align-items: center;
    }
    /* ... rest of CSS ... */
    .control-group {
      display: flex;
      gap: 10px;
      align-items: center;
      flex-wrap: wrap;
      background: rgba(240, 246, 252, 0.03);
      border: 1px solid rgba(240, 246, 252, 0.07);
      padding: 10px 12px;
      border-radius: 8px;
    }
    .control-group label {
      font-size: 12px;
      color: var(--text-muted);
      text-transform: uppercase;
      letter-spacing: 0.05em;
      font-weight: 600;
    }
    .control-group select {
      background: var(--bg-primary);
      color: var(--text-primary);
      border: 1px solid rgba(240, 246, 252, 0.12);
      border-radius: 6px;
      padding: 6px 8px;
      font-size: 13px;
      outline: none;
    }
    .cols {
      display: flex;
      gap: 10px;
      flex-wrap: wrap;
    }
    .cols .col-toggle {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      font-size: 13px;
      color: var(--text-primary);
      user-select: none;
    }
    .cols input[type="checkbox"] {
      width: 14px;
      height: 14px;
      accent-color: var(--blue);
    }
    .muted {
      color: var(--text-muted);
      font-size: 14px;
    }
    .muted code {
      background: var(--bg-panel);
      padding: 2px 6px;
      border-radius: 4px;
      font-family: ui-monospace, SFMono-Regular, "SF Mono", Menlo, Consolas, monospace;
      font-size: 13px;
    }
    .container {
      background: var(--bg-panel);
      border-radius: 8px;
      border: 1px solid rgba(240, 246, 252, 0.1);
      overflow-x: auto;
      max-width: 100%%;
    }
    table {
      width: 100%%;
      border-collapse: collapse;
      table-layout: fixed;
      min-width: 640px;
    }
    th, td {
      padding: 12px 16px;
      text-align: left;
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    th {
      background: var(--bg-primary);
      font-weight: 600;
      font-size: 12px;
      text-transform: uppercase;
      letter-spacing: 0.05em;
      color: var(--text-muted);
      position: sticky;
      top: 0;
      z-index: 10;
      border-bottom: 1px solid rgba(240, 246, 252, 0.1);
      cursor: default;
    }
    th.resizable {
      position: sticky;
      padding-right: 22px;
    }
    th .resizer {
      position: absolute;
      right: 0;
      top: 0;
      height: 100%%;
      width: 10px;
      cursor: col-resize;
      user-select: none;
      touch-action: none;
    }
    th .resizer::after {
      content: '';
      position: absolute;
      right: 4px;
      top: 25%%;
      width: 2px;
      height: 50%%;
      background: rgba(139, 148, 158, 0.35);
      border-radius: 1px;
      transition: background 0.15s ease;
    }
    th .resizer:hover::after {
      background: rgba(88, 166, 255, 0.7);
    }
    tbody tr {
      border-bottom: 1px solid rgba(240, 246, 252, 0.05);
      transition: all 0.15s ease;
    }
    tbody tr:hover {
      background: rgba(240, 246, 252, 0.03);
    }
    tbody tr.offline-row {
      opacity: 0.65;
      border-left: 3px solid var(--red);
    }
    tbody tr.offline-row:hover {
      opacity: 0.85;
    }
    .status-cell {
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .status-badge {
      display: inline-flex;
      align-items: center;
      padding: 4px 8px;
      border-radius: 6px;
      font-size: 11px;
      font-weight: 600;
      text-transform: uppercase;
      letter-spacing: 0.03em;
    }
    .status-badge.online {
      background: rgba(63, 185, 80, 0.15);
      color: var(--green);
    }
    .status-badge.offline {
      background: rgba(248, 81, 73, 0.15);
      color: var(--red);
      padding: 4px 10px;
    }
    .rtt-cell {
      display: flex;
      align-items: center;
      gap: 12px;
      font-family: ui-monospace, SFMono-Regular, monospace;
    }
    .rtt-value {
      min-width: 60px;
      font-weight: 500;
    }
    .rtt-bar {
      display: flex;
      gap: 1px;
      height: 14px;
      align-items: center;
    }
    .rtt-bar span {
      display: inline-block;
      width: 3px;
      height: 100%%;
      border-radius: 1px;
      transition: all 0.3s ease;
    }
    .rtt-bar .bar-good {
      background: var(--green);
    }
    .rtt-bar .bar-warn {
      background: var(--yellow);
    }
    .rtt-bar .bar-bad {
      background: var(--red);
    }
    .rtt-bar span:not(.bar-empty) {
      animation: pulse 2s ease-in-out infinite;
    }
    .rtt-bar .bar-empty {
      background: rgba(139, 148, 158, 0.2);
    }
    @keyframes pulse {
      0%%, 100%% { opacity: 1; }
      50%% { opacity: 0.6; }
    }
    .ip-cell {
      font-family: ui-monospace, SFMono-Regular, monospace;
      font-size: 13px;
      color: var(--blue);
    }
    .name-cell {
      font-weight: 500;
      cursor: pointer;
    }
    .name-cell:hover {
      text-decoration: underline;
    }
    #updated {
      margin-top: 16px;
      font-size: 12px;
      color: var(--text-muted);
      display: flex;
      align-items: center;
      gap: 8px;
    }
    .pill {
      display: inline-flex;
      align-items: center;
      gap: 6px;
      padding: 4px 8px;
      border-radius: 999px;
      background: rgba(88, 166, 255, 0.12);
      color: var(--blue);
      font-size: 12px;
      font-weight: 600;
    }
    .status-indicator {
      width: 8px;
      height: 8px;
      border-radius: 50%%;
      background: var(--green);
      animation: pulse 2s ease-in-out infinite;
    }
    @media (max-width: 840px) {
      body {
        padding: 16px;
      }
      h1 {
        font-size: 20px;
      }
      th, td {
        padding: 10px 12px;
      }
      .rtt-cell {
        gap: 6px;
      }
      table {
        min-width: 520px;
      }
    }
    @media (max-width: 620px) {
      body {
        padding: 12px;
      }
      .container {
        border-radius: 6px;
      }
      th, td {
        padding: 8px 10px;
        font-size: 13px;
      }
      h1 {
        font-size: 18px;
      }
      .muted {
        font-size: 13px;
      }
      .rtt-cell {
        flex-direction: column;
        align-items: flex-start;
        gap: 4px;
      }
      #updated {
        flex-wrap: wrap;
        gap: 6px;
      }
    }
    .detail-overlay {
      position: fixed;
      inset: 0;
      background: rgba(0, 0, 0, 0.55);
      display: none;
      align-items: center;
      justify-content: center;
      padding: 18px;
      z-index: 1000;
    }
    .detail-overlay.open {
      display: flex;
    }
    .detail-panel {
      width: min(720px, 100%%);
      background: var(--bg-panel);
      border: 1px solid rgba(240, 246, 252, 0.12);
      border-radius: 12px;
      overflow: hidden;
      box-shadow: 0 20px 60px rgba(0,0,0,0.5);
    }
    .detail-header {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      padding: 14px 16px;
      border-bottom: 1px solid rgba(240, 246, 252, 0.07);
      background: rgba(240, 246, 252, 0.02);
    }
    .detail-title {
      font-size: 14px;
      font-weight: 700;
      color: var(--text-primary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .detail-close {
      border: 1px solid rgba(240, 246, 252, 0.12);
      background: var(--bg-primary);
      color: var(--text-primary);
      padding: 6px 10px;
      border-radius: 8px;
      cursor: pointer;
      font-size: 13px;
    }
    .detail-body {
      padding: 16px;
      display: grid;
      grid-template-columns: 1fr 1fr;
      gap: 12px 18px;
    }
    .detail-row {
      display: flex;
      flex-direction: column;
      gap: 4px;
      min-width: 0;
    }
    .detail-label {
      font-size: 11px;
      color: var(--text-muted);
      text-transform: uppercase;
      letter-spacing: 0.06em;
      font-weight: 700;
    }
    .detail-value {
      font-size: 14px;
      color: var(--text-primary);
      overflow: hidden;
      text-overflow: ellipsis;
      white-space: nowrap;
    }
    .detail-value.mono {
      font-family: ui-monospace, SFMono-Regular, monospace;
      font-size: 13px;
      color: var(--blue);
    }
    .detail-value.error {
      color: var(--red);
      white-space: normal;
      overflow: visible;
    }
    .detail-section {
      grid-column: 1 / -1;
      margin-top: 8px;
      padding-top: 14px;
      border-top: 1px solid rgba(240, 246, 252, 0.07);
      display: flex;
      flex-direction: column;
      gap: 10px;
      min-width: 0;
    }
    .detail-section-title {
      display: flex;
      align-items: center;
      justify-content: space-between;
      gap: 12px;
      font-weight: 800;
      font-size: 12px;
      color: var(--text-muted);
      text-transform: uppercase;
      letter-spacing: 0.06em;
    }
    .trace-controls {
      display: flex;
      gap: 8px;
      flex-wrap: wrap;
    }
    .btn {
      appearance: none;
      border: 1px solid rgba(240, 246, 252, 0.12);
      background: rgba(88, 166, 255, 0.12);
      color: var(--text-primary);
      padding: 6px 10px;
      border-radius: 8px;
      cursor: pointer;
      font-size: 13px;
      font-weight: 700;
    }
    .btn:disabled {
      opacity: 0.6;
      cursor: not-allowed;
    }
    .trace-status {
      color: var(--text-muted);
      font-size: 13px;
      white-space: normal;
      overflow: visible;
    }
    pre.trace-output {
      margin: 0;
      background: rgba(13, 17, 23, 0.6);
      border: 1px solid rgba(240, 246, 252, 0.12);
      border-radius: 10px;
      padding: 12px;
      overflow: auto;
      white-space: pre;
      font-family: ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, "Liberation Mono", "Courier New", monospace;
      font-size: 12.5px;
      color: var(--text-primary);
      max-height: 46vh;
    }
    @media (max-width: 720px) {
      .detail-body {
        grid-template-columns: 1fr;
      }
    }
  </style>
</head>
<body>
  <header>
    <h1>🌐 MultiPingTUI Live Status</h1>
    <div class="nav-links">
      <a href="/" class="nav-link active">Live List</a>
      <a href="/dashboard" class="nav-link">Dashboard</a>
    </div>
    <p class="muted" style="margin-top:12px">Auto-refreshes every second · <code>/state</code> includes view+data · <code>/json</code> data only · <code>/text</code> plain text</p>
	    <div class="controls">
	      <div class="control-group">
	        <label for="filter">Filter</label>
	        <select id="filter">
	          <option value="0">All</option>
	          <option value="1">Smart</option>
	          <option value="2">Online</option>
	          <option value="3">Offline</option>
	        </select>
	      </div>
	      <div class="control-group">
	        <label for="rate">Rate</label>
	        <select id="rate">
	          <option value="0">100ms</option>
	          <option value="1">1s</option>
	          <option value="2">5s</option>
	          <option value="3">30s</option>
	        </select>
	      </div>
	      <div class="control-group">
	        <label for="sort">Sort</label>
	        <select id="sort">
	          <option value="0">Name</option>
          <option value="1">Status</option>
          <option value="2">RTT</option>
          <option value="3">Last Seen</option>
          <option value="4">IP</option>
        </select>
      </div>
      <div class="control-group">
        <label>Columns</label>
        <div class="cols" id="cols"></div>
      </div>
      <span class="pill" id="sync-pill">synced</span>
    </div>
  </header>

  <div class="container">
    <table id="status">
      <colgroup id="colgroup"></colgroup>
      <thead>
        <tr></tr>
      </thead>
      <tbody></tbody>
    </table>
  </div>

  <div id="updated">
    <span class="status-indicator"></span>
    <span>Loading…</span>
  </div>

  <div id="detail-overlay" class="detail-overlay" aria-hidden="true">
    <div class="detail-panel" role="dialog" aria-modal="true" aria-labelledby="detail-title">
      <div class="detail-header">
        <div class="detail-title" id="detail-title">Details</div>
        <button class="detail-close" id="detail-close" type="button">Close</button>
      </div>
      <div class="detail-body" id="detail-body"></div>
    </div>
  </div>

  <script>
    const initialColumns = %s;
    const columnNames = {1:'Status', 2:'Name', 3:'IP Address', 4:'RTT', 5:'Last Reply', 6:'Last Loss'};
    const tbody = document.querySelector('#status tbody');
    const theadRow = document.querySelector('#status thead tr');
    const colgroup = document.querySelector('#colgroup');
    const updatedEl = document.querySelector('#updated span:last-child');
    const syncPill = document.querySelector('#sync-pill');
    const filterEl = document.querySelector('#filter');
    const rateEl = document.querySelector('#rate');
    const sortEl = document.querySelector('#sort');
    const colsEl = document.querySelector('#cols');
    const detailOverlay = document.querySelector('#detail-overlay');
    const detailTitle = document.querySelector('#detail-title');
    const detailBody = document.querySelector('#detail-body');
    const detailClose = document.querySelector('#detail-close');
    let refreshTimer = null;
    let refreshIntervalMs = 1000;
    let selectedKey = null;
    let traceTimer = null;
    let currentColCount = 6;
    let lastTrace = null;

    const WIDTHS_KEY = 'mping.columnWidths.v1';
    const DEFAULT_WIDTHS = {1: 120, 2: 260, 3: 210, 4: 200, 5: 170, 6: 240};
    const MIN_WIDTH = 80;

    function normalizeCols(cols) {
      if (!Array.isArray(cols) || cols.length === 0) return [1,2,3,4,5,6];
      const set = new Set(cols.map((n) => Number(n)).filter((n) => Number.isInteger(n) && n >= 1 && n <= 6));
      return Array.from(set).sort((a,b) => a-b);
    }

    function loadWidths() {
      try {
        const raw = localStorage.getItem(WIDTHS_KEY);
        const parsed = raw ? JSON.parse(raw) : {};
        if (parsed && typeof parsed === 'object') return parsed;
      } catch (_) {}
      return {};
    }

    function saveWidths(widths) {
      try {
        localStorage.setItem(WIDTHS_KEY, JSON.stringify(widths));
      } catch (_) {}
    }

    function getColWidth(widths, col) {
      const v = Number(widths[col]);
      if (Number.isFinite(v) && v >= MIN_WIDTH) return v;
      return DEFAULT_WIDTHS[col] || 160;
    }

	    function renderControls(view) {
	      if (typeof view.filter === 'number') {
	        filterEl.value = String(view.filter);
	      }
	      if (typeof view.rate === 'number') {
	        rateEl.value = String(view.rate);
	      }
	      if (typeof view.sort === 'number') {
	        sortEl.value = String(view.sort);
	      }

      const cols = normalizeCols(view.cols);
      colsEl.innerHTML = '';
      for (let i = 1; i <= 6; i++) {
        const label = document.createElement('label');
        label.className = 'col-toggle';
        const cb = document.createElement('input');
        cb.type = 'checkbox';
        cb.checked = cols.includes(i);
        cb.dataset.col = String(i);
        label.appendChild(cb);
        const span = document.createElement('span');
        span.textContent = columnNames[i];
        label.appendChild(span);
        colsEl.appendChild(label);
      }
    }

    function renderTableStructure(cols) {
      const widths = loadWidths();

      colgroup.innerHTML = cols.map((c) => '<col data-col="' + c + '" style="width:' + getColWidth(widths, c) + 'px">').join('');

      theadRow.innerHTML = cols.map((c, idx) => {
        const canResize = idx !== cols.length - 1;
        const resizer = canResize ? '<span class="resizer" data-col="' + c + '"></span>' : '';
        return '<th class="' + (canResize ? 'resizable' : '') + '" data-col="' + c + '">' + columnNames[c] + resizer + '</th>';
      }).join('');

      // Attach resizing handlers for this freshly-rendered header
      theadRow.querySelectorAll('.resizer').forEach((handle) => {
        handle.addEventListener('pointerdown', (e) => {
          e.preventDefault();
          const col = Number(handle.dataset.col);
          const widthsNow = loadWidths();
          const startX = e.clientX;
          const startW = getColWidth(widthsNow, col);

          const colEl = colgroup.querySelector('col[data-col="' + col + '"]');
          if (!colEl) return;

          const onMove = (ev) => {
            const next = Math.max(MIN_WIDTH, Math.round(startW + (ev.clientX - startX)));
            colEl.style.width = next + 'px';
            widthsNow[col] = next;
          };

          const onUp = () => {
            document.removeEventListener('pointermove', onMove);
            document.removeEventListener('pointerup', onUp);
            saveWidths(widthsNow);
          };

          document.addEventListener('pointermove', onMove);
          document.addEventListener('pointerup', onUp);
        });
      });
    }

    function parseRTT(rttStr) {
      if (!rttStr || rttStr === '-') return null;
      const match = rttStr.match(/^([\d.]+)(ms|µs|s)$/);
      if (!match) return null;
      let value = parseFloat(match[1]);
      const unit = match[2];
      if (unit === 's') value *= 1000;
      if (unit === 'µs') value /= 1000;
      return value;
    }

    function createRTTBar(rttMs) {
      if (rttMs === null) return '';

      const maxRTT = 200;
      const bars = 12;
      const filledCount = Math.min(bars, Math.ceil((rttMs / maxRTT) * bars));

      let colorClass = 'bar-good';
      if (rttMs > 150) {
        colorClass = 'bar-bad';
      } else if (rttMs > 50) {
        colorClass = 'bar-warn';
      }

      let html = '<div class="rtt-bar">';
      for (let i = 0; i < bars; i++) {
        if (i < filledCount) {
          html += '<span class="' + colorClass + '"></span>';
        } else {
          html += '<span class="bar-empty"></span>';
        }
      }
      html += '</div>';
      return html;
    }

    function renderUpdated(text) {
      const now = new Date();
      updatedEl.textContent = text + ' · ' + now.toLocaleTimeString();
    }

    function escapeHtml(s) {
      return String(s)
        .replaceAll('&', '&amp;')
        .replaceAll('<', '&lt;')
        .replaceAll('>', '&gt;')
        .replaceAll('"', '&quot;')
        .replaceAll("'", '&#39;');
    }

    function openDetail(row) {
      const nextKey = row.key || null;
      const alreadyOpen = detailOverlay.classList.contains('open');
      const sameKey = alreadyOpen && selectedKey && nextKey === selectedKey;
      selectedKey = nextKey;

      if (sameKey) {
        updateDetailValues(row);
        return;
      }

      detailTitle.textContent = row.host || 'Details';
      detailBody.innerHTML =
        '<div class="detail-row"><div class="detail-label">Host</div><div class="detail-value" data-field="host"></div></div>' +
        '<div class="detail-row"><div class="detail-label">IP</div><div class="detail-value mono" data-field="ip"></div></div>' +
        '<div class="detail-row"><div class="detail-label">Status</div><div class="detail-value" data-field="status"></div></div>' +
        '<div class="detail-row"><div class="detail-label">RTT</div><div class="detail-value mono" data-field="rtt"></div></div>' +
        '<div class="detail-row"><div class="detail-label">Last Reply</div><div class="detail-value" data-field="last_reply"></div></div>' +
        '<div class="detail-row"><div class="detail-label">Last Loss</div><div class="detail-value" data-field="last_loss"></div></div>' +
        '<div class="detail-row"><div class="detail-label">Online Time</div><div class="detail-value" data-field="online_time"></div></div>' +
        '<div class="detail-row" data-field-row="error" style="display:none;"><div class="detail-label">Error</div><div class="detail-value error" data-field="error"></div></div>' +
        '<div class="detail-section">' +
          '<div class="detail-section-title">' +
            '<span>Traceroute</span>' +
            '<div class="trace-controls">' +
              '<button class="btn" type="button" id="trace-run">Run</button>' +
              '<button class="btn" type="button" id="trace-refresh">Refresh</button>' +
            '</div>' +
          '</div>' +
          '<div class="trace-status" id="trace-status"></div>' +
          '<pre class="trace-output" id="trace-output"></pre>' +
        '</div>';

      updateDetailValues(row);
      detailOverlay.classList.add('open');
      detailOverlay.setAttribute('aria-hidden', 'false');

      wireTraceControls();
      startTracePolling();
    }

    function updateDetailValues(row) {
      detailTitle.textContent = row.host || 'Details';
      const statusText = row.online ? 'ONLINE' : 'OFFLINE';
      const rttText = row.online ? (row.rtt || '-') : '-';
      const lossText = row.last_loss_ago ? (row.last_loss_ago + ' (' + (row.last_loss_duration || '-') + ')') : '-';

      const set = (field, val) => {
        const el = detailBody.querySelector('[data-field="' + field + '"]');
        if (!el) return;
        el.textContent = val == null ? '' : String(val);
      };

      set('host', row.host || '-');
      set('ip', row.ip || '-');
      set('status', statusText);
      set('rtt', rttText);
      set('last_reply', row.last_reply || '-');
      set('last_loss', lossText);
      set('online_time', row.online_time || '-');

      const errRow = detailBody.querySelector('[data-field-row="error"]');
      if (row.error) {
        set('error', row.error);
        if (errRow) errRow.style.display = '';
      } else {
        set('error', '');
        if (errRow) errRow.style.display = 'none';
      }
    }

    function closeDetail() {
      detailOverlay.classList.remove('open');
      detailOverlay.setAttribute('aria-hidden', 'true');
      selectedKey = null;
      stopTracePolling();
    }

    detailClose.addEventListener('click', closeDetail);
    detailOverlay.addEventListener('click', (e) => {
      if (e.target === detailOverlay) closeDetail();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && detailOverlay.classList.contains('open')) closeDetail();
    });

    function stopTracePolling() {
      if (traceTimer) {
        clearInterval(traceTimer);
        traceTimer = null;
      }
      lastTrace = null;
    }

    function startTracePolling() {
      stopTracePolling();
      traceTimer = setInterval(fetchTrace, 1000);
      fetchTrace();
    }

    function renderTrace(trace) {
      const statusEl = document.querySelector('#trace-status');
      const outEl = document.querySelector('#trace-output');
      const runBtn = document.querySelector('#trace-run');
      const refreshBtn = document.querySelector('#trace-refresh');
      if (!statusEl || !outEl) return;

      if (!trace || !trace.key) {
        statusEl.textContent = 'No traceroute yet.';
        outEl.textContent = '';
        if (runBtn) runBtn.disabled = false;
        if (refreshBtn) refreshBtn.disabled = false;
        return;
      }

      const took = (typeof trace.took_ms === 'number' && trace.took_ms > 0)
        ? (' · ' + (Math.round(trace.took_ms / 100) / 10) + 's')
        : '';

      if (trace.running) {
        statusEl.textContent = 'Running to ' + (trace.target || '-') + '…';
        if (runBtn) runBtn.disabled = true;
      } else {
        if (runBtn) runBtn.disabled = false;
        if (trace.error) {
          statusEl.textContent = 'Error: ' + trace.error + (trace.target ? (' (target ' + trace.target + ')') : '') + took;
        } else if ((trace.output || '').trim() !== '') {
          statusEl.textContent = 'Target ' + (trace.target || '-') + took;
        } else {
          statusEl.textContent = 'No traceroute yet.';
        }
      }
      if (refreshBtn) refreshBtn.disabled = false;
      outEl.textContent = trace.output || '';
    }

    async function fetchTrace() {
      if (!selectedKey || !detailOverlay.classList.contains('open')) return;
      try {
        const res = await fetch('/trace?key=' + encodeURIComponent(selectedKey), {cache:'no-store'});
        const trace = await res.json();
        lastTrace = trace;
        if (trace && trace.key === selectedKey) renderTrace(trace);
      } catch (_) {}
    }

    async function runTrace() {
      if (!selectedKey) return;
      const runBtn = document.querySelector('#trace-run');
      if (runBtn) runBtn.disabled = true;
      try {
        await fetch('/trace', {
          method: 'POST',
          headers: {'Content-Type': 'application/json'},
          body: JSON.stringify({key: selectedKey}),
          cache: 'no-store'
        });
      } catch (_) {}
      await fetchTrace();
    }

    function wireTraceControls() {
      const runBtn = document.querySelector('#trace-run');
      const refreshBtn = document.querySelector('#trace-refresh');
      if (runBtn) runBtn.onclick = runTrace;
      if (refreshBtn) refreshBtn.onclick = fetchTrace;
      if (lastTrace && lastTrace.key === selectedKey) renderTrace(lastTrace);
      else renderTrace({key: selectedKey});
    }

	    function rateToMs(rate) {
	      switch (rate) {
	        case 0: return 250;    // UpdateRate100ms (cap web polling a bit)
	        case 1: return 1000;   // UpdateRate1s
	        case 2: return 5000;   // UpdateRate5s
	        case 3: return 30000;  // UpdateRate30s
	        default: return 1000;
	      }
	    }

	    function setAutoRefresh(ms) {
	      if (refreshTimer) {
	        clearInterval(refreshTimer);
	      }
	      refreshIntervalMs = ms;
	      refreshTimer = setInterval(refresh, ms);
	    }

	    async function updateView(patch) {
	      await fetch('/view', {
	        method: 'POST',
	        headers: {'Content-Type': 'application/json'},
	        body: JSON.stringify(patch),
	        cache: 'no-store'
	      });
	    }

    function currentSelectedCols() {
      const cols = [];
      colsEl.querySelectorAll('input[type="checkbox"]').forEach((cb) => {
        if (cb.checked) cols.push(Number(cb.dataset.col));
      });
      return normalizeCols(cols);
    }

        async function refresh() {
          try {
            const res = await fetch('/state', {cache:'no-store', headers:{'Cache-Control':'no-cache','Pragma':'no-cache'}});
            const state = await res.json();
            const view = state.view || {};
            const columns = normalizeCols(view.cols || initialColumns);
            currentColCount = columns.length;
            renderTableStructure(columns);
            renderControls(view);
            const desiredInterval = rateToMs(view.rate);
            if (desiredInterval !== refreshIntervalMs) {
              setAutoRefresh(desiredInterval);
            }
    
            const data = state.statuses || [];
            
            const existingRows = new Map();
            tbody.querySelectorAll('tr').forEach((tr) => {
              if (tr.dataset.key) existingRows.set(tr.dataset.key, tr);
            });
            
            const newKeys = new Set();
    
            for (const row of data) {
              const key = row.key;
              newKeys.add(key);
              
              let tr = existingRows.get(key);
              if (!tr) {
                tr = document.createElement('tr');
                tr.dataset.key = key;
                // Create cells initially
                columns.forEach(col => {
                    const td = document.createElement('td');
                    td.dataset.col = col;
                    tr.appendChild(td);
                });
                tbody.appendChild(tr);
              } else {
                 // Check if columns changed
                 const currentCells = tr.querySelectorAll('td');
                 if (currentCells.length !== columns.length) {
                     tr.innerHTML = '';
                     columns.forEach(col => {
                        const td = document.createElement('td');
                        td.dataset.col = col;
                        tr.appendChild(td);
                     });
                 }
              }
    
              if (!row.online) {
                tr.className = 'offline-row';
              } else {
                tr.className = '';
              }
    
              const cells = tr.querySelectorAll('td');
              
              columns.forEach((col, idx) => {
                const td = cells[idx];
                if (!td) return; // Should not happen given logic above
    
                let newVal = '';
                let newClass = '';
                
                // Calculate new content
                if (col === 1) {
                     newVal = row.online
                      ? '<div class="status-cell"><span class="status-badge online">● Online</span></div>'
                      : '<div class="status-cell"><span class="status-badge offline">○ Offline</span></div>';
                } else if (col === 2) {
                     newClass = 'name-cell';
                     newVal = escapeHtml(row.host || '-');
                } else if (col === 3) {
                     newClass = 'ip-cell';
                     newVal = escapeHtml(row.ip || '-');
                } else if (col === 4) {
                     if (row.online && (row.rtt || '-') !== '-') {
                        newVal = '<div class="rtt-cell"><span class="rtt-value">' + escapeHtml(row.rtt) + '</span>' + createRTTBar(parseRTT(row.rtt)) + '</div>';
                     } else {
                        newVal = '-';
                     }
                } else if (col === 5) {
                     newVal = escapeHtml(row.last_reply || '-');
                } else if (col === 6) {
                     newVal = row.last_loss_ago ? escapeHtml(row.last_loss_ago + ' (' + row.last_loss_duration + ')') : '-';
                }
                
                // Apply updates only if changed
                if (td.className !== newClass) td.className = newClass;
                
                // For simple text columns, use textContent to be faster, but we have HTML in col 1 and 4.
                // To be safe and simple, we use innerHTML comparison.
                if (td.innerHTML !== newVal) {
                    td.innerHTML = newVal;
                }
              });
            }
    
            existingRows.forEach((tr, key) => {
              if (!newKeys.has(key)) tr.remove();
            });
    
            if (selectedKey) {
              const match = data.find((r) => r.key === selectedKey);
              if (match) openDetail(match);
            }
            syncPill.textContent = 'synced';
            renderUpdated('Connected');
          } catch (err) {
            if (tbody.children.length === 0) {
                tbody.innerHTML = '<tr><td colspan="' + currentColCount + '" style="color: var(--red); text-align: center; padding: 24px;">⚠ Error loading data</td></tr>';
            }
            syncPill.textContent = 'offline';
            renderUpdated('Disconnected');
          }
        }

    sortEl.addEventListener('change', async () => {
      const sort = Number(sortEl.value);
      syncPill.textContent = 'updating…';
      await updateView({sort});
      await refresh();
    });

	    filterEl.addEventListener('change', async () => {
	      const filter = Number(filterEl.value);
	      syncPill.textContent = 'updating…';
	      await updateView({filter});
	      await refresh();
	    });

	    rateEl.addEventListener('change', async () => {
	      const rate = Number(rateEl.value);
	      syncPill.textContent = 'updating…';
	      await updateView({rate});
	      setAutoRefresh(rateToMs(rate));
	      await refresh();
	    });

	    colsEl.addEventListener('change', async (e) => {
	      if (!e.target || e.target.type !== 'checkbox') return;
	      const cols = currentSelectedCols();
	      syncPill.textContent = 'updating…';
      await updateView({cols});
      await refresh();
    });

    tbody.addEventListener('click', (e) => {
      const cell = e.target && e.target.closest ? e.target.closest('td.name-cell') : null;
      if (!cell) return;
      const tr = cell.closest('tr');
      if (!tr) return;
      const key = tr.dataset.key;
      if (!key) return;
      // Find the row by key from last rendered DOM: stash row data on the tr via dataset?
      // We don't have it, so request a fresh state quickly and open from that.
      (async () => {
        try {
          const res = await fetch('/state', {cache:'no-store'});
          const state = await res.json();
          const data = state.statuses || [];
          const match = data.find((r) => r.key === key);
          if (match) openDetail(match);
        } catch (_) {}
      })();
    });

    setAutoRefresh(1000);
    refresh();
  </script>
</body>
</html>`, marshalColumns(cols))
}

func (s *StatusServer) collectStatuses() []HostStatus {
	wrappers := s.repo.GetAll()
	view := s.snapshotView()
	filtered := s.filterAndSort(wrappers, view)
	statuses := make([]HostStatus, 0, len(filtered))
	now := time.Now()

	for _, wrapper := range filtered {
		stats := s.statsProvider(wrapper)

		key := wrapper.Host()
		host := stats.GetHostRepr()
		if host == "" {
			host = wrapper.Host()
		}

		ip := stats.iprepr
		online := stats.state && stats.error_message == ""
		rtt := "-"
		if online && stats.lastrtt_as_string != "" {
			rtt = stats.lastrtt_as_string
		}

		lastReply := "never"
		if stats.lastrecv > 0 {
			lastReply = fmt.Sprintf("%s ago", time.Duration(stats.last_seen_nano).Round(time.Second))
		}

		onlineTime := stats.OnlineUptime(now.UnixNano()).Round(time.Second).String()

		var lastLossAgo, lastLossDuration string
		if stats.last_loss_nano > 0 {
			lastLossAgo = fmt.Sprintf("%s ago", time.Duration(now.UnixNano()-stats.last_loss_nano).Round(time.Second))
			lastLossDuration = time.Duration(stats.last_loss_duration).Round(time.Second / 10).String()
		}

		statuses = append(statuses, HostStatus{
			Key:              key,
			Host:             host,
			IP:               ip,
			Online:           online,
			RTT:              rtt,
			LastReply:        lastReply,
			OnlineTime:       onlineTime,
			LastLossAgo:      lastLossAgo,
			LastLossDuration: lastLossDuration,
			Error:            stats.error_message,
		})
	}

	return statuses
}

func (s *StatusServer) UpdateView(view ServerView) {
	view.Cols = normalizeColumns(view.Cols)
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	s.view = view
}

func (s *StatusServer) View() ServerView {
	return s.snapshotView()
}

func (s *StatusServer) snapshotView() ServerView {
	s.viewMu.RLock()
	defer s.viewMu.RUnlock()
	copied := ServerView{
		Filter: s.view.Filter,
		Sort:   s.view.Sort,
		Rate:   s.view.Rate,
		Hidden: make(map[string]bool, len(s.view.Hidden)),
		Cols:   append([]int{}, s.view.Cols...),
	}
	for k, v := range s.view.Hidden {
		copied.Hidden[k] = v
	}
	return copied
}

func (s *StatusServer) columnsFromView() []int {
	cols := normalizeColumns(s.snapshotView().Cols)
	if len(cols) == 0 {
		return []int{1, 2, 3, 4, 5, 6}
	}
	return append([]int{}, cols...)
}

func (s *StatusServer) renderColumns(st HostStatus, columns []int) string {
	var parts []string
	for _, c := range columns {
		switch c {
		case 1:
			if st.Online {
				parts = append(parts, "✓")
			} else {
				parts = append(parts, "✗")
			}
		case 2:
			parts = append(parts, st.Host)
		case 3:
			parts = append(parts, st.IP)
		case 4:
			if st.Online {
				parts = append(parts, st.RTT)
			} else {
				parts = append(parts, "-")
			}
		case 5:
			parts = append(parts, st.LastReply)
		case 6:
			if st.LastLossAgo != "" {
				parts = append(parts, fmt.Sprintf("%s (%s)", st.LastLossAgo, st.LastLossDuration))
			} else {
				parts = append(parts, "-")
			}
		}
	}
	return strings.Join(parts, " | ")
}

func normalizeColumns(cols []int) []int {
	if len(cols) == 0 {
		return []int{}
	}
	seen := make(map[int]bool, 6)
	out := make([]int, 0, 6)
	for _, c := range cols {
		if c < 1 || c > 6 || seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	sort.Ints(out)
	return out
}

func validFilterMode(m FilterMode) bool {
	switch m {
	case FilterAll, FilterSmart, FilterOnline, FilterOffline:
		return true
	default:
		return false
	}
}

func validSortMode(m SortMode) bool {
	switch m {
	case SortByName, SortByStatus, SortByRTT, SortByLastSeen, SortByIP:
		return true
	default:
		return false
	}
}

func validUpdateRate(r UpdateRate) bool {
	switch r {
	case UpdateRate100ms, UpdateRate1s, UpdateRate5s, UpdateRate30s:
		return true
	default:
		return false
	}
}

func marshalColumns(cols []int) string {
	data, _ := json.Marshal(cols)
	return string(data)
}

func (s *StatusServer) filterAndSort(wrappers []PingWrapperInterface, view ServerView) []PingWrapperInterface {
	return applyFilterAndSort(wrappers, view.Filter, view.Sort, view.Hidden, s.statsProvider)
}

type DashboardStats struct {
	Total             int               `json:"total"`
	Online            int               `json:"online"`
	Offline           int               `json:"offline"`
	NeverSeen         int               `json:"never_seen"`
	Uptime            string            `json:"uptime"`
	AvgRTT            string            `json:"avg_rtt"`
	HealthPercent     float64           `json:"health_percent"`
	RecentTransitions []TransitionEvent `json:"recent_transitions"`
	TopOffline        []HostStatus      `json:"top_offline"`
	TopRTT            []HostStatus      `json:"top_rtt"`
	RTTDist           []RTTBucket       `json:"rtt_dist"`
}

type RTTBucket struct {
	Label string `json:"label"`
	Count int    `json:"count"`
}

func (s *StatusServer) dashboardApiHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	wrappers := s.repo.GetAll()
	view := s.snapshotView()

	visible := make([]PingWrapperInterface, 0, len(wrappers))
	for _, w := range wrappers {
		if !view.Hidden[w.Host()] {
			visible = append(visible, w)
		}
	}

	total := len(visible)
	online := 0
	offline := 0
	neverSeen := 0
	var totalRTT time.Duration

	buckets := []struct {
		label string
		count int
		max   time.Duration
	}{
		{"<5ms", 0, 5 * time.Millisecond},
		{"5-20ms", 0, 20 * time.Millisecond},
		{"20-50ms", 0, 50 * time.Millisecond},
		{"50-100ms", 0, 100 * time.Millisecond},
		{">100ms", 0, 100 * time.Hour},
	}

	offlineList := make([]HostStatus, 0)
	type rttEntry struct {
		status HostStatus
		rtt    time.Duration
	}
	rttList := make([]rttEntry, 0)
	now := time.Now()
	rttCount := 0

	for _, wrapper := range visible {
		st := s.statsProvider(wrapper)
		isOnline := st.state && st.error_message == ""
		seen := st.has_ever_received

		if isOnline {
			online++
			if st.lastrtt > 0 {
				totalRTT += st.lastrtt
				rttCount++
				for i := range buckets {
					if st.lastrtt < buckets[i].max {
						buckets[i].count++
						break
					}
				}
			}
		} else {
			offline++
		}
		if !seen {
			neverSeen++
		}

		hs := HostStatus{
			Host:        wrapper.Host(),
			IP:          st.iprepr,
			Online:      isOnline,
			RTT:         st.lastrtt_as_string,
			Error:       st.error_message,
			LastLossAgo: "-",
		}
		if st.lastrecv > 0 {
			hs.LastReply = time.Duration(st.last_seen_nano).Round(time.Second).String() + " ago"
		} else {
			hs.LastReply = "never"
		}

		if st.last_loss_nano > 0 {
			hs.LastLossAgo = fmt.Sprintf("%s ago", time.Duration(now.UnixNano()-st.last_loss_nano).Round(time.Second))
			hs.LastLossDuration = time.Duration(st.last_loss_duration).Round(time.Second / 10).String()
		}

		if !isOnline {
			offlineList = append(offlineList, hs)
		} else if st.lastrtt > 0 {
			rttList = append(rttList, rttEntry{status: hs, rtt: st.lastrtt})
		}
	}

	sort.Slice(offlineList, func(i, j int) bool {
		return offlineList[i].Host < offlineList[j].Host
	})
	if len(offlineList) > 5 {
		offlineList = offlineList[:5]
	}

	sort.Slice(rttList, func(i, j int) bool {
		return rttList[i].rtt > rttList[j].rtt
	})
	if len(rttList) > 5 {
		rttList = rttList[:5]
	}
	topRTT := make([]HostStatus, 0, len(rttList))
	for _, entry := range rttList {
		topRTT = append(topRTT, entry.status)
	}

	health := 0.0
	if total > 0 {
		health = (float64(online) / float64(total)) * 100
	}

	avgRTT := "N/A"
	if rttCount > 0 {
		avgRTT = (totalRTT / time.Duration(rttCount)).Round(time.Microsecond).String()
	}

	uptime := "N/A"
	var transitions []TransitionEvent
	if s.globalStats != nil {
		uptime = time.Since(s.globalStats.GetStartTime()).Round(time.Second).String()
		transitions = s.globalStats.GetTransitions(20)
	}

	dist := make([]RTTBucket, len(buckets))
	for i, b := range buckets {
		dist[i] = RTTBucket{Label: b.label, Count: b.count}
	}

	resp := DashboardStats{
		Total:             total,
		Online:            online,
		Offline:           offline,
		NeverSeen:         neverSeen,
		Uptime:            uptime,
		AvgRTT:            avgRTT,
		HealthPercent:     health,
		RecentTransitions: transitions,
		TopOffline:        offlineList,
		TopRTT:            topRTT,
		RTTDist:           dist,
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	json.NewEncoder(w).Encode(resp)
}

func (s *StatusServer) dashboardHtmlHandler(w http.ResponseWriter, r *http.Request) {
	if !allowMethods(w, r, http.MethodGet) {
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	fmt.Fprintf(w, `<!doctype html>
<html lang="en">
<head>
  <meta charset="UTF-8">
  <title>MultiPingTUI Dashboard</title>
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <style>
    :root {
      color-scheme: dark;
      --bg-primary: #0D1117;
      --bg-panel: #161B22;
      --text-primary: #C9D1D9;
      --text-muted: #8B949E;
      --green: #3FB950;
      --yellow: #E2B93D;
      --red: #F85149;
      --blue: #58A6FF;
    }
    body {
      font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Helvetica, Arial, sans-serif;
      background: var(--bg-primary);
      color: var(--text-primary);
      padding: 24px;
      line-height: 1.5;
    }
    header {
      margin-bottom: 24px;
      padding-bottom: 16px;
      border-bottom: 1px solid var(--bg-panel);
    }
    h1 { font-size: 24px; margin-bottom: 8px; margin-top:0; }
    .nav-links { display: flex; gap: 20px; margin-top: 12px; }
    .nav-link { color: var(--text-muted); text-decoration: none; font-weight: 600; padding-bottom: 4px; border-bottom: 2px solid transparent; font-size: 14px; }
    .nav-link:hover { color: var(--text-primary); }
    .nav-link.active { color: var(--text-primary); border-bottom-color: var(--blue); }
    
    .grid { display: grid; grid-template-columns: repeat(auto-fit, minmax(300px, 1fr)); gap: 20px; }
    .card { background: var(--bg-panel); border: 1px solid rgba(240,246,252,0.1); border-radius: 8px; padding: 16px; }
    .card h2 { font-size: 12px; text-transform: uppercase; color: var(--text-muted); margin-bottom: 16px; margin-top: 0; font-weight: 700; letter-spacing: 0.05em; }
    
    .stat-row { display: flex; justify-content: space-between; margin-bottom: 8px; font-size: 14px; }
    .stat-val { font-weight: 600; font-family: ui-monospace, SFMono-Regular, monospace; }
    
    .health-bar { height: 8px; background: rgba(255,255,255,0.1); border-radius: 4px; overflow: hidden; margin-top: 12px; }
    .health-fill { height: 100%%; background: var(--green); transition: width 0.5s ease; }
    
    .list-item { display: flex; justify-content: space-between; align-items: center; font-size: 13px; padding: 8px 0; border-bottom: 1px solid rgba(255,255,255,0.05); }
    .list-item:last-child { border-bottom: none; }
    .badge { padding: 2px 6px; border-radius: 4px; font-size: 11px; font-weight: bold; margin-right: 8px; }
    .badge.up { background: rgba(63, 185, 80, 0.2); color: var(--green); }
    .badge.down { background: rgba(248, 81, 73, 0.2); color: var(--red); }
    
    @media (max-width: 600px) {
        body { padding: 16px; }
        .grid { grid-template-columns: 1fr; }
        .card[style*="span 2"] { grid-column: span 1 !important; }
    }
  </style>
</head>
<body>
  <header>
    <h1>🌐 MultiPingTUI Dashboard</h1>
    <div class="nav-links">
      <a href="/" class="nav-link">Live List</a>
      <a href="/dashboard" class="nav-link active">Dashboard</a>
    </div>
  </header>
  
  <div class="grid">
    <div class="card">
      <h2>Network Health</h2>
      <div class="stat-row"><span>Online</span><span class="stat-val" id="online">-</span></div>
      <div class="stat-row"><span>Total</span><span class="stat-val" id="total">-</span></div>
      <div class="stat-row"><span>Percentage</span><span class="stat-val" id="pct">-</span></div>
      <div class="health-bar"><div class="health-fill" id="health-fill" style="width: 0%%"></div></div>
    </div>
    
    <div class="card">
      <h2>Global Stats</h2>
      <div class="stat-row"><span>Uptime</span><span class="stat-val" id="uptime">-</span></div>
      <div class="stat-row"><span>Avg RTT</span><span class="stat-val" id="avg-rtt">-</span></div>
      <div class="stat-row"><span>Never Seen</span><span class="stat-val" id="never">-</span></div>
    </div>
    
    <div class="card">
      <h2>RTT Distribution</h2>
      <div id="rtt-dist"></div>
    </div>

    <div class="card" style="grid-column: span 2">
      <h2>Recent Transitions</h2>
      <div id="transitions"></div>
    </div>
    
    <div class="card">
      <h2>Top Offline</h2>
      <div id="top-offline"></div>
    </div>
    
    <div class="card">
      <h2>Highest RTT</h2>
      <div id="top-rtt"></div>
    </div>
  </div>

  <script>
    function appendText(parent, tag, text, className) {
      const el = document.createElement(tag);
      if (className) el.className = className;
      el.textContent = text == null ? '' : String(text);
      parent.appendChild(el);
      return el;
    }

    async function refresh() {
      try {
        const res = await fetch('/api/dashboard', {cache:'no-store'});
        const data = await res.json();
        
        document.getElementById('online').textContent = data.online;
        document.getElementById('total').textContent = data.total;
        document.getElementById('pct').textContent = data.health_percent.toFixed(1) + '%%';
        const fill = document.getElementById('health-fill');
        fill.style.width = data.health_percent + '%%';
        
        if (data.health_percent > 90) fill.style.background = 'var(--green)';
        else if (data.health_percent > 50) fill.style.background = 'var(--yellow)';
        else fill.style.background = 'var(--red)';
        
        document.getElementById('uptime').textContent = data.uptime;
        document.getElementById('avg-rtt').textContent = data.avg_rtt;
        document.getElementById('never').textContent = data.never_seen;
        
        const transEl = document.getElementById('transitions');
        transEl.innerHTML = '';
        (data.recent_transitions || []).slice(0, 10).forEach(t => {
          const div = document.createElement('div');
          div.className = 'list-item';
          const time = new Date(t.When).toLocaleTimeString();
          const dur = (t.Duration / 1e9).toFixed(1) + 's';
          const left = document.createElement('div');
          appendText(left, 'span', t.State ? 'UP' : 'DOWN', t.State ? 'badge up' : 'badge down');
          left.appendChild(document.createTextNode(' '));
          appendText(left, 'b', t.Host || '', '');
          const right = appendText(div, 'div', time + ' (' + dur + ')', '');
          right.style.color = 'var(--text-muted)';
          div.insertBefore(left, right);
          transEl.appendChild(div);
        });
        
        const rttDistEl = document.getElementById('rtt-dist');
        rttDistEl.innerHTML = '';
        (data.rtt_dist || []).forEach(b => {
           if(b.count > 0 || true) { // Show all buckets? maybe just non-zero
             if (b.count === 0) return;
             const div = document.createElement('div');
             div.className = 'stat-row';
             appendText(div, 'span', b.label, '');
             appendText(div, 'span', b.count, 'stat-val');
             rttDistEl.appendChild(div);
           }
        });
        
        const offEl = document.getElementById('top-offline');
        offEl.innerHTML = '';
        (data.top_offline || []).forEach(h => {
            const div = document.createElement('div');
            div.className = 'list-item';
            appendText(div, 'span', h.host, '');
            const lastReply = appendText(div, 'span', h.last_reply, 'stat-val');
            lastReply.style.color = 'var(--text-muted)';
            offEl.appendChild(div);
        });
        
        const rttListEl = document.getElementById('top-rtt');
        rttListEl.innerHTML = '';
        (data.top_rtt || []).forEach(h => {
            const div = document.createElement('div');
            div.className = 'list-item';
            appendText(div, 'span', h.host, '');
            appendText(div, 'span', h.rtt, 'stat-val');
            rttListEl.appendChild(div);
        });

      } catch(e) { console.error(e); }
    }
    setInterval(refresh, 2000);
    refresh();
  </script>
</body>
</html>`)
}
