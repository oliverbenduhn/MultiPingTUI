package main

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// HeaderModel handles the top bar
type HeaderModel struct {
	width      int
	filterMode FilterMode
	sortMode   SortMode
	updateRate UpdateRate
	countdown  string
}

func NewHeaderModel() HeaderModel {
	return HeaderModel{
		updateRate: UpdateRate1s,
	}
}

func (m HeaderModel) Init() tea.Cmd {
	return nil
}

func (m HeaderModel) Update(msg tea.Msg) (HeaderModel, tea.Cmd) {
	return m, nil
}

func (m HeaderModel) View() string {
	var s strings.Builder
	s.WriteString(titleStyle.Render(VersionString()))
	s.WriteString("\n")

	filterText := fmt.Sprintf("Filter: %s", m.getFilterModeString())
	sortText := fmt.Sprintf("Sort: %s", m.getSortModeString())
	rateText := fmt.Sprintf("Rate: %s", m.getUpdateRateString())

	if m.countdown != "" {
		rateText += " " + m.countdown
	}

	headerText := fmt.Sprintf(" %s │ %s │ %s ", filterText, sortText, rateText)
	if m.width > 0 && len(headerText) > m.width {
		headerText = headerText[:m.width]
	}
	header := headerStyle.Render(headerText)
	s.WriteString(header)
	s.WriteString("\n\n")
	return s.String()
}

func (m HeaderModel) getFilterModeString() string {
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

func (m HeaderModel) getSortModeString() string {
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

func (m HeaderModel) getUpdateRateString() string {
	switch m.updateRate {
	case UpdateRate100ms:
		return "100ms"
	case UpdateRate1s:
		return "1s"
	case UpdateRate5s:
		return "5s"
	case UpdateRate30s:
		return "30s"
	default:
		return "100ms"
	}
}

// FooterModel handles the bottom help bar
type FooterModel struct {
	width int
	mode  FooterMode
}

type FooterMode int

const (
	FooterList FooterMode = iota
	FooterDetails
	FooterDashboard
)

func NewFooterModel() FooterModel {
	return FooterModel{mode: FooterList}
}

func (m FooterModel) View() string {
	var s strings.Builder
	s.WriteString("\n")
	switch m.mode {
	case FooterDetails:
		s.WriteString(helpStyle.Render("esc: back │ t: traceroute │ pgup/pgdn: scroll │ d: dashboard │ q: quit"))
	case FooterDashboard:
		s.WriteString(helpStyle.Render("esc: back │ d: back │ q: quit"))
	default:
		s.WriteString(helpStyle.Render("↑↓/jk: navigate │ enter: details │ del: hide host │ ins: show all │ d: dashboard │ e: edit config │ 1-6: columns │ g: group by subnet │ q: quit"))
		s.WriteString("\n")
		s.WriteString(helpStyle.Render("f: filter │ s: sort │ r: rate (100ms/1s/5s/30s) │ pgup/pgdn: scroll"))
	}
	return s.String()
}

// HostListModel handles the list of hosts
type HostListModel struct {
	cursor           int
	scrollOffset     int
	width            int
	height           int
	visibleColumns   map[int]bool
	statsCache       map[string]PWStats
	filterMode       FilterMode
	sortMode         SortMode
	hiddenHosts      map[string]bool
	cachedWrappers   []PingWrapperInterface
	cacheInvalidated bool
	groupBySubnet    bool
	rawInputs        []string
}

func NewHostListModel() HostListModel {
	visibleCols := make(map[int]bool)
	for i := 1; i <= 6; i++ {
		visibleCols[i] = true
	}
	return HostListModel{
		cursor:           -1,
		visibleColumns:   visibleCols,
		statsCache:       make(map[string]PWStats),
		hiddenHosts:      make(map[string]bool),
		sortMode:         SortByIP, // Default sort
		cacheInvalidated: true,
		groupBySubnet:    true,
	}
}

// Helper methods for HostListModel would go here (e.g., renderListView logic)
// For now, I'm just defining the structure. I'll move the logic from tui.go later.
