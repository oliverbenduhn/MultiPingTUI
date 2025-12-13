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
}

func StartStatusServer(repo HostRepository, provider StatsProvider, initialView ServerView, port int) (*StatusServer, error) {
	if port <= 0 {
		return nil, nil
	}

	server := &StatusServer{
		repo:          repo,
		statsProvider: provider,
		view:          initialView,
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", server.htmlHandler)
	mux.HandleFunc("/text", server.textHandler)
	mux.HandleFunc("/json", server.jsonHandler)
	mux.HandleFunc("/view", server.viewHandler)
	mux.HandleFunc("/state", server.stateHandler)

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
    .rtt-bar .bar-filled {
      background: var(--green);
      animation: pulse 2s ease-in-out infinite;
    }
    .rtt-bar .bar-partial {
      background: var(--yellow);
      opacity: 0.6;
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

      let html = '<div class="rtt-bar">';
      for (let i = 0; i < bars; i++) {
        if (i < filledCount - 2) {
          html += '<span class="bar-filled"></span>';
        } else if (i < filledCount) {
          html += '<span class="bar-partial"></span>';
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
      selectedKey = row.key || null;
      detailTitle.textContent = row.host || 'Details';
      const statusText = row.online ? 'ONLINE' : 'OFFLINE';
      const rttText = row.online ? (row.rtt || '-') : '-';
      const lossText = row.last_loss_ago ? (row.last_loss_ago + ' (' + (row.last_loss_duration || '-') + ')') : '-';

      const parts = [
        ['Host', row.host || '-', ''],
        ['IP', row.ip || '-', 'mono'],
        ['Status', statusText, row.online ? '' : ''],
        ['RTT', rttText, 'mono'],
        ['Last Reply', row.last_reply || '-', ''],
        ['Last Loss', lossText, ''],
        ['Online Time', row.online_time || '-', ''],
      ];
      if (row.error) {
        parts.push(['Error', row.error, 'error']);
      }

      detailBody.innerHTML = parts.map(([label, value, cls]) => {
        const klass = cls ? ('detail-value ' + cls) : 'detail-value';
        return (
          '<div class="detail-row">' +
            '<div class="detail-label">' + escapeHtml(label) + '</div>' +
            '<div class="' + klass + '">' + escapeHtml(value) + '</div>' +
          '</div>'
        );
      }).join('');

      detailOverlay.classList.add('open');
      detailOverlay.setAttribute('aria-hidden', 'false');
    }

    function closeDetail() {
      detailOverlay.classList.remove('open');
      detailOverlay.setAttribute('aria-hidden', 'true');
      selectedKey = null;
    }

    detailClose.addEventListener('click', closeDetail);
    detailOverlay.addEventListener('click', (e) => {
      if (e.target === detailOverlay) closeDetail();
    });
    document.addEventListener('keydown', (e) => {
      if (e.key === 'Escape' && detailOverlay.classList.contains('open')) closeDetail();
    });

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
	        tbody.innerHTML = '';

        for (const row of data) {
          const tr = document.createElement('tr');
          if (!row.online) {
            tr.className = 'offline-row';
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

          columns.forEach((col) => {
            const val = colValues[col] ?? '-';
            const td = document.createElement('td');

            if (col === 1) {
              td.innerHTML = val;
            } else if (col === 2) {
              td.className = 'name-cell';
              td.textContent = val;
              td.dataset.key = row.key || '';
            } else if (col === 3) {
              td.className = 'ip-cell';
              td.textContent = val;
            } else if (col === 4 && row.online && val !== '-') {
              td.innerHTML = '<div class="rtt-cell"><span class="rtt-value">' + val + '</span>' + createRTTBar(parseRTT(val)) + '</div>';
            } else {
              td.textContent = val;
            }
            tr.appendChild(td);
          });
          tbody.appendChild(tr);
        }

        // Keep detail panel updated if it is open.
        if (selectedKey) {
          const match = data.find((r) => r.key === selectedKey);
          if (match) openDetail(match);
        }
        syncPill.textContent = 'synced';
        renderUpdated('Connected');
      } catch (err) {
        tbody.innerHTML = '<tr><td colspan="6" style="color: var(--red); text-align: center; padding: 24px;">⚠ Error loading data</td></tr>';
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
      const key = cell.dataset.key;
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
