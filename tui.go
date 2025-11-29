package main

import (
	"fmt"
	"runtime/debug"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"os"
	"os/signal"
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
	wh             *WrapperHolder
	header         HeaderModel
	footer         FooterModel
	hostList       HostListModel
	quitting       bool
	transitionWriter *TransitionWriter
	editingHosts     bool
	hostInput        string
	statusMessage    string
	statsCache       map[string]PWStats // cache stats per wrapper to avoid recalculation
	statsCacheTime   time.Time          // when stats were last calculated
	lastTickTime     time.Time          // when last tick happened
	statusServer     *StatusServer      // optional web status server
}

func NewTUIModel(wh *WrapperHolder, tw *TransitionWriter, initialFilter FilterMode) *TUIModel {
	if initialFilter != FilterOnline && initialFilter != FilterOffline && initialFilter != FilterSmart {
		initialFilter = FilterSmart
	}

	hostList := NewHostListModel()
	hostList.filterMode = initialFilter

	return &TUIModel{
		wh:               wh,
		header:           NewHeaderModel(),
		footer:           NewFooterModel(),
		hostList:         hostList,
		transitionWriter: tw,
		statsCache:       make(map[string]PWStats),
		statsCacheTime:   time.Time{},
		lastTickTime:     time.Now(),
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
	Quit        key.Binding
	FilterCycle key.Binding
	SortCycle   key.Binding
	Escape      key.Binding
	EditHosts   key.Binding
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
	EditHosts: key.NewBinding(
		key.WithKeys("e"),
		key.WithHelp("e", "edit hosts"),
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
	m.statsCacheTime = time.Now()
	for _, wrapper := range m.wh.Wrappers() {
		stats := wrapper.CalcStats(2 * 1e9)
		m.statsCache[wrapper.Host()] = stats
	}
}

// getCachedStats returns cached stats for a wrapper
func (m *TUIModel) getCachedStats(wrapper PingWrapperInterface) PWStats {
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

func (m *TUIModel) applyHostInput() {
	raw := strings.TrimSpace(m.hostInput)
	hosts := parseHostsInput(raw)
	m.wh.ReplaceHosts(hosts)
	m.hostList.cursor = -1
	m.hostList.scrollOffset = 0
	m.hostList.filterMode = FilterAll
	m.header.filterMode = FilterAll
	m.footer.showDetails = false
	if len(hosts) == 0 {
		m.statusMessage = "Cleared hosts; no targets configured."
	} else {
		m.statusMessage = fmt.Sprintf("Updated hosts (%d)", len(hosts))
	}
	m.editingHosts = false
}

func (m *TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
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
		}
		
		// Update countdown in header
		m.header.countdown = m.getRemainingTime()

		// Always continue UI ticker at 100ms
		return m, m.tickCmd()

	case tea.KeyMsg:
		if m.editingHosts {
			switch {
			case key.Matches(msg, keys.Escape):
				m.editingHosts = false
				m.hostInput = ""
				m.statusMessage = ""
				return m, nil
			case key.Matches(msg, keys.Enter):
				m.applyHostInput()
				return m, nil
			}
			// basic inline input editing
			switch msg.Type {
			case tea.KeyBackspace, tea.KeyDelete:
				if len(m.hostInput) > 0 {
					m.hostInput = m.hostInput[:len(m.hostInput)-1]
				}
				return m, nil
			case tea.KeyCtrlL:
				m.hostInput = ""
				return m, nil
			case tea.KeyCtrlN:
				m.hostInput += "\n"
				return m, nil
			case tea.KeySpace:
				m.hostInput += " "
				return m, nil
			case tea.KeyRunes:
				m.hostInput += string(msg.Runes)
				return m, nil
			}
			return m, nil
		}

		switch {
		case key.Matches(msg, keys.Quit):
			m.quitting = true
			m.wh.Stop()
			return m, tea.Quit

		case key.Matches(msg, keys.Escape):
			if m.footer.showDetails {
				m.footer.showDetails = false
			}
			return m, nil

		case key.Matches(msg, keys.Enter):
			if m.hostList.cursor >= 0 {
				m.footer.showDetails = !m.footer.showDetails
			}
			return m, nil

		case key.Matches(msg, keys.Up):
			filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)
			if len(filtered) > 0 {
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else if m.hostList.cursor > 0 {
					m.hostList.cursor--
				}
				m.hostList.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)
			if len(filtered) > 0 {
				if m.hostList.cursor < 0 {
					m.hostList.cursor = 0
				} else if m.hostList.cursor < len(filtered)-1 {
					m.hostList.cursor++
				}
				m.hostList.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.PageUp):
			filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)
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
			filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)
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
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.SortCycle):
			m.hostList.sortMode = nextSortMode(m.hostList.sortMode)
			m.header.sortMode = m.hostList.sortMode
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.CycleRate):
			m.header.updateRate = nextUpdateRate(m.header.updateRate)
			m.statusMessage = fmt.Sprintf("Update rate: %s", m.header.getUpdateRateString())
			// No need to restart any tickers - the time-based calculation handles everything
			return m, nil

		case key.Matches(msg, keys.HideHost):
			if m.hostList.cursor >= 0 && !m.footer.showDetails {
				filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)
				if m.hostList.cursor < len(filtered) {
					hostToHide := filtered[m.hostList.cursor].Host()
					m.hostList.hiddenHosts[hostToHide] = true
					m.statusMessage = fmt.Sprintf("Hidden: %s (press INS to show all)", hostToHide)
					// Move cursor to next visible item or previous if at end
					if m.hostList.cursor >= len(filtered)-1 && m.hostList.cursor > 0 {
						m.hostList.cursor--
					}
					m.hostList.adjustScroll()
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
			m.pushStatusView()
			return m, nil

		case key.Matches(msg, keys.EditHosts):
			m.editingHosts = true
			m.statusMessage = "Edit hosts: one per line, Enter=apply, Esc=cancel, Ctrl+L=clear, Ctrl+N=new line."
			var b strings.Builder
			for i, w := range m.wh.Wrappers() {
				if i > 0 {
					b.WriteString("\n")
				}
				b.WriteString(w.Host())
			}
			m.hostInput = b.String()
			return m, nil

		default:
			// Handle number keys 1-6 for column toggling
			if len(msg.String()) == 1 && msg.String() >= "1" && msg.String() <= "6" {
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

	if m.editingHosts {
		s.WriteString(m.renderHostInput())
		return s.String()
	}

	// Get filtered and sorted wrappers
	filtered := m.hostList.getFilteredWrappers(m.wh.Wrappers(), m.getCachedStats)

	if m.footer.showDetails && m.hostList.cursor >= 0 && m.hostList.cursor < len(filtered) {
		// Show detail view
		s.WriteString(m.renderDetailView(filtered[m.hostList.cursor]))
	} else {
		// Show list view
		s.WriteString(m.hostList.renderListView(filtered, m.getCachedStats))
	}

	// Footer
	s.WriteString(m.footer.View())

	return s.String()
}

func (m *TUIModel) renderHostInput() string {
	var b strings.Builder
	b.WriteString("Edit hosts (one per line, CIDR allowed):\n")
	b.WriteString("Ctrl+L: clear all │ Ctrl+N: new line │ enter: apply │ esc: cancel\n\n")
	b.WriteString("hosts>\n")
	b.WriteString(m.hostInput)
	b.WriteString("█")
	b.WriteString("\n\n")
	return b.String()
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

	return detailStyle.Render(details.String())
}

func (m *TUIModel) pushStatusView() {
	if m.statusServer == nil {
		return
	}
	m.statusServer.UpdateView(ServerView{
		Filter: m.hostList.filterMode,
		Sort:   m.hostList.sortMode,
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
func RunTUI(wh *WrapperHolder, tw *TransitionWriter, initialFilter FilterMode, webPort int) (finalErr error) {
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
		wh.Start()
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
		wh.Stop()
		return fmt.Errorf("interrupted by user")
	case <-time.After(60 * time.Second):
		wh.Stop()
		return fmt.Errorf("timeout waiting for wrappers to start (60s)")
	}

	model := NewTUIModel(wh, tw, initialFilter)
	var statusServer *StatusServer
	if webPort > 0 {
		initialView := ServerView{
			Filter: model.hostList.filterMode,
			Sort:   model.hostList.sortMode,
			Hidden: cloneHiddenHosts(model.hostList.hiddenHosts),
			Cols:   visibleColumnsList(model.hostList.visibleColumns),
		}
		var err error
		statusServer, err = StartStatusServer(wh, model.getCachedStats, initialView, webPort)
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to start status server on port %d: %v\n", webPort, err)
		} else {
			model.statusServer = statusServer
		}
	}

	defer wh.Stop()
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

	_, err := p.Run()
	return err
}
