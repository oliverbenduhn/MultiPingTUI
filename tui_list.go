package main

import (
	"bytes"
	"fmt"
	"sort"
	"strings"
	"time"
)

func (m *HostListModel) renderListView(wrappers []PingWrapperInterface, getCachedStats func(PingWrapperInterface) PWStats) string {
	var s strings.Builder

	if len(wrappers) == 0 {
		s.WriteString(helpStyle.Render("No hosts match the current filter"))
		return s.String()
	}

	now := time.Now().UnixNano()

	// Dynamic column widths with toggleable columns
	statusWidth := 3
	nameWidth := 32
	ipWidth := 18
	rttWidth := 10
	lastReplyWidth := 16
	lastLossWidth := 16
	minName := 15
	minIP := 12
	minRTT := 8
	minLastReply := 12
	minLastLoss := 12

	// Count visible columns for spacing calculation
	visibleCount := 0
	if m.visibleColumns[1] {
		visibleCount++
	}
	if m.visibleColumns[2] {
		visibleCount++
	}
	if m.visibleColumns[3] {
		visibleCount++
	}
	if m.visibleColumns[4] {
		visibleCount++
	}
	if m.visibleColumns[5] {
		visibleCount++
	}
	if m.visibleColumns[6] {
		visibleCount++
	}

	spaceCount := visibleCount - 1 // spaces between visible columns
	if spaceCount < 0 {
		spaceCount = 0
	}

	totalWidth := 0
	if m.visibleColumns[1] {
		totalWidth += statusWidth
	}
	if m.visibleColumns[2] {
		totalWidth += nameWidth
	}
	if m.visibleColumns[3] {
		totalWidth += ipWidth
	}
	if m.visibleColumns[4] {
		totalWidth += rttWidth
	}
	if m.visibleColumns[5] {
		totalWidth += lastReplyWidth
	}
	if m.visibleColumns[6] {
		totalWidth += lastLossWidth
	}
	totalWidth += spaceCount

	target := m.width - 2
	if target < 50 {
		target = 50
	}

	// Shrink columns (starting with the widest) until we fit, but not below mins
shrinkColumns:
	for totalWidth > target {
		switch {
		case nameWidth > minName && m.visibleColumns[2]:
			nameWidth--
		case lastLossWidth > minLastLoss && m.visibleColumns[6]:
			lastLossWidth--
		case lastReplyWidth > minLastReply && m.visibleColumns[5]:
			lastReplyWidth--
		case ipWidth > minIP && m.visibleColumns[3]:
			ipWidth--
		case rttWidth > minRTT && m.visibleColumns[4]:
			rttWidth--
		default:
			// We hit mins; break to avoid infinite loop
			break shrinkColumns
		}
		totalWidth = 0
		if m.visibleColumns[1] {
			totalWidth += statusWidth
		}
		if m.visibleColumns[2] {
			totalWidth += nameWidth
		}
		if m.visibleColumns[3] {
			totalWidth += ipWidth
		}
		if m.visibleColumns[4] {
			totalWidth += rttWidth
		}
		if m.visibleColumns[5] {
			totalWidth += lastReplyWidth
		}
		if m.visibleColumns[6] {
			totalWidth += lastLossWidth
		}
		totalWidth += spaceCount
	}

	// Build table header based on visible columns with dynamic widths
	var headerParts []string
	if m.visibleColumns[1] {
		headerParts = append(headerParts, fmt.Sprintf("%-*s", statusWidth, "1:St"))
	}
	if m.visibleColumns[2] {
		headerParts = append(headerParts, fmt.Sprintf("%-*s", nameWidth, "2:Name"))
	}
	if m.visibleColumns[3] {
		headerParts = append(headerParts, fmt.Sprintf("%-*s", ipWidth, "3:IP"))
	}
	if m.visibleColumns[4] {
		headerParts = append(headerParts, fmt.Sprintf("%-*s", rttWidth, "4:RTT"))
	}
	if m.visibleColumns[5] {
		headerParts = append(headerParts, fmt.Sprintf("%-*s", lastReplyWidth, "5:Last Reply"))
	}
	if m.visibleColumns[6] {
		headerParts = append(headerParts, "6:Last Loss")
	}

	headerLine := strings.Join(headerParts, " ")
	s.WriteString(headerStyle.Render(headerLine))
	s.WriteString("\n")
	// Separator line with minimum width
	sepWidth := m.width - 2
	if sepWidth < 10 {
		sepWidth = 100 // Default width if terminal size not yet known
	}
	s.WriteString(separatorStyle.Render(strings.Repeat("─", sepWidth)))
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
		stats := getCachedStats(wrapper)
		isOnline := stats.state && stats.error_message == ""

		// Column values
		status := "✓"
		if !isOnline {
			status = "✗"
		}

		name := stats.GetHostRepr()
		if name == "" {
			name = wrapper.Host()
		}
		if len(name) > nameWidth {
			if nameWidth > 3 {
				name = name[:nameWidth-3] + "..."
			} else {
				name = name[:nameWidth]
			}
		}

		ip := stats.iprepr
		if len(ip) > ipWidth {
			if ipWidth > 3 {
				ip = ip[:ipWidth-3] + "..."
			} else {
				ip = ip[:ipWidth]
			}
		}

		rtt := stats.lastrtt_as_string
		if !isOnline {
			rtt = "-"
		}

		// Only show last reply when host is offline to avoid clutter for healthy hosts
		lastReply := "-"
		if !isOnline {
			if stats.lastrecv > 0 {
				lastReply = time.Duration(stats.last_seen_nano).Round(time.Second).String() + " ago"
			} else {
				lastReply = "never"
			}
		}

		lastLoss := "-"
		if stats.last_loss_nano > 0 {
			lastLoss = fmt.Sprintf("%s ago (%s)",
				time.Duration(time.Now().UnixNano()-stats.last_loss_nano).Round(time.Second),
				time.Duration(stats.last_loss_duration).Round(time.Second/10))
		}

		// Build line based on visible columns with dynamic widths
		var lineParts []string
		if m.visibleColumns[1] {
			lineParts = append(lineParts, fmt.Sprintf("%-*s", statusWidth, status))
		}
		if m.visibleColumns[2] {
			lineParts = append(lineParts, fmt.Sprintf("%-*s", nameWidth, name))
		}
		if m.visibleColumns[3] {
			lineParts = append(lineParts, fmt.Sprintf("%-*s", ipWidth, ip))
		}
		if m.visibleColumns[4] {
			lineParts = append(lineParts, fmt.Sprintf("%-*s", rttWidth, rtt))
		}
		if m.visibleColumns[5] {
			lineParts = append(lineParts, fmt.Sprintf("%-*s", lastReplyWidth, lastReply))
		}
		if m.visibleColumns[6] {
			lineParts = append(lineParts, lastLoss)
		}

		line := strings.Join(lineParts, " ")

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

func (m *HostListModel) adjustScroll() {
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

func (m *HostListModel) getFilteredWrappers(wrappers []PingWrapperInterface, getCachedStats func(PingWrapperInterface) PWStats) []PingWrapperInterface {
	var filtered []PingWrapperInterface

	for _, wrapper := range wrappers {
		// Skip hidden hosts
		if m.hiddenHosts[wrapper.Host()] {
			continue
		}

		stats := getCachedStats(wrapper)
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
			statsI := getCachedStats(filtered[i])
			statsJ := getCachedStats(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			// Push hosts without recent replies to the end
			if onlineI != onlineJ {
				return onlineI
			}

			// Use DNS name (hrepr) if available, otherwise use Host()
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
			statsI := getCachedStats(filtered[i])
			statsJ := getCachedStats(filtered[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			return filtered[i].Host() < filtered[j].Host()
		})
	case SortByRTT:
		sort.Slice(filtered, func(i, j int) bool {
			statsI := getCachedStats(filtered[i])
			statsJ := getCachedStats(filtered[j])
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
			statsI := getCachedStats(filtered[i])
			statsJ := getCachedStats(filtered[j])
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
			statsI := getCachedStats(filtered[i])
			statsJ := getCachedStats(filtered[j])
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

func (m *HostListModel) getColumnName(colNum int) string {
	switch colNum {
	case 1:
		return "St"
	case 2:
		return "Name"
	case 3:
		return "IP"
	case 4:
		return "RTT"
	case 5:
		return "Last Reply"
	case 6:
		return "Last Loss"
	default:
		return "Unknown"
	}
}

