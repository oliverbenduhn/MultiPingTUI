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
	Host             string `json:"host"`
	IP               string `json:"ip"`
	Online           bool   `json:"online"`
	RTT              string `json:"rtt"`
	LastReply        string `json:"last_reply"`
	LastLossAgo      string `json:"last_loss_ago,omitempty"`
	LastLossDuration string `json:"last_loss_duration,omitempty"`
	Error            string `json:"error,omitempty"`
}

type ServerView struct {
	Filter FilterMode
	Sort   SortMode
	Hidden map[string]bool
	Cols   []int
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
	mux.HandleFunc("/", server.textHandler)
	mux.HandleFunc("/json", server.jsonHandler)
	mux.HandleFunc("/live", server.htmlHandler)

	listener, err := net.Listen("tcp", fmt.Sprintf("0.0.0.0:%d", port))
	if err != nil {
		return nil, err
	}

	server.srv = &http.Server{
		Addr:              listener.Addr().String(),
		Handler:           mux,
		ReadHeaderTimeout: 2 * time.Second,
		// Very aggressive timeouts to prevent goroutine leaks
		IdleTimeout:       5 * time.Second,
		ReadTimeout:       3 * time.Second,
		WriteTimeout:      10 * time.Second,
		MaxHeaderBytes:    1 << 20, // 1 MB
	}
	// Disable keep-alives completely to prevent lingering connReader goroutines
	server.srv.SetKeepAlivesEnabled(false)

	go func() {
		err := server.srv.Serve(listener)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(os.Stderr, "status server error: %v\n", err)
		}
	}()

	fmt.Fprintf(os.Stderr, "Status server listening on http://%s (/: text, /json: JSON)\n", server.srv.Addr)

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
      table-layout: auto;
      min-width: 640px;
    }
    th, td {
      padding: 12px 16px;
      text-align: left;
      word-break: break-word;
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
    }
    #updated {
      margin-top: 16px;
      font-size: 12px;
      color: var(--text-muted);
      display: flex;
      align-items: center;
      gap: 8px;
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
  </style>
</head>
<body>
  <header>
    <h1>🌐 MultiPingTUI Live Status</h1>
    <p class="muted">Auto-refreshes every second · <code>/json</code> for JSON · <code>/</code> for text</p>
  </header>

  <div class="container">
    <table id="status">
      <thead>
        <tr>%s</tr>
      </thead>
      <tbody></tbody>
    </table>
  </div>

  <div id="updated">
    <span class="status-indicator"></span>
    <span>Loading…</span>
  </div>

  <script>
    const columns = %s;
    const columnNames = {1:'Status', 2:'Name', 3:'IP Address', 4:'RTT', 5:'Last Reply', 6:'Last Loss'};
    const tbody = document.querySelector('#status tbody');
    document.querySelector('#status thead tr').innerHTML = columns.map(c => '<th>' + columnNames[c] + '</th>').join('');
    const updatedEl = document.querySelector('#updated span:last-child');
    const REFRESH_MS = 1000;

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

    async function refresh() {
      try {
        const res = await fetch('/json', {cache:'no-store', headers:{'Cache-Control':'no-cache','Pragma':'no-cache'}});
        const data = await res.json();
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
        renderUpdated('Connected');
      } catch (err) {
        tbody.innerHTML = '<tr><td colspan="' + columns.length + '" style="color: var(--red); text-align: center; padding: 24px;">⚠ Error loading data</td></tr>';
        renderUpdated('Disconnected');
      }
    }

    refresh();
    setInterval(refresh, REFRESH_MS);
  </script>
</body>
</html>`, s.renderHTMLHeader(cols), marshalColumns(cols))
}

func (s *StatusServer) collectStatuses() []HostStatus {
	wrappers := s.repo.GetAll()
	view := s.snapshotView()
	filtered := s.filterAndSort(wrappers, view)
	statuses := make([]HostStatus, 0, len(filtered))
	now := time.Now()

	for _, wrapper := range filtered {
		stats := s.statsProvider(wrapper)

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

		var lastLossAgo, lastLossDuration string
		if stats.last_loss_nano > 0 {
			lastLossAgo = fmt.Sprintf("%s ago", time.Duration(now.UnixNano()-stats.last_loss_nano).Round(time.Second))
			lastLossDuration = time.Duration(stats.last_loss_duration).Round(time.Second / 10).String()
		}

		statuses = append(statuses, HostStatus{
			Host:             host,
			IP:               ip,
			Online:           online,
			RTT:              rtt,
			LastReply:        lastReply,
			LastLossAgo:      lastLossAgo,
			LastLossDuration: lastLossDuration,
			Error:            stats.error_message,
		})
	}

	return statuses
}

func (s *StatusServer) UpdateView(view ServerView) {
	s.viewMu.Lock()
	defer s.viewMu.Unlock()
	s.view = view
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
	cols := s.snapshotView().Cols
	if len(cols) == 0 {
		return []int{1, 2, 3, 4, 5, 6}
	}
	out := append([]int{}, cols...)
	sort.Ints(out)
	return out
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
