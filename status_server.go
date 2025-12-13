package main

import (
	"bytes"
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

	traceMu sync.RWMutex
	traces  map[string]*webTraceState
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

func StartStatusServer(repo HostRepository, provider StatsProvider, initialView ServerView, port int) (*StatusServer, error) {
	if port <= 0 {
		return nil, nil
	}

	server := &StatusServer{
		repo:          repo,
		statsProvider: provider,
		view:          initialView,
		traces:        make(map[string]*webTraceState),
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.htmlHandler)
	mux.HandleFunc("/text", server.textHandler)
	mux.HandleFunc("/json", server.jsonHandler)
	mux.HandleFunc("/view", server.viewHandler)
	mux.HandleFunc("/state", server.stateHandler)
	mux.HandleFunc("/trace", server.traceHandler)

	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
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

		seq := s.startTrace(key, target)
		timeout := 20 * time.Second
		go func(hostKey string, hostTarget string, hostSeq int) {
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
	return st
}

func (s *StatusServer) startTrace(key, target string) int {
	st := s.getOrCreateTraceState(key)
	s.traceMu.Lock()
	defer s.traceMu.Unlock()
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

func (s *StatusServer) jsonHandler(w http.ResponseWriter, _ *http.Request) {
	statuses := s.collectStatuses()
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	if err := json.NewEncoder(w).Encode(statuses); err != nil {
		http.Error(w, "failed to encode status", http.StatusInternalServerError)
	}
}

func (s *StatusServer) stateHandler(w http.ResponseWriter, _ *http.Request) {
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
		if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
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
			s.view.Hidden = patch.Hidden
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

func (s *StatusServer) textHandler(w http.ResponseWriter, _ *http.Request) {
	statuses := s.collectStatuses()
	cols := s.columnsFromView()
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Connection", "close")
	for _, st := range statuses {
		fmt.Fprintln(w, s.renderColumns(st, cols))
	}
}

func (s *StatusServer) htmlHandler(w http.ResponseWriter, _ *http.Request) {
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
      opacity: 0.3;
      border-left: 3px solid var(--red);
    }
    tbody tr.offline-row:hover {
      opacity: 0.5;
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
    <p class="muted">Auto-refreshes every second · <code>/state</code> includes view+data · <code>/json</code> data only · <code>/text</code> plain text</p>
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
        renderTableStructure(columns);
        renderControls(view);
        const desiredInterval = rateToMs(view.rate);
        if (desiredInterval !== refreshIntervalMs) {
          setAutoRefresh(desiredInterval);
        }

        const data = state.statuses || [];

        // Map existing rows for reuse to prevent flickering
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
          }
          // Ensure row is in the correct position (append moves it if already exists)
          tbody.appendChild(tr);

          if (!row.online) {
            tr.className = 'offline-row';
          } else {
            tr.className = '';
          }

          const colValues = {
            1: row.online
              ? '<div class="status-cell"><span class="status-badge online">● Online</span></div>'
              : '<div class="status-cell"><span class="status-badge offline">○ Offline</span></div>',
            2: row.host || '-',
            3: row.ip || '-',
            4: row.online ? (row.rtt || '-') : '-',
            5: row.last_reply || '-',
            6: row.last_loss_ago ? row.last_loss_ago + ' (' + row.last_loss_duration + ')' : '-'
          };

          let rowHtml = '';
          columns.forEach((col) => {
            const val = colValues[col] ?? '-';

            if (col === 1) {
               rowHtml += '<td>' + val + '</td>';
            } else if (col === 2) {
               rowHtml += '<td class="name-cell">' + escapeHtml(val) + '</td>';
            } else if (col === 3) {
               rowHtml += '<td class="ip-cell">' + escapeHtml(val) + '</td>';
            } else if (col === 4 && row.online && val !== '-') {
               rowHtml += '<td><div class="rtt-cell"><span class="rtt-value">' + escapeHtml(val) + '</span>' + createRTTBar(parseRTT(val)) + '</div></td>';
            } else {
               rowHtml += '<td>' + escapeHtml(val) + '</td>';
            }
          });

          // Only update innerHTML if it changed to minimize heavy relayouts
          if (tr.innerHTML !== rowHtml) {
            tr.innerHTML = rowHtml;
          }
        }

        // Remove rows that are no longer in the data
        existingRows.forEach((tr, key) => {
          if (!newKeys.has(key)) tr.remove();
        });

        // Keep detail panel updated if it is open.
        if (selectedKey) {
          const match = data.find((r) => r.key === selectedKey);
          if (match) openDetail(match);
        }
        syncPill.textContent = 'synced';
        renderUpdated('Connected');
      } catch (err) {
        // Only show error if we don't have stale data showing (optional, but safer to leave error logic simple)
        // For now, if error, we might want to clear or just show overlay.
        // Keeping simple: if completely failed and empty, show error.
        if (tbody.children.length === 0) {
            tbody.innerHTML = '<tr><td colspan="6" style="color: var(--red); text-align: center; padding: 24px;">⚠ Error loading data</td></tr>';
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

func (s *StatusServer) renderHTMLHeader(columns []int) string {
	var b strings.Builder
	for _, c := range columns {
		name := map[int]string{1: "St", 2: "Name", 3: "IP", 4: "RTT", 5: "Last Reply", 6: "Last Loss"}[c]
		fmt.Fprintf(&b, "<th>%s</th>", name)
	}
	return b.String()
}

func marshalColumns(cols []int) string {
	data, _ := json.Marshal(cols)
	return string(data)
}

func (s *StatusServer) filterAndSort(wrappers []PingWrapperInterface, view ServerView) []PingWrapperInterface {
	var filtered []PingWrapperInterface

	for _, wrapper := range wrappers {
		if view.Hidden[wrapper.Host()] {
			continue
		}

		stats := s.statsProvider(wrapper)
		isOnline := stats.state && stats.error_message == ""
		seen := stats.has_ever_received

		switch view.Filter {
		case FilterAll:
			filtered = append(filtered, wrapper)
		case FilterSmart:
			if isOnline || seen {
				filtered = append(filtered, wrapper)
			}
		case FilterOnline:
			if isOnline {
				filtered = append(filtered, wrapper)
			}
		case FilterOffline:
			if !isOnline {
				filtered = append(filtered, wrapper)
			}
		}
	}

	switch view.Sort {
	case SortByName:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := s.statsProvider(filtered[i])
			statsJ := s.statsProvider(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			nameI := statsI.GetHostRepr()
			nameJ := statsJ.GetHostRepr()
			if nameI == "" {
				nameI = filtered[i].Host()
			}
			if nameJ == "" {
				nameJ = filtered[j].Host()
			}
			return nameI < nameJ
		})
	case SortByStatus:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := s.statsProvider(filtered[i])
			statsJ := s.statsProvider(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			return filtered[i].Host() < filtered[j].Host()
		})
	case SortByRTT:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := s.statsProvider(filtered[i])
			statsJ := s.statsProvider(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			return statsI.lastrtt < statsJ.lastrtt
		})
	case SortByLastSeen:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := s.statsProvider(filtered[i])
			statsJ := s.statsProvider(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return !onlineI
			}
			if !onlineI && !onlineJ {
				if statsI.lastrecv == 0 && statsJ.lastrecv == 0 {
					return filtered[i].Host() < filtered[j].Host()
				}
				if statsI.lastrecv == 0 {
					return false
				}
				if statsJ.lastrecv == 0 {
					return true
				}
				return statsI.last_loss_nano > statsJ.last_loss_nano
			}
			hasLossI := statsI.last_loss_nano > 0
			hasLossJ := statsJ.last_loss_nano > 0
			if hasLossI != hasLossJ {
				return hasLossI
			}
			if hasLossI && hasLossJ {
				return statsI.last_loss_nano > statsJ.last_loss_nano
			}
			nameI := statsI.GetHostRepr()
			nameJ := statsJ.GetHostRepr()
			if nameI == "" {
				nameI = filtered[i].Host()
			}
			if nameJ == "" {
				nameJ = filtered[j].Host()
			}
			return nameI < nameJ
		})
	case SortByIP:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := s.statsProvider(filtered[i])
			statsJ := s.statsProvider(filtered[j])
			keyI := ipKey(statsI.iprepr)
			keyJ := ipKey(statsJ.iprepr)
			if keyI != nil && keyJ != nil && !bytes.Equal(keyI, keyJ) {
				return bytes.Compare(keyI, keyJ) < 0
			}
			if keyI != nil && keyJ == nil {
				return true
			}
			if keyI == nil && keyJ != nil {
				return false
			}
			return filtered[i].Host() < filtered[j].Host()
		})
	}

	return filtered
}
