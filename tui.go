package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"bytes"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"net"
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

// TUIModel is the bubbletea model for the TUI
type TUIModel struct {
	wh               *WrapperHolder
	cursor           int
	scrollOffset     int
	filterMode       FilterMode
	sortMode         SortMode
	showDetails      bool
	width            int
	height           int
	quitting         bool
	transitionWriter *TransitionWriter
	editingHosts     bool
	hostInput        string
	statusMessage    string
	hiddenHosts      map[string]bool // tracks hidden hosts by Host() name
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
}

// Styles
var (
	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("170")).
			MarginLeft(1)

	headerStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("12")).
			Background(lipgloss.Color("236")).
			Padding(0, 1)

	selectedStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("0")).
			Background(lipgloss.Color("12")).
			Bold(true)

	newOnlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("33")).
			Bold(true)

	onlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("46")).
			Bold(true)

	offlineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("196")).
			Bold(true)

	helpStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("241")).
			MarginLeft(1)

	detailStyle = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("12")).
			Padding(1, 2).
			MarginLeft(2)
)

func NewTUIModel(wh *WrapperHolder, tw *TransitionWriter, initialFilter FilterMode) *TUIModel {
	if initialFilter != FilterOnline && initialFilter != FilterOffline && initialFilter != FilterSmart {
		initialFilter = FilterSmart
	}

	return &TUIModel{
		wh:               wh,
		cursor:           -1,
		scrollOffset:     0,
		filterMode:       initialFilter,
		sortMode:         SortByIP,
		showDetails:      false,
		transitionWriter: tw,
		hiddenHosts:      make(map[string]bool),
	}
}

func (m *TUIModel) Init() tea.Cmd {
	return tea.Batch(
		tickCmd(),
		tea.EnterAltScreen,
	)
}

