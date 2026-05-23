package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"runtime/debug"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FilterMode represents the current filter state
type FilterMode int

const (
	FilterAll FilterMode = iota
	FilterSmart
	FilterOnline
	FilterOffline
)

// SortMode represents the current sort state
type SortMode int

const (
	SortByName SortMode = iota
	SortByStatus
	SortByRTT
	SortByLastSeen
	SortByIP
)

// UpdateRate represents the refresh rate
type UpdateRate int

const (
	UpdateRate100ms UpdateRate = iota
	UpdateRate1s
	UpdateRate5s
	UpdateRate30s
)

// TUIModel is the bubbletea model for the TUI
type TUIModel struct {
	ps               *PingService
	repo             HostRepository
	header           HeaderModel
	footer           FooterModel
	hostList         HostListModel
	quitting         bool
	transitionWriter *TransitionWriter
	statusMessage    string
	statsCache       map[string]PWStats // cache stats per wrapper to avoid recalculation
	statsCacheMu     sync.RWMutex       // mutex for statsCache
	statsCacheTime   time.Time          // when stats were last calculated
	lastTickTime     time.Time          // when last tick happened
	statusServer     *StatusServer      // optional web status server

	detailHost        string
	detailTraceScroll int
	traceStates       map[string]*traceState

	viewMode tuiViewMode

	globalStats *GlobalStatistics

	hostsRaw []string
}

type tuiViewMode int

const (
	viewList tuiViewMode = iota
	viewDetails
	viewDashboard
)

type traceState struct {
	running    bool
	seq        int
	startedAt  time.Time
	finishedAt time.Time
	target     string
	output     string
	err        string
	took       time.Duration
}

type configEditedMsg struct {
	err error
}

func NewTUIModel(ps *PingService, repo HostRepository, tw *TransitionWriter, initialFilter FilterMode, globalStats *GlobalStatistics) *TUIModel {
	if initialFilter != FilterOnline && initialFilter != FilterOffline && initialFilter != FilterSmart {
		initialFilter = FilterSmart
	}

	hostList := NewHostListModel()
	hostList.filterMode = initialFilter

	return &TUIModel{
		ps:               ps,
		repo:             repo,
		header:           NewHeaderModel(),
		footer:           NewFooterModel(),
		hostList:         hostList,
		transitionWriter: tw,
		statsCache:       make(map[string]PWStats),
		statsCacheTime:   time.Time{},
		lastTickTime:     time.Now(),
		traceStates:      make(map[string]*traceState),
		viewMode:         viewList,
		globalStats:      globalStats,
		hostsRaw:         nil,
	}
}

// tickMsg is sent every 100ms to update the display
type tickMsg time.Time

// keyMap defines the keyboard shortcuts
type keyMap struct {
	Up          key.Binding
	Down        key.Binding
	PageUp      key.Binding
	PageDown    key.Binding
	Enter       key.Binding
	Traceroute  key.Binding
	Dashboard   key.Binding
	Quit        key.Binding
	FilterCycle key.Binding
	SortCycle   key.Binding
	Escape      key.Binding
	EditConfig  key.Binding
	HideHost    key.Binding
	ShowAll     key.Binding
	CycleRate   key.Binding
}

var keys = keyMap{
	Up: key.NewBinding(
		key.WithKeys("up", "k"),
		key.WithHelp("↑/k", "up"),
	),
	Down: key.NewBinding(
		key.WithKeys("down", "j"),
		key.WithHelp("↓/j", "down"),
	),
	PageUp: key.NewBinding(
		key.WithKeys("pgup"),
		key.WithHelp("pgup", "page up"),
	),
	PageDown: key.NewBinding(
		key.WithKeys("pgdown"),
		key.WithHelp("pgdown", "page down"),
	),
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "details"),
	),
	Traceroute: key.NewBinding(
		key.WithKeys("t"),
		key.WithHelp("t", "traceroute"),
	),
	Dashboard: key.NewBinding(
		key.WithKeys("d"),
		key.WithHelp("d", "dashboard"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	FilterCycle: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "cycle filter"),
	),
	SortCycle: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "cycle sort"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
	),
	EditConfig: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit config"),
	),
	HideHost: key.NewBinding(
		key.WithKeys("delete"),
		key.WithHelp("del", "hide host"),
	),
	ShowAll: key.NewBinding(
		key.WithKeys("insert"),
		key.WithHelp("ins", "show all"),
	),
	CycleRate: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "cycle update rate"),
	),
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#e5e7eb")).
			MarginLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#e5e7eb")).
			Background(lipgloss.Color("#1f2937")).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color("#3b82f6")).
			Bold(true)

	newOnlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#22d3ee")).
			Bold(true)

	onlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4ade80")).
			Bold(true)

	offlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#f87171")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ca3af")).
			MarginLeft(1)

	detailStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3b82f6")).
			Padding(1, 2).
			MarginLeft(2)

	accentStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#eab308")).
			Bold(true)

	separatorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#4b5563"))
)

func (m *TUIModel) Init() tea.Cmd {
	// Don't block in Init() - let first View() happen quickly
	// Cache will be filled by first tick
	return tea.Batch(
		m.tickCmd(),
		tea.EnterAltScreen,
	)
}

