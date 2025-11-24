package main

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// FilterMode represents the current filter state
type FilterMode int

const (
	FilterAll FilterMode = iota
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
)

// TUIModel is the bubbletea model for the TUI
type TUIModel struct {
	wh               *WrapperHolder
	cursor           int
	filterMode       FilterMode
	sortMode         SortMode
	showDetails      bool
	width            int
	height           int
	quitting         bool
	transitionWriter *TransitionWriter
}

// tickMsg is sent every 100ms to update the display
type tickMsg time.Time

// keyMap defines the keyboard shortcuts
type keyMap struct {
	Up       key.Binding
	Down     key.Binding
	Enter    key.Binding
	Quit     key.Binding
	FilterAll key.Binding
	FilterOnline key.Binding
	FilterOffline key.Binding
	SortName key.Binding
	SortStatus key.Binding
	SortRTT key.Binding
	Escape key.Binding
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
	Enter: key.NewBinding(
		key.WithKeys("enter"),
		key.WithHelp("enter", "details"),
	),
	Quit: key.NewBinding(
		key.WithKeys("q", "ctrl+c"),
		key.WithHelp("q", "quit"),
	),
	FilterAll: key.NewBinding(
		key.WithKeys("a"),
		key.WithHelp("a", "all"),
	),
	FilterOnline: key.NewBinding(
		key.WithKeys("o"),
		key.WithHelp("o", "online"),
	),
	FilterOffline: key.NewBinding(
		key.WithKeys("f"),
		key.WithHelp("f", "offline"),
	),
	SortName: key.NewBinding(
		key.WithKeys("n"),
		key.WithHelp("n", "sort name"),
	),
	SortStatus: key.NewBinding(
		key.WithKeys("s"),
		key.WithHelp("s", "sort status"),
	),
	SortRTT: key.NewBinding(
		key.WithKeys("r"),
		key.WithHelp("r", "sort rtt"),
	),
	Escape: key.NewBinding(
		key.WithKeys("esc"),
		key.WithHelp("esc", "back"),
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

	normalStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("252"))

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

func NewTUIModel(wh *WrapperHolder, tw *TransitionWriter) *TUIModel {
	return &TUIModel{
		wh:               wh,
		cursor:           0,
		filterMode:       FilterAll,
		sortMode:         SortByName,
		showDetails:      false,
		transitionWriter: tw,
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
			m.showDetails = !m.showDetails
			return m, nil

		case key.Matches(msg, keys.Up):
			if m.cursor > 0 {
				m.cursor--
			}
			return m, nil

		case key.Matches(msg, keys.Down):
			filtered := m.getFilteredWrappers()
			if m.cursor < len(filtered)-1 {
				m.cursor++
			}
			return m, nil

		case key.Matches(msg, keys.FilterAll):
			m.filterMode = FilterAll
			m.cursor = 0
			return m, nil

		case key.Matches(msg, keys.FilterOnline):
			m.filterMode = FilterOnline
			m.cursor = 0
			return m, nil

		case key.Matches(msg, keys.FilterOffline):
			m.filterMode = FilterOffline
			m.cursor = 0
			return m, nil

		case key.Matches(msg, keys.SortName):
			m.sortMode = SortByName
			return m, nil

		case key.Matches(msg, keys.SortStatus):
			m.sortMode = SortByStatus
			return m, nil

		case key.Matches(msg, keys.SortRTT):
			m.sortMode = SortByRTT
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

	// Get filtered and sorted wrappers
	filtered := m.getFilteredWrappers()

	if m.showDetails && len(filtered) > 0 {
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
		s.WriteString(helpStyle.Render("↑↓/jk: navigate │ enter: details │ a: all │ o: online │ f: offline │ n: sort name │ s: sort status │ r: sort rtt │ q: quit"))
	}

	return s.String()
}

func (m *TUIModel) renderListView(wrappers []PingWrapperInterface) string {
	var s strings.Builder

	for i, wrapper := range wrappers {
		stats := wrapper.CalcStats(2 * 1e9)
		isOnline := stats.state && stats.error_message == ""

		// Build line
		var line string
		if isOnline {
			line = fmt.Sprintf("✓ %-40s %s", wrapper.Host(), stats.lastrtt_as_string)
			if i == m.cursor {
				line = selectedStyle.Render(line)
			} else {
				line = onlineStyle.Render(line)
			}
		} else {
			reason := "timeout"
			if stats.error_message != "" {
				reason = stats.error_message
			} else if stats.lastrecv == 0 {
				reason = "never replied"
			}
			line = fmt.Sprintf("✗ %-40s %s", wrapper.Host(), reason)
			if i == m.cursor {
				line = selectedStyle.Render(line)
			} else {
				line = offlineStyle.Render(line)
			}
		}

		s.WriteString(line)
		s.WriteString("\n")
	}

	if len(wrappers) == 0 {
		s.WriteString(helpStyle.Render("No hosts match the current filter"))
	}

	return s.String()
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

	details.WriteString(fmt.Sprintf("\nUptime: %s\n", time.Duration(time.Now().UnixNano()-stats.startup_time).Round(time.Second)))

	return detailStyle.Render(details.String())
}

func (m *TUIModel) getFilteredWrappers() []PingWrapperInterface {
	var filtered []PingWrapperInterface

	for _, wrapper := range m.wh.ping_wrappers {
		stats := wrapper.CalcStats(2 * 1e9)
		isOnline := stats.state && stats.error_message == ""

		switch m.filterMode {
		case FilterAll:
			filtered = append(filtered, wrapper)
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
			return filtered[i].Host() < filtered[j].Host()
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
			return statsI.lastrtt < statsJ.lastrtt
		})
	}

	return filtered
}

func (m *TUIModel) getFilterModeString() string {
	switch m.filterMode {
	case FilterAll:
		return "All"
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
	default:
		return "Unknown"
	}
}

// RunTUI starts the TUI interface
func RunTUI(wh *WrapperHolder, tw *TransitionWriter) error {
	model := NewTUIModel(wh, tw)
	p := tea.NewProgram(model, tea.WithAltScreen())

	wh.Start()
	defer wh.Stop()

	_, err := p.Run()
	return err
}