func tickCmd() tea.Cmd {
	return tea.Tick(100*time.Millisecond, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func (m *TUIModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		return m, nil

	case tickMsg:
		// Update stats for all wrappers
		m.wh.CalcStats(2 * 1e9)
		return m, tickCmd()

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
			if m.showDetails {
				m.showDetails = false
			}
			return m, nil

		case key.Matches(msg, keys.Enter):
			if m.cursor >= 0 {
				m.showDetails = !m.showDetails
			}
			return m, nil

		case key.Matches(msg, keys.Up):
			filtered := m.getFilteredWrappers()
			if len(filtered) > 0 {
				if m.cursor < 0 {
					m.cursor = 0
				} else if m.cursor > 0 {
					m.cursor--
				}
				m.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			filtered := m.getFilteredWrappers()
			if len(filtered) > 0 {
				if m.cursor < 0 {
					m.cursor = 0
				} else if m.cursor < len(filtered)-1 {
					m.cursor++
				}
				m.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.PageUp):
			filtered := m.getFilteredWrappers()
			if len(filtered) > 0 {
				visibleLines := m.height - 7
				if visibleLines < 1 {
					visibleLines = 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				} else {
					m.cursor -= visibleLines
					if m.cursor < 0 {
						m.cursor = 0
					}
				}
				m.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.PageDown):
			filtered := m.getFilteredWrappers()
			if len(filtered) > 0 {
				visibleLines := m.height - 7
				if visibleLines < 1 {
					visibleLines = 1
				}
				if m.cursor < 0 {
					m.cursor = 0
				} else {
					m.cursor += visibleLines
					if m.cursor >= len(filtered) {
						m.cursor = len(filtered) - 1
					}
				}
				m.adjustScroll()
			}
			return m, nil

		case key.Matches(msg, keys.FilterCycle):
			m.filterMode = nextFilterMode(m.filterMode)
			m.cursor = -1
			m.scrollOffset = 0
			return m, nil

		case key.Matches(msg, keys.SortCycle):
			m.sortMode = nextSortMode(m.sortMode)
			return m, nil

		case key.Matches(msg, keys.HideHost):
			if m.cursor >= 0 && !m.showDetails {
				filtered := m.getFilteredWrappers()
				if m.cursor < len(filtered) {
					hostToHide := filtered[m.cursor].Host()
					m.hiddenHosts[hostToHide] = true
					m.statusMessage = fmt.Sprintf("Hidden: %s (press INS to show all)", hostToHide)
					// Move cursor to next visible item or previous if at end
					if m.cursor >= len(filtered)-1 && m.cursor > 0 {
						m.cursor--
					}
					m.adjustScroll()
				}
			}
			return m, nil

		case key.Matches(msg, keys.ShowAll):
			if len(m.hiddenHosts) > 0 {
				count := len(m.hiddenHosts)
				m.hiddenHosts = make(map[string]bool)
				m.statusMessage = fmt.Sprintf("Showing all hosts (%d unhidden)", count)
			} else {
				m.statusMessage = "No hidden hosts"
			}
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
		}
	}

	return m, nil
}

func (m *TUIModel) View() string {
	if m.quitting {
		return "Goodbye!\n"
	}

	var s strings.Builder

	// Title
	s.WriteString(titleStyle.Render(VersionString()))
	s.WriteString("\n")

	// Header with filter and sort info
	filterText := fmt.Sprintf("Filter: %s", m.getFilterModeString())
	sortText := fmt.Sprintf("Sort: %s", m.getSortModeString())
	header := headerStyle.Render(fmt.Sprintf(" %s │ %s ", filterText, sortText))
	s.WriteString(header)
	s.WriteString("\n\n")

	if m.statusMessage != "" {
		s.WriteString(helpStyle.Render(m.statusMessage))
		s.WriteString("\n\n")
	}

	if m.editingHosts {
		s.WriteString(m.renderHostInput())
		return s.String()
	}

	// Get filtered and sorted wrappers
	filtered := m.getFilteredWrappers()

	if m.showDetails && m.cursor >= 0 && m.cursor < len(filtered) {
		// Show detail view
		s.WriteString(m.renderDetailView(filtered[m.cursor]))
	} else {
		// Show list view
		s.WriteString(m.renderListView(filtered))
	}

	// Help
	s.WriteString("\n")
	if m.showDetails {
		s.WriteString(helpStyle.Render("esc: back │ q: quit"))
	} else {
		s.WriteString(helpStyle.Render("↑↓/jk: navigate │ enter: details │ e: edit hosts │ q: quit"))
		s.WriteString("\n")
		s.WriteString(helpStyle.Render("f: cycle filters (smart/online/offline/all) │ s: cycle sort (name/status/rtt/last/ip)"))
	}

	return s.String()
}

func (m *TUIModel) renderListView(wrappers []PingWrapperInterface) string {
	var s strings.Builder

	if len(wrappers) == 0 {
		s.WriteString(helpStyle.Render("No hosts match the current filter"))
		return s.String()
	}

	now := time.Now().UnixNano()

	// Table header
	headerLine := fmt.Sprintf("%-3s %-40s %-20s %-12s %-20s %s",
		"St", "Name", "IP", "RTT", "Last Reply", "Last Loss")
	s.WriteString(headerStyle.Render(headerLine))
	s.WriteString("\n")
	// Separator line with minimum width
	sepWidth := m.width - 2
	if sepWidth < 10 {
		sepWidth = 100 // Default width if terminal size not yet known
	}
	s.WriteString(strings.Repeat("─", sepWidth))
	s.WriteString("\n")

	// Calculate visible range (accounting for header)
	visibleLines := m.height - 7 // Reduced for header
	if visibleLines < 1 {
		visibleLines = 1
	}

	start := m.scrollOffset
	end := m.scrollOffset + visibleLines
	if end > len(wrappers) {
		end = len(wrappers)
	}

	// Render only visible items
	for i := start; i < end; i++ {
		wrapper := wrappers[i]
		stats := wrapper.CalcStats(2 * 1e9)
		isOnline := stats.state && stats.error_message == ""

		// Column values
		status := "✓"
		if !isOnline {
			status = "✗"
		}

		name := wrapper.Host()
		if len(name) > 40 {
			name = name[:37] + "..."
		}

		ip := stats.iprepr
		if len(ip) > 20 {
			ip = ip[:17] + "..."
		}

		rtt := stats.lastrtt_as_string
		if !isOnline {
			rtt = "-"
		}

		lastReply := "-"
		if stats.lastrecv > 0 {
			lastReply = time.Duration(stats.last_seen_nano).Round(time.Second).String() + " ago"
		} else {
			lastReply = "never"
		}

		lastLoss := "-"
		if stats.last_loss_nano > 0 {
			lastLoss = fmt.Sprintf("%s ago (%s)",
				time.Duration(time.Now().UnixNano()-stats.last_loss_nano).Round(time.Second),
				time.Duration(stats.last_loss_duration).Round(time.Second/10))
		}

		// Build line
		line := fmt.Sprintf("%-3s %-40s %-20s %-12s %-20s %s",
			status, name, ip, rtt, lastReply, lastLoss)

		if i == m.cursor && m.cursor >= 0 {
			line = selectedStyle.Render(line)
		} else if isOnline && stats.last_up_transition > 0 && now-stats.last_up_transition < int64(20*time.Second) {
			line = newOnlineStyle.Render(line)
		} else if isOnline {
			line = onlineStyle.Render(line)
		} else {
			line = offlineStyle.Render(line)
		}

		s.WriteString(line)
		s.WriteString("\n")
	}

	// Show scroll indicator if needed
	if len(wrappers) > visibleLines {
		totalItems := len(wrappers)
		scrollInfo := fmt.Sprintf(" [%d-%d/%d] ", start+1, end, totalItems)
		s.WriteString(helpStyle.Render(scrollInfo))
	}

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
	stats := wrapper.CalcStats(2 * 1e9)
	isOnline := stats.state && stats.error_message == ""

	var details strings.Builder
	details.WriteString(fmt.Sprintf("Host: %s\n", wrapper.Host()))
	details.WriteString(fmt.Sprintf("IP: %s\n\n", stats.iprepr))

	if isOnline {
		details.WriteString(onlineStyle.Render("Status: ONLINE ✓"))
		details.WriteString("\n\n")
		details.WriteString(fmt.Sprintf("Last RTT: %s\n", stats.lastrtt_as_string))
		details.WriteString(fmt.Sprintf("Last Received: %s ago\n", time.Duration(stats.last_seen_nano).Round(time.Millisecond)))
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

func (m *TUIModel) applyHostInput() {
	raw := strings.TrimSpace(m.hostInput)
	hosts := parseHostsInput(raw)
	m.wh.ReplaceHosts(hosts)
	m.cursor = -1
	m.scrollOffset = 0
	m.filterMode = FilterAll
	m.showDetails = false
	if len(hosts) == 0 {
		m.statusMessage = "Cleared hosts; no targets configured."
	} else {
		m.statusMessage = fmt.Sprintf("Updated hosts (%d)", len(hosts))
	}
	m.editingHosts = false
}

// adjustScroll adjusts the scroll offset to keep the cursor visible
func (m *TUIModel) adjustScroll() {
	if m.cursor < 0 {
		return
	}

	// Calculate available height for list items
	// height - title(1) - header(1) - spacing(1) - table_header(1) - separator(1) - help(2) = height - 7
	visibleLines := m.height - 7
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Scroll up if cursor is above visible area
	if m.cursor < m.scrollOffset {
		m.scrollOffset = m.cursor
	}

	// Scroll down if cursor is below visible area
	if m.cursor >= m.scrollOffset+visibleLines {
		m.scrollOffset = m.cursor - visibleLines + 1
	}
}

func (m *TUIModel) getFilteredWrappers() []PingWrapperInterface {
	var filtered []PingWrapperInterface

	for _, wrapper := range m.wh.Wrappers() {
		// Skip hidden hosts
		if m.hiddenHosts[wrapper.Host()] {
			continue
		}

		stats := wrapper.CalcStats(2 * 1e9)
		isOnline := stats.state && stats.error_message == ""
		seen := stats.has_ever_received

		switch m.filterMode {
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

	// Sort
	switch m.sortMode {
	case SortByName:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := filtered[i].CalcStats(2 * 1e9)
			statsJ := filtered[j].CalcStats(2 * 1e9)
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			// Push hosts without recent replies to the end
			if onlineI != onlineJ {
				return onlineI
			}

			// Use DNS name (hrepr) if available, otherwise use Host()
			nameI := statsI.hrepr
			nameJ := statsJ.hrepr
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
			statsI := filtered[i].CalcStats(2 * 1e9)
			statsJ := filtered[j].CalcStats(2 * 1e9)
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			return filtered[i].Host() < filtered[j].Host()
		})
	case SortByRTT:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := filtered[i].CalcStats(2 * 1e9)
			statsJ := filtered[j].CalcStats(2 * 1e9)
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			// Push hosts without recent replies to the end
			if onlineI != onlineJ {
				return onlineI
			}

			return statsI.lastrtt < statsJ.lastrtt
		})
	case SortByLastSeen:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := filtered[i].CalcStats(2 * 1e9)
			statsJ := filtered[j].CalcStats(2 * 1e9)
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			// Offline hosts first, then online hosts
			if onlineI != onlineJ {
				return !onlineI // offline (false) comes before online (true)
			}

			// Among offline hosts: never received replies go last
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
				// Both have received before: sort by last_loss_nano (most recent problem first)
				return statsI.last_loss_nano > statsJ.last_loss_nano
			}

			// Among online hosts: sort by whether they ever had a loss
			hasLossI := statsI.last_loss_nano > 0
			hasLossJ := statsJ.last_loss_nano > 0
			if hasLossI != hasLossJ {
				return hasLossI // hosts with past issues first
			}
			if hasLossI && hasLossJ {
				// Both had losses: sort by most recent loss
				return statsI.last_loss_nano > statsJ.last_loss_nano
			}

			// Both are stable online hosts with no history of loss: sort by name
			nameI := statsI.hrepr
			nameJ := statsJ.hrepr
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
			statsI := filtered[i].CalcStats(2 * 1e9)
			statsJ := filtered[j].CalcStats(2 * 1e9)
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

func (m *TUIModel) getFilterModeString() string {
	switch m.filterMode {
	case FilterAll:
		return "All"
	case FilterSmart:
		return "Smart"
	case FilterOnline:
		return "Online"
	case FilterOffline:
		return "Offline"
	default:
		return "Unknown"
	}
}

func (m *TUIModel) getSortModeString() string {
	switch m.sortMode {
	case SortByName:
		return "Name"
	case SortByStatus:
		return "Status"
	case SortByRTT:
		return "RTT"
	case SortByLastSeen:
		return "Last Seen"
	case SortByIP:
		return "IP"
	default:
		return "Unknown"
	}
}

func nextFilterMode(current FilterMode) FilterMode {
	switch current {
	case FilterSmart:
		return FilterOnline
	case FilterOnline:
		return FilterOffline
	case FilterOffline:
		return FilterAll
	default:
		return FilterSmart
	}
}

func nextSortMode(current SortMode) SortMode {
	switch current {
	case SortByName:
		return SortByStatus
	case SortByStatus:
		return SortByRTT
	case SortByRTT:
		return SortByLastSeen
	case SortByLastSeen:
		return SortByIP
	default:
		return SortByName
	}
}

func parseHostsInput(raw string) []string {
	fields := strings.Fields(raw)
	var hosts []string
	for _, item := range fields {
		if ips, err := ExpandCIDR(item); err == nil {
			hosts = append(hosts, ips...)
		} else {
			hosts = append(hosts, item)
		}
	}
	return hosts
}

func ipKey(s string) []byte {
	ip := net.ParseIP(s)
	if ip == nil {
		return nil
	}
	if v4 := ip.To4(); v4 != nil {
		return append(make([]byte, 12), v4...)
	}
	return ip.To16()
}

// RunTUI starts the TUI interface with an initial filter mode applied
func RunTUI(wh *WrapperHolder, tw *TransitionWriter, initialFilter FilterMode) error {
	model := NewTUIModel(wh, tw, initialFilter)
	p := tea.NewProgram(model, tea.WithAltScreen())

	wh.Start()
	defer wh.Stop()

	_, err := p.Run()
	return err
}