// tickCmd returns a command that ticks every 100ms for UI updates
func (m *TUIModel) tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *TUIModel) getTickDuration() time.Duration {
	switch m.header.updateRate {
	case UpdateRate100ms:
		return 100 * time.Millisecond
	case UpdateRate1s:
		return 1 * time.Second
	case UpdateRate5s:
		return 5 * time.Second
	case UpdateRate30s:
		return 30 * time.Second
	default:
		return 100 * time.Millisecond
	}
}

// updateStatsCache updates the cached stats for all wrappers
// This is called once per tick to avoid recalculating stats multiple times per frame
func (m *TUIModel) updateStatsCache() {
	m.statsCacheMu.Lock()
	defer m.statsCacheMu.Unlock()
	now := time.Now()
	m.statsCacheTime = now

	for _, wrapper := range m.repo.GetAll() {
		stats := wrapper.CalcStats(TimeoutThresholdNS)

		// Detect state transitions for dashboard
		if old, ok := m.statsCache[wrapper.Host()]; ok {
			// Basic state change
			// Note: PWStats.ComputeState handles its own internal transitions logic, but
			// we want to capture it here for the UI event log.
			if old.state != stats.state {
				// Only record if it wasn't just initialized (avoid spam on startup)
				if old.has_ever_received || old.state_initialized {
					evt := TransitionEvent{
						Host:  wrapper.Host(),
						IP:    stats.iprepr,
						State: stats.state,
						When:  now,
					}
					if stats.state {
						// Came UP. Duration was how long it was DOWN (approx)
						// stats.last_loss_duration is computed in PWStats upon recovery
						evt.Duration = time.Duration(stats.last_loss_duration)
					} else {
						// Went DOWN. Duration was how long it was UP.
						// stats.last_up_transition tells us when it last came up.
						if stats.last_up_transition > 0 {
							upTime := now.UnixNano() - stats.last_up_transition
							if upTime > 0 {
								evt.Duration = time.Duration(upTime)
							}
						}
					}

					m.globalStats.AddTransition(evt)
				}
			}
		}

		m.statsCache[wrapper.Host()] = stats
	}
}

// getCachedStats returns cached stats for a wrapper
func (m *TUIModel) getCachedStats(wrapper PingWrapperInterface) PWStats {
	m.statsCacheMu.RLock()
	defer m.statsCacheMu.RUnlock()
	if stats, ok := m.statsCache[wrapper.Host()]; ok {
		return stats
	}
	// Cache miss - return empty stats instead of calling CalcStats()
	// This prevents blocking on first View() before cache is filled
	return PWStats{
		hrepr:  wrapper.Host(),
		iprepr: wrapper.Host(),
		state:  false,
	}
}

func (m *TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Keep TUI view synchronized with the web live view (when enabled).
	m.syncViewFromStatusServer()

	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.header.width = msg.Width
		m.footer.width = msg.Width
		m.hostList.width = msg.Width
		m.hostList.height = msg.Height - 5 // Adjust for header/footer
		return m, nil

	case tickMsg:
		now := time.Now()
		elapsed := now.Sub(m.lastTickTime)
		interval := m.getTickDuration()

		// Check if it's time for a stats update
		if elapsed >= interval {
			// Update stats cache for all wrappers
			m.updateStatsCache()
			m.lastTickTime = now
			m.hostList.cacheInvalidated = true
		}

		// Update countdown in header
		m.header.countdown = m.getRemainingTime()

		// Always continue UI ticker at 100ms
		return m, m.tickCmd()

	case tracerouteResult:
		state := m.getOrCreateTraceState(msg.host)
		if msg.seq != state.seq {
			// Stale result from a previous run.
			return m, nil
		}

		state.running = false
		state.finishedAt = time.Now()
		state.took = msg.took
		state.target = msg.target
		state.output = strings.TrimRight(msg.output, "\n")
		if msg.err != nil {
			state.err = msg.err.Error()
		} else {
			state.err = ""
		}
		return m, nil

	case configEditedMsg:
		settings, err := LoadUserSettings()
		if err != nil {
			m.statusMessage = fmt.Sprintf("Failed to reload config: %v", err)
			return m, nil
		}
		applyUserSettingsToModel(m, settings)

		// Update hosts if they changed.
		if !sameStringSlice(settings.Hosts, m.hostsRaw) {
			hosts, err := parseHostsInput(strings.Join(settings.Hosts, "\n"))
			if err != nil {
				m.statusMessage = fmt.Sprintf("Failed to reload hosts: %v", err)
				return m, nil
			}
			m.hostsRaw = append([]string{}, settings.Hosts...)
			m.ps.ReplaceHosts(hosts)
			m.hostList.cursor = -1
			m.hostList.scrollOffset = 0
			m.hostList.cacheInvalidated = true
		}
		if msg.err != nil {
			m.statusMessage = fmt.Sprintf("Config edited (with error): %v", msg.err)
		} else {
			m.statusMessage = "Config reloaded"
		}
		m.pushStatusView()
		return m, nil

	case tea.KeyMsg:
		switch {
		case key.Matches(msg, keys.Quit):
			m.quitting = true
			m.ps.Stop()
			return m, tea.Quit

		case key.Matches(msg, keys.Escape):
			if m.viewMode != viewList {
				m.setViewMode(viewList)
			}
			return m, nil

		case key.Matches(msg, keys.Enter):
			if m.viewMode == viewList && m.hostList.cursor >= 0 {
				m.setViewMode(viewDetails)
				if wrapper, ok := m.selectedWrapper(); ok {
					m.setDetailHost(wrapper.Host())
				}
				return m, nil
			}
			if m.viewMode != viewList {
				m.setViewMode(viewList)
			}
			return m, nil

		case key.Matches(msg, keys.Dashboard):
			if m.viewMode == viewDashboard {
				m.setViewMode(viewList)
			} else {
				m.setViewMode(viewDashboard)
			}
			return m, nil

		case key.Matches(msg, keys.Traceroute):
			if m.viewMode != viewDetails {
				return m, nil
			}
			wrapper, ok := m.selectedWrapper()
			if !ok {
				return m, nil
			}
			stats := m.getCachedStats(wrapper)
			target := strings.TrimSpace(stats.iprepr)
			if target == "" {
				target = strings.TrimSpace(wrapper.Host())
			}

			host := wrapper.Host()
			m.setDetailHost(host)
			state := m.getOrCreateTraceState(host)
			state.seq++
			state.running = true
			state.startedAt = time.Now()
			state.finishedAt = time.Time{}
			state.target = target
			state.output = ""
			state.err = ""
			m.detailTraceScroll = 0

			seq := state.seq
			timeout := 20 * time.Second
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(context.Background(), timeout)
				defer cancel()
				return runTraceroute(ctx, host, target, seq)
			}

		case key.Matches(msg, keys.Up):
			filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
			if len(filtered) > 0 {
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else if m.hostList.cursor > 0 {
					m.hostList.cursor--
				}
				m.hostList.adjustScroll()
				if m.viewMode == viewDetails && m.hostList.cursor >= 0 && m.hostList.cursor < len(filtered) {
					m.setDetailHost(filtered[m.hostList.cursor].Host())
				}
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
			if len(filtered) > 0 {
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else if m.hostList.cursor < len(filtered)-1 {
					m.hostList.cursor++
				}
				m.hostList.adjustScroll()
				if m.viewMode == viewDetails && m.hostList.cursor >= 0 && m.hostList.cursor < len(filtered) {
					m.setDetailHost(filtered[m.hostList.cursor].Host())
				}
			}
			return m, nil

		case key.Matches(msg, keys.PageUp):
			if m.viewMode == viewDetails {
				m.scrollTrace(-1)
				return m, nil
			}
			filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
			if len(filtered) > 0 {
				visibleLines := m.hostList.height - 7
				if visibleLines < 1 {
					visibleLines = 1
				}
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else {
					m.hostList.cursor -= visibleLines
					if m.hostList.cursor < 0 {
						m.hostList.cursor = 0
					}
				}
				m.hostList.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.PageDown):
			if m.viewMode == viewDetails {
				m.scrollTrace(1)
				return m, nil
			}
			filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
			if len(filtered) > 0 {
				visibleLines := m.hostList.height - 7
				if visibleLines < 1 {
					visibleLines = 1
				}
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else {
					m.hostList.cursor += visibleLines
					if m.hostList.cursor >= len(filtered) {
						m.hostList.cursor = len(filtered) - 1
					}
				}
				m.hostList.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.FilterCycle):
			m.hostList.filterMode = nextFilterMode(m.hostList.filterMode)
			m.header.filterMode = m.hostList.filterMode
			m.hostList.cursor = -1
			m.hostList.scrollOffset = 0
			m.hostList.cacheInvalidated = true
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.SortCycle):
			m.hostList.sortMode = nextSortMode(m.hostList.sortMode)
			m.header.sortMode = m.hostList.sortMode
			m.hostList.cacheInvalidated = true
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.CycleRate):
			m.header.updateRate = nextUpdateRate(m.header.updateRate)
			m.statusMessage = fmt.Sprintf("Update rate: %s", m.header.getUpdateRateString())
			// No need to restart any tickers - the time-based calculation handles everything
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.HideHost):
			if m.viewMode == viewList && m.hostList.cursor >= 0 {
				filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
				if m.hostList.cursor < len(filtered) {
					hostToHide := filtered[m.hostList.cursor].Host()
					m.hostList.hiddenHosts[hostToHide] = true
					m.statusMessage = fmt.Sprintf("Hidden: %s (press INS to show all)", hostToHide)
					// Move cursor to next visible item or previous if at end
					if m.hostList.cursor >= len(filtered)-1 && m.hostList.cursor > 0 {
						m.hostList.cursor--
					}
					m.hostList.adjustScroll()
					m.hostList.cacheInvalidated = true
					m.pushStatusView()
				}
			}
			return m, nil

		case key.Matches(msg, keys.ShowAll):
			if len(m.hostList.hiddenHosts) > 0 {
				count := len(m.hostList.hiddenHosts)
				m.hostList.hiddenHosts = make(map[string]bool)
				m.statusMessage = fmt.Sprintf("Showing all hosts (%d unhidden)", count)
			} else {
				m.statusMessage = "No hidden hosts"
			}
			m.hostList.cacheInvalidated = true
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.EditConfig):
			path, err := userSettingsPath()
			if err != nil {
				m.statusMessage = fmt.Sprintf("Failed to locate config: %v", err)
				return m, nil
			}
			// Ensure config exists so the editor has something to open.
			_ = ensureConfigFile(path)

			exe, err := os.Executable()
			if err != nil {
				m.statusMessage = fmt.Sprintf("Failed to locate executable: %v", err)
				return m, nil
			}
			cmd := exec.Command(exe, "-edit-config")
			cmd.Env = append(os.Environ(), "MPING_CONFIG="+path)
			m.statusMessage = fmt.Sprintf("Editing config: %s (Ctrl+S save, Esc close)", path)
			return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
				return configEditedMsg{err: err}
			})

		default:
			// Handle number keys 1-6 for column toggling
			if len(msg.String()) == 1 && msg.String() >= "1" && msg.String() <= "6" {
				if m.viewMode != viewList {
					return m, nil
				}
				colNum := int(msg.String()[0] - '0')
				m.hostList.visibleColumns[colNum] = !m.hostList.visibleColumns[colNum]
				colName := m.hostList.getColumnName(colNum)
				if m.hostList.visibleColumns[colNum] {
					m.statusMessage = fmt.Sprintf("Column %d (%s) shown", colNum, colName)
				} else {
					m.statusMessage = fmt.Sprintf("Column %d (%s) hidden", colNum, colName)
				}
				m.pushStatusView()
				return m, nil
			}
		}
	}

	return m, nil
}

func sameStringSlice(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func (m *TUIModel) setViewMode(mode tuiViewMode) {
	m.viewMode = mode
	switch mode {
	case viewDetails:
		m.footer.mode = FooterDetails
	case viewDashboard:
		m.footer.mode = FooterDashboard
	default:
		m.footer.mode = FooterList
	}
}

func (m *TUIModel) selectedWrapper() (PingWrapperInterface, bool) {
	filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)
	if m.hostList.cursor < 0 || m.hostList.cursor >= len(filtered) {
		return nil, false
	}
	return filtered[m.hostList.cursor], true
}

func (m *TUIModel) setDetailHost(host string) {
	if m.detailHost == host {
		return
	}
	m.detailHost = host
	m.detailTraceScroll = 0
}

func (m *TUIModel) getOrCreateTraceState(host string) *traceState {
	if m.traceStates == nil {
		m.traceStates = make(map[string]*traceState)
	}
	if st, ok := m.traceStates[host]; ok {
		return st
	}
	st := &traceState{}
	m.traceStates[host] = st
	return st
}

func (m *TUIModel) scrollTrace(direction int) {
	wrapper, ok := m.selectedWrapper()
	if !ok {
		return
	}
	host := wrapper.Host()
	m.setDetailHost(host)

	st, ok := m.traceStates[host]
	if !ok || st.output == "" {
		return
	}

	lines := strings.Split(st.output, "\n")
	visible := m.detailTraceVisibleLines()
	if len(lines) <= visible {
		m.detailTraceScroll = 0
		return
	}

	maxScroll := len(lines) - visible
	page := visible - 1
	if page < 1 {
		page = 1
	}
	next := m.detailTraceScroll + direction*page
	if next < 0 {
		next = 0
	}
	if next > maxScroll {
		next = maxScroll
	}
	m.detailTraceScroll = next
}

func (m *TUIModel) detailTraceVisibleLines() int {
	// Roughly: window height minus header/footer + details header lines.
	visible := m.hostList.height - 10
	if visible < 5 {
		visible = 5
	}
	return visible
}

func (m *TUIModel) syncViewFromStatusServer() {
	if m.statusServer == nil {
		return
	}

	view := m.statusServer.View()
	changed := false

	if view.Filter != m.hostList.filterMode {
		m.hostList.filterMode = view.Filter
		m.header.filterMode = view.Filter
		m.hostList.cursor = -1
		m.hostList.scrollOffset = 0
		m.hostList.cacheInvalidated = true
		changed = true
	}

	if view.Sort != m.hostList.sortMode {
		m.hostList.sortMode = view.Sort
		m.header.sortMode = view.Sort
		m.hostList.cacheInvalidated = true
		changed = true
	}

	if view.Rate != m.header.updateRate && view.Rate >= UpdateRate100ms && view.Rate <= UpdateRate30s {
		m.header.updateRate = view.Rate
		m.statusMessage = fmt.Sprintf("Update rate: %s", m.header.getUpdateRateString())
		changed = true
	}

	if view.Hidden != nil {
		if !sameHiddenHosts(m.hostList.hiddenHosts, view.Hidden) {
			m.hostList.hiddenHosts = cloneHiddenHosts(view.Hidden)
			m.hostList.cacheInvalidated = true
			changed = true
		}
	}

	if len(view.Cols) == 0 {
		for i := 1; i <= 6; i++ {
			if !m.hostList.visibleColumns[i] {
				changed = true
			}
			m.hostList.visibleColumns[i] = true
		}
	} else {
		want := make(map[int]bool, 6)
		for _, c := range view.Cols {
			if c >= 1 && c <= 6 {
				want[c] = true
			}
		}
		for i := 1; i <= 6; i++ {
			if m.hostList.visibleColumns[i] != want[i] {
				changed = true
			}
			m.hostList.visibleColumns[i] = want[i]
		}
	}

	if changed {
		m.hostList.cacheInvalidated = true
	}
}

func sameHiddenHosts(a, b map[string]bool) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}

func (m *TUIModel) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	var s strings.Builder

	// Header
	s.WriteString(m.header.View())

	if m.statusMessage != "" {
		s.WriteString(helpStyle.Render(m.statusMessage))
		s.WriteString("\n\n")
	}

	// Get filtered and sorted wrappers
	filtered := m.hostList.getFilteredWrappers(m.repo.GetAll(), m.getCachedStats)

	switch m.viewMode {
	case viewDashboard:
		s.WriteString(m.renderDashboardView())
	case viewDetails:
		if m.hostList.cursor >= 0 && m.hostList.cursor < len(filtered) {
			s.WriteString(m.renderDetailView(filtered[m.hostList.cursor]))
		} else {
			s.WriteString(m.hostList.renderListView(filtered, m.getCachedStats))
		}
	default:
		s.WriteString(m.hostList.renderListView(filtered, m.getCachedStats))
	}

	// Footer
	s.WriteString(m.footer.View())

	return s.String()
}

func (m *TUIModel) renderDetailView(wrapper PingWrapperInterface) string {
	stats := m.getCachedStats(wrapper)
	isOnline := stats.state && stats.error_message == ""

	var details strings.Builder
	details.WriteString(fmt.Sprintf("Host: %s\n", wrapper.Host()))
	details.WriteString(fmt.Sprintf("IP: %s\n\n", stats.iprepr))

	if isOnline {
		details.WriteString(onlineStyle.Render("Status: ONLINE ✓"))
		details.WriteString("\n\n")
		details.WriteString(accentStyle.Render(fmt.Sprintf("Last RTT: %s\n", stats.lastrtt_as_string)))
		details.WriteString(accentStyle.Render(fmt.Sprintf("Last Received: %s ago\n", time.Duration(stats.last_seen_nano).Round(time.Millisecond))))
		if stats.last_loss_nano > 0 {
			details.WriteString("\n")
			details.WriteString(fmt.Sprintf("Last Loss: %s\n", time.Unix(0, stats.last_loss_nano).Format("2006-01-02 15:04:05")))
			details.WriteString(fmt.Sprintf("Loss Duration: %s\n", time.Duration(stats.last_loss_duration).Round(time.Second)))
		}
	} else {
		details.WriteString(offlineStyle.Render("Status: OFFLINE ✗"))
		details.WriteString("\n\n")
		if stats.error_message != "" {
			details.WriteString(fmt.Sprintf("Error: %s\n", stats.error_message))
		}
		if stats.lastrecv == 0 {
			details.WriteString("Never received a reply\n")
		} else {
			details.WriteString(fmt.Sprintf("Last seen: %s ago\n", time.Duration(stats.last_seen_nano).Round(time.Second)))
		}
	}

	details.WriteString(fmt.Sprintf("\nOnline time: %s\n", stats.OnlineUptime(time.Now().UnixNano()).Round(time.Second)))

	host := wrapper.Host()

	details.WriteString("\n")
	details.WriteString(accentStyle.Render("Traceroute"))
	details.WriteString("\n")

	st, ok := m.traceStates[host]
	switch {
	case ok && st.running:
		details.WriteString(helpStyle.Render(fmt.Sprintf("running to %s… (%s elapsed)  │  press t to restart", st.target, time.Since(st.startedAt).Round(time.Second))))
		details.WriteString("\n")
	case ok && st.err != "" && st.output == "":
		details.WriteString(helpStyle.Render(fmt.Sprintf("failed to run traceroute to %s: %s", st.target, st.err)))
		details.WriteString("\n")
	case ok && st.output != "":
		if st.err != "" {
			details.WriteString(helpStyle.Render(fmt.Sprintf("target %s  │  finished in %s  │  error: %s", st.target, st.took.Round(time.Second/10), st.err)))
		} else {
			details.WriteString(helpStyle.Render(fmt.Sprintf("target %s  │  finished in %s", st.target, st.took.Round(time.Second/10))))
		}
		details.WriteString("\n\n")

		lines := strings.Split(st.output, "\n")
		visible := m.detailTraceVisibleLines()
		scroll := 0
		if m.detailHost == host {
			scroll = m.detailTraceScroll
			if scroll < 0 {
				scroll = 0
			}
			if scroll > len(lines) {
				scroll = 0
			}
		}
		end := scroll + visible
		if end > len(lines) {
			end = len(lines)
		}
		details.WriteString(strings.Join(lines[scroll:end], "\n"))
		details.WriteString("\n")
		if len(lines) > visible {
			details.WriteString(helpStyle.Render(fmt.Sprintf("[lines %d-%d/%d] pgup/pgdn to scroll", scroll+1, end, len(lines))))
			details.WriteString("\n")
		}
	default:
		details.WriteString(helpStyle.Render("press t to run traceroute"))
		details.WriteString("\n")
	}

	return detailStyle.Render(details.String())
}

func (m *TUIModel) renderDashboardView() string {
	type entry struct {
		host     string
		ip       string
		rtt      string
		ago      string
		err      string
		seen     bool
		online   bool
		lossNano int64
		rttVal   time.Duration
	}

	all := m.repo.GetAll()
	hidden := 0
	visibleWrappers := make([]PingWrapperInterface, 0, len(all))
	for _, w := range all {
		if m.hostList.hiddenHosts[w.Host()] {
			hidden++
			continue
		}
		visibleWrappers = append(visibleWrappers, w)
	}

	filtered := m.hostList.getFilteredWrappers(all, m.getCachedStats)

	countOnline := 0
	countOffline := 0
	countNever := 0
	var totalRTT time.Duration
	rttCount := 0

	offlineEntries := make([]entry, 0, len(visibleWrappers))
	hotRTT := make([]entry, 0, len(visibleWrappers))

	// RTT Distribution Buckets
	buckets := []struct {
		label string
		count int
		max   time.Duration // exclusive upper bound
	}{
		{"<5ms", 0, 5 * time.Millisecond},
		{"5-20ms", 0, 20 * time.Millisecond},
		{"20-50ms", 0, 50 * time.Millisecond},
		{"50-100ms", 0, 100 * time.Millisecond},
		{">100ms", 0, 100 * time.Hour}, // catch-all
	}

	for _, w := range visibleWrappers {
		st := m.getCachedStats(w)
		online := st.state && st.error_message == ""
		seen := st.has_ever_received

		if online {
			countOnline++
			if st.lastrtt > 0 {
				totalRTT += st.lastrtt
				rttCount++
				// Bucket logic
				for i := range buckets {
					if st.lastrtt < buckets[i].max {
						buckets[i].count++
						break
					}
				}
			}
		} else {
			countOffline++
		}
		if !seen {
			countNever++
		}

		ip := st.iprepr
		if ip == "" {
			ip = "-"
		}

		var ago string
		if st.lastrecv > 0 {
			ago = time.Duration(st.last_seen_nano).Round(time.Second).String() + " ago"
		} else {
			ago = "never"
		}

		e := entry{
			host:     w.Host(),
			ip:       ip,
			rtt:      st.lastrtt_as_string,
			ago:      ago,
			err:      st.error_message,
			seen:     seen,
			online:   online,
			lossNano: st.last_loss_nano,
			rttVal:   st.lastrtt,
		}

		if !online {
			offlineEntries = append(offlineEntries, e)
		} else if st.lastrtt > 0 {
			hotRTT = append(hotRTT, e)
		}
	}

	sort.SliceStable(offlineEntries, func(i, j int) bool {
		li, lj := offlineEntries[i].lossNano, offlineEntries[j].lossNano
		if li != lj {
			return li > lj
		}
		if offlineEntries[i].seen != offlineEntries[j].seen {
			return offlineEntries[i].seen
		}
		return offlineEntries[i].host < offlineEntries[j].host
	})

	sort.SliceStable(hotRTT, func(i, j int) bool {
		if hotRTT[i].rttVal != hotRTT[j].rttVal {
			return hotRTT[i].rttVal > hotRTT[j].rttVal
		}
		return hotRTT[i].host < hotRTT[j].host
	})

	var b strings.Builder
	b.WriteString(accentStyle.Render("Dashboard"))
	b.WriteString("\n")
	b.WriteString(strings.Repeat("═", 60))
	b.WriteString("\n\n")

	// Network Health Bar
	total := len(visibleWrappers)
	pct := 0.0
	if total > 0 {
		pct = (float64(countOnline) / float64(total)) * 100
	}
	barWidth := 30
	filled := 0
	if total > 0 {
		filled = int((float64(countOnline) / float64(total)) * float64(barWidth))
	}
	bar := "[" + strings.Repeat("█", filled) + strings.Repeat("░", barWidth-filled) + "]"

	healthColor := offlineStyle
	if pct > 90 {
		healthColor = onlineStyle
	} else if pct > 50 {
		healthColor = accentStyle
	}

	b.WriteString(fmt.Sprintf("Network Health: %s %s", healthColor.Render(bar), healthColor.Render(fmt.Sprintf("%.1f%% (%d/%d)", pct, countOnline, total))))
	b.WriteString("\n\n")

	// Global Stats
	uptime := time.Since(m.globalStats.GetStartTime()).Round(time.Second)
	avgRTT := "N/A"
	if rttCount > 0 {
		avgRTT = (totalRTT / time.Duration(rttCount)).Round(time.Microsecond).String()
	}

	b.WriteString(fmt.Sprintf("Total: %-5d │ Online: %-4d │ Offline: %-4d │ Never seen: %d\n", total, countOnline, countOffline, countNever))
	b.WriteString(fmt.Sprintf("Last tick: %s │ Uptime: %-8s │ Avg RTT: %s", time.Since(m.lastTickTime).Round(time.Second/10), uptime, avgRTT))
	b.WriteString("\n\n")

	b.WriteString(helpStyle.Render(fmt.Sprintf("Current view (after filter): %d hosts", len(filtered))))
	b.WriteString("\n\n")

	// Recent Transitions
	recentTransitions := m.globalStats.GetTransitions(5)
	if len(recentTransitions) > 0 {
		b.WriteString(accentStyle.Render("Recent Transitions (last 5)"))
		b.WriteString("\n")
		for _, evt := range recentTransitions {
			timeStr := evt.When.Format("15:04:05")
			if evt.State {
				// Came UP
				durStr := ""
				if evt.Duration > 0 {
					durStr = fmt.Sprintf(" (down for %s)", evt.Duration.Round(time.Second))
				}
				b.WriteString(fmt.Sprintf("  %s %s up   at %s%s\n", onlineStyle.Render("⬆"), evt.Host, timeStr, durStr))
			} else {
				// Went DOWN
				durStr := ""
				if evt.Duration > 0 {
					durStr = fmt.Sprintf(" (was up for %s)", evt.Duration.Round(time.Second))
				}
				b.WriteString(fmt.Sprintf("  %s %s down at %s%s\n", offlineStyle.Render("⬇"), evt.Host, timeStr, durStr))
			}
		}
		b.WriteString("\n")
	}

	// Offline Top 5
	if len(offlineEntries) > 0 {
		b.WriteString(offlineStyle.Render("Offline (top 5 by last seen)"))
		b.WriteString("\n")
		limit := 5
		if len(offlineEntries) < limit {
			limit = len(offlineEntries)
		}
		for i := 0; i < limit; i++ {
			e := offlineEntries[i]
			line := fmt.Sprintf("  - %-15s last seen: %s", e.host, e.ago)
			if e.err != "" {
				// truncate error if too long
				errShort := e.err
				if len(errShort) > 30 {
					errShort = errShort[:27] + "..."
				}
				line += fmt.Sprintf(" (err: %s)", errShort)
			}
			b.WriteString(line)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	// Highest RTT Top 5
	if len(hotRTT) > 0 {
		b.WriteString(accentStyle.Render("Highest RTT (top 5)"))
		b.WriteString("\n")
		limit := 5
		if len(hotRTT) < limit {
			limit = len(hotRTT)
		}
		for i := 0; i < limit; i++ {
			e := hotRTT[i]
			rtt := e.rtt
			if rtt == "" {
				rtt = "-"
			}
			// Calc deviation from avg?
			b.WriteString(fmt.Sprintf("  - %-15s rtt: %s\n", e.host, rtt))
		}
		b.WriteString("\n")
	}

	// RTT Distribution
	if countOnline > 0 {
		b.WriteString(accentStyle.Render("Response Time Distribution"))
		b.WriteString("\n")
		var distParts []string
		for _, bucket := range buckets {
			if bucket.count > 0 {
				distParts = append(distParts, fmt.Sprintf("%s: %d", bucket.label, bucket.count))
			}
		}
		b.WriteString("  " + strings.Join(distParts, " │ "))
		b.WriteString("\n")
	}

	return detailStyle.Render(strings.TrimRight(b.String(), "\n"))
}

func (m *TUIModel) pushStatusView() {
	if m.statusServer == nil {
		return
	}
	m.statusServer.UpdateView(ServerView{
		Filter: m.hostList.filterMode,
		Sort:   m.hostList.sortMode,
		Rate:   m.header.updateRate,
		Hidden: cloneHiddenHosts(m.hostList.hiddenHosts),
		Cols:   visibleColumnsList(m.hostList.visibleColumns),
	})
}

// getRemainingTime returns a countdown string for 5s and 30s rates
func (m *TUIModel) getRemainingTime() string {
	// Only show countdown for 5s and 30s rates
	if m.header.updateRate != UpdateRate5s && m.header.updateRate != UpdateRate30s {
		return ""
	}

	elapsed := time.Since(m.lastTickTime)
	duration := m.getTickDuration()
	remaining := duration - elapsed

	// If remaining is negative or zero, show full duration
	// This prevents showing "0s" and makes the countdown more intuitive
	if remaining <= 0 {
		remaining = duration
	}

	// Return countdown in seconds (rounded up so we never show 0)
	seconds := int(remaining.Seconds())
	if seconds <= 0 {
		seconds = int(duration.Seconds())
	}
	return fmt.Sprintf("(%ds)", seconds)
}

// RunTUI starts the TUI interface with an initial filter mode applied
func RunTUI(ps *PingService, repo HostRepository, tw *TransitionWriter, initialFilter FilterMode, webPort int, globalStats *GlobalStatistics, initialSettings UserSettings, initialHostsRaw []string) (finalErr error) {
	// Early panic protection before any terminal manipulation
	defer func() {
		if r := recover(); r != nil {
			// Ensure terminal is restored
			fmt.Print("\033[?25h")     // Show cursor
			fmt.Print("\033[2J\033[H") // Clear screen
			fmt.Print("\033[?1049l")   // Exit alt screen
			finalErr = fmt.Errorf("panic in TUI: %v\n%s", r, debug.Stack())
			fmt.Fprintf(os.Stderr, "PANIC in TUI:\n%v\n%s\n", r, debug.Stack())
		}
	}()

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Starting wrapper initialization (this may take a moment for large subnets)...\n")
	}

	// Start wrappers BEFORE entering alt screen with timeout protection
	startDone := make(chan bool, 1)
	startErr := make(chan error, 1)

	// Setup signal handling for Ctrl+C during startup
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, os.Interrupt)
	defer signal.Stop(sigChan)

	go func() {
		defer func() {
			if r := recover(); r != nil {
				fmt.Fprintf(os.Stderr, "PANIC during wrapper start: %v\n", r)
				startErr <- fmt.Errorf("panic: %v", r)
				return
			}
			startDone <- true
		}()
		ps.Start()
	}()

	// Wait for startup with timeout and interrupt support
	select {
	case <-startDone:
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG: All wrappers started, launching TUI...\n")
		}
	case err := <-startErr:
		return fmt.Errorf("error starting wrappers: %w", err)
	case <-sigChan:
		fmt.Fprintf(os.Stderr, "\nInterrupted during startup, cleaning up...\n")
		ps.Stop()
		return fmt.Errorf("interrupted by user")
	case <-time.After(60 * time.Second):
		ps.Stop()
		return fmt.Errorf("timeout waiting for wrappers to start (60s)")
	}

	model := NewTUIModel(ps, repo, tw, initialFilter, globalStats)
	model.hostsRaw = append([]string{}, initialHostsRaw...)
	applyUserSettingsToModel(model, initialSettings)
	var statusServer *StatusServer
	if webPort > 0 {
		initialView := ServerView{
			Filter: model.hostList.filterMode,
			Sort:   model.hostList.sortMode,
			Rate:   model.header.updateRate,
			Hidden: cloneHiddenHosts(model.hostList.hiddenHosts),
			Cols:   visibleColumnsList(model.hostList.visibleColumns),
		}
		var err error
		statusServer, err = StartStatusServer(repo, model.getCachedStats, initialView, webPort, globalStats)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start status server on port %d: %v\n", webPort, err)
		} else {
			model.statusServer = statusServer
		}
	}

	defer ps.Stop()
	if statusServer != nil {
		defer statusServer.Stop()
	}

	p := tea.NewProgram(
		model,
		tea.WithAltScreen(),
	)

	// Additional panic protection for bubbletea Run
	defer func() {
		if r := recover(); r != nil {
			_ = p.ReleaseTerminal()
			// Explicit terminal cleanup
			fmt.Print("\033[?25h")     // Show cursor
			fmt.Print("\033[2J\033[H") // Clear screen
			fmt.Fprintf(os.Stderr, "PANIC in bubbletea.Run:\n%v\n%s\n", r, debug.Stack())
			finalErr = fmt.Errorf("panic in bubbletea: %v", r)
		}
	}()

	finalModel, runErr := p.Run()
	if tm, ok := finalModel.(*TUIModel); ok {
		_ = SaveUserSettings(userSettingsFromModel(tm))
	}
	return runErr
}

func applyUserSettingsToModel(m *TUIModel, settings UserSettings) {
	if validFilterMode(settings.View.Filter) {
		if m.hostList.filterMode != settings.View.Filter {
			m.hostList.filterMode = settings.View.Filter
			m.header.filterMode = settings.View.Filter
			m.hostList.cursor = -1
			m.hostList.scrollOffset = 0
		}
	}
	if validSortMode(settings.View.Sort) {
		m.hostList.sortMode = settings.View.Sort
		m.header.sortMode = settings.View.Sort
	}
	if validUpdateRate(settings.View.Rate) {
		m.header.updateRate = settings.View.Rate
	}

	// Hidden: nil means "no hidden hosts".
	if settings.View.Hidden == nil {
		m.hostList.hiddenHosts = make(map[string]bool)
	} else {
		m.hostList.hiddenHosts = cloneHiddenHosts(settings.View.Hidden)
	}

	// Cols: nil/empty means "all visible".
	for i := 1; i <= 6; i++ {
		m.hostList.visibleColumns[i] = false
	}
	if len(settings.View.Cols) == 0 {
		for i := 1; i <= 6; i++ {
			m.hostList.visibleColumns[i] = true
		}
	} else {
		for _, c := range settings.View.Cols {
			if c >= 1 && c <= 6 {
				m.hostList.visibleColumns[c] = true
			}
		}
	}
	m.hostList.cacheInvalidated = true
}

func userSettingsFromModel(m *TUIModel) UserSettings {
	var out UserSettings
	if len(m.hostsRaw) > 0 {
		out.Hosts = append([]string{}, m.hostsRaw...)
	} else {
		// No reliable raw host list available; keep empty instead of serializing display strings.
		out.Hosts = []string{}
	}
	out.View = UserViewSettings{
		Filter: m.hostList.filterMode,
		Sort:   m.hostList.sortMode,
		Rate:   m.header.updateRate,
		Cols:   visibleColumnsList(m.hostList.visibleColumns),
		Hidden: cloneHiddenHosts(m.hostList.hiddenHosts),
	}
	return out
}
