package main

import (
	"bytes"
	"fmt"
	"net"
	"sort"
	"strings"
	"time"

	"github.com/mattn/go-runewidth"
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

	// Create a local copy of visible columns to avoid mutating persistent settings
	visibleCols := make(map[int]bool)
	for k, v := range m.visibleColumns {
		visibleCols[k] = v
	}

	// Count visible columns for spacing calculation
	visibleCount := 0
	if visibleCols[1] {
		visibleCount++
	}
	if visibleCols[2] {
		visibleCount++
	}
	if visibleCols[3] {
		visibleCount++
	}
	if visibleCols[4] {
		visibleCount++
	}
	if visibleCols[5] {
		visibleCount++
	}
	if visibleCols[6] {
		visibleCount++
	}

	spaceCount := visibleCount - 1 // spaces between visible columns
	if spaceCount < 0 {
		spaceCount = 0
	}

	totalWidth := 0
	if visibleCols[1] {
		totalWidth += statusWidth
	}
	if visibleCols[2] {
		totalWidth += nameWidth
	}
	if visibleCols[3] {
		totalWidth += ipWidth
	}
	if visibleCols[4] {
		totalWidth += rttWidth
	}
	if visibleCols[5] {
		totalWidth += lastReplyWidth
	}
	if visibleCols[6] {
		totalWidth += lastLossWidth
	}
	totalWidth += spaceCount

	target := m.width - 2
	if m.width <= 0 {
		target = 100
	} else if target < 10 {
		target = 10
	}

	// Shrink columns (starting with the widest) until we fit, but not below mins
shrinkColumns:
	for totalWidth > target {
		switch {
		case nameWidth > minName && visibleCols[2]:
			nameWidth--
		case lastLossWidth > minLastLoss && visibleCols[6]:
			lastLossWidth--
		case lastReplyWidth > minLastReply && visibleCols[5]:
			lastReplyWidth--
		case ipWidth > minIP && visibleCols[3]:
			ipWidth--
		case rttWidth > minRTT && visibleCols[4]:
			rttWidth--
		default:
			// We hit mins; break to avoid infinite loop
			break shrinkColumns
		}
		totalWidth = 0
		if visibleCols[1] {
			totalWidth += statusWidth
		}
		if visibleCols[2] {
			totalWidth += nameWidth
		}
		if visibleCols[3] {
			totalWidth += ipWidth
		}
		if visibleCols[4] {
			totalWidth += rttWidth
		}
		if visibleCols[5] {
			totalWidth += lastReplyWidth
		}
		if visibleCols[6] {
			totalWidth += lastLossWidth
		}
		totalWidth += spaceCount
	}

	// If we still don't fit even with minimums, dynamically disable columns starting from the right
	// (excluding Status (1) and Name (2)) to fit the terminal.
	if totalWidth > target {
		for _, col := range []int{6, 5, 4, 3} {
			if visibleCols[col] {
				visibleCols[col] = false
				// Recompute totalWidth
				visibleCount = 0
				for i := 1; i <= 6; i++ {
					if visibleCols[i] {
						visibleCount++
					}
				}
				spaceCount = visibleCount - 1
				if spaceCount < 0 {
					spaceCount = 0
				}
				totalWidth = 0
				if visibleCols[1] {
					totalWidth += statusWidth
				}
				if visibleCols[2] {
					totalWidth += nameWidth
				}
				if visibleCols[3] {
					totalWidth += ipWidth
				}
				if visibleCols[4] {
					totalWidth += rttWidth
				}
				if visibleCols[5] {
					totalWidth += lastReplyWidth
				}
				if visibleCols[6] {
					totalWidth += lastLossWidth
				}
				totalWidth += spaceCount

				if totalWidth <= target {
					break
				}
			}
		}
	}

	// If we STILL don't fit, shrink name down to absolute minimum of 3 characters
	if totalWidth > target && visibleCols[2] {
		needed := totalWidth - target
		nameWidth -= needed
		if nameWidth < 3 {
			nameWidth = 3
		}
	}

	// Build table header based on visible columns with dynamic widths
	var headerParts []string
	if visibleCols[1] {
		headerParts = append(headerParts, displayPad("1:St", statusWidth))
	}
	if visibleCols[2] {
		headerParts = append(headerParts, displayPad("2:Name", nameWidth))
	}
	if visibleCols[3] {
		headerParts = append(headerParts, displayPad("3:IP", ipWidth))
	}
	if visibleCols[4] {
		headerParts = append(headerParts, displayPad("4:RTT", rttWidth))
	}
	if visibleCols[5] {
		headerParts = append(headerParts, displayPad("5:Last Reply", lastReplyWidth))
	}
	if visibleCols[6] {
		headerParts = append(headerParts, displayPad("6:Last Loss", lastLossWidth))
	}

	headerLine := strings.Join(headerParts, " ")
	s.WriteString(headerStyle.Render(headerLine))
	s.WriteString("\n")
	// Separator line with minimum width
	sepWidth := m.width - 2
	if m.width <= 0 {
		sepWidth = totalWidth
	} else if sepWidth < 1 {
		sepWidth = 1
	}
	s.WriteString(separatorStyle.Render(strings.Repeat("─", sepWidth)))
	s.WriteString("\n")

	// Calculate visible range (accounting for header)
	visibleLines := m.height - 7 // Reduced for header
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Build all renderable rows
	var rows []renderRow
	var lastGroup string
	subnets := extractSubnets(m.rawInputs)

	if m.groupBySubnet && len(subnets) > 0 {
		for idx, w := range wrappers {
			stats := getCachedStats(w)
			g := getHostGroup(w.Host(), stats.iprepr, subnets)
			if g != lastGroup {
				rows = append(rows, renderRow{
					isHeader:  true,
					groupName: g,
					hostIndex: -1,
				})
				lastGroup = g
			}
			rows = append(rows, renderRow{
				isHeader:  false,
				groupName: g,
				hostIndex: idx,
			})
		}
	} else {
		for idx := range wrappers {
			rows = append(rows, renderRow{
				isHeader:  false,
				hostIndex: idx,
			})
		}
	}

	// Calculate group totals for live stats header
	groupTotals := make(map[string]int)
	groupOnline := make(map[string]int)
	if m.groupBySubnet && len(subnets) > 0 {
		for _, w := range wrappers {
			stats := getCachedStats(w)
			g := getHostGroup(w.Host(), stats.iprepr, subnets)
			groupTotals[g]++
			isOnline := stats.state && stats.error_message == ""
			if isOnline {
				groupOnline[g]++
			}
		}
	}

	// Adjust scroll offset dynamically based on dynamic headers
	m.adjustScrollForRows(rows)

	start := m.scrollOffset
	end := m.scrollOffset + visibleLines
	if end > len(rows) {
		end = len(rows)
	}

	// Render only visible rows (either headers or hosts)
	for i := start; i < end; i++ {
		row := rows[i]
		if row.isHeader {
			// Render beautiful group header row!
			online := groupOnline[row.groupName]
			total := groupTotals[row.groupName]
			
			headerText := fmt.Sprintf("%s (%d/%d Online) ", row.groupName, online, total)
			textLen := runewidth.StringWidth(headerText)
			
			lineLen := m.width - 4 - textLen
			if lineLen < 5 {
				lineLen = 5
			}
			line := headerText + strings.Repeat("─", lineLen)
			s.WriteString(subnetHeaderStyle.Render(line))
			s.WriteString("\n")
		} else {
			wrapper := wrappers[row.hostIndex]
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
			name = truncateDisplay(name, nameWidth)

			ip := stats.iprepr
			if ip == "" {
				ip = "-"
			}
			ip = truncateDisplay(ip, ipWidth)

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
			if visibleCols[1] {
				lineParts = append(lineParts, displayPad(status, statusWidth))
			}
			if visibleCols[2] {
				lineParts = append(lineParts, displayPad(name, nameWidth))
			}
			if visibleCols[3] {
				lineParts = append(lineParts, displayPad(ip, ipWidth))
			}
			if visibleCols[4] {
				lineParts = append(lineParts, displayPad(rtt, rttWidth))
			}
			if visibleCols[5] {
				lineParts = append(lineParts, displayPad(lastReply, lastReplyWidth))
			}
			if visibleCols[6] {
				lineParts = append(lineParts, truncateDisplay(lastLoss, lastLossWidth))
			}

			line := strings.Join(lineParts, " ")

			if row.hostIndex == m.cursor && m.cursor >= 0 {
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
	}

	// Show scroll indicator if needed
	if len(rows) > visibleLines {
		totalItems := len(rows)
		scrollInfo := fmt.Sprintf(" [%d-%d/%d] ", start+1, end, totalItems)
		s.WriteString(helpStyle.Render(scrollInfo))
	}

	return s.String()
}

func (m *HostListModel) adjustScroll() {
	// Refined dynamically on-the-fly inside renderListView to support dynamic group headers
}

func (m *HostListModel) adjustScrollForRows(rows []renderRow) {
	if m.cursor < 0 {
		m.scrollOffset = 0
		return
	}

	visibleLines := m.height - 7
	if visibleLines < 1 {
		visibleLines = 1
	}

	// Find the cursor's index in the rows slice
	cursorRowIdx := -1
	for idx, r := range rows {
		if !r.isHeader && r.hostIndex == m.cursor {
			cursorRowIdx = idx
			break
		}
	}

	if cursorRowIdx == -1 {
		m.scrollOffset = 0
		return
	}

	// Adjust scrollOffset to keep cursorRowIdx in view
	if cursorRowIdx < m.scrollOffset {
		m.scrollOffset = cursorRowIdx
	} else if cursorRowIdx >= m.scrollOffset+visibleLines {
		m.scrollOffset = cursorRowIdx - visibleLines + 1
	}

	// Clamp scrollOffset
	maxOffset := len(rows) - visibleLines
	if maxOffset < 0 {
		maxOffset = 0
	}
	if m.scrollOffset > maxOffset {
		m.scrollOffset = maxOffset
	}
	if m.scrollOffset < 0 {
		m.scrollOffset = 0
	}
}

// getFilteredWrappers is the single source of truth for which hosts appear in
// the list view. Cache invariant: cachedWrappers is valid iff cacheInvalidated
// is false AND cachedWrappers is non-nil. The cache is invalidated by:
//   - updateStatsCache() (set true after every tick that refreshes stats)
//   - filter/sort mode changes (in Update() handlers)
//   - ReplaceHosts (clears and re-seeds)
//
// Reading this twice per frame (once for the list, once for the dashboard) is
// intentional — both views must agree on what's visible.
func (m *HostListModel) getFilteredWrappers(wrappers []PingWrapperInterface, getCachedStats func(PingWrapperInterface) PWStats) []PingWrapperInterface {
	// Return cached result if valid
	if !m.cacheInvalidated && m.cachedWrappers != nil {
		return m.cachedWrappers
	}

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

	// Sort & Group
	if m.groupBySubnet {
		subnets := extractSubnets(m.rawInputs)
		if len(subnets) > 0 {
			// Partition by group
			groupMap := make(map[string][]PingWrapperInterface)
			for _, wrapper := range filtered {
				stats := getCachedStats(wrapper)
				g := getHostGroup(wrapper.Host(), stats.iprepr, subnets)
				groupMap[g] = append(groupMap[g], wrapper)
			}

			// Sort each group individually
			for _, gHosts := range groupMap {
				m.sortWrappersSlice(gHosts, getCachedStats)
			}

			// Gather groups in order
			var orderedGroups []string
			for _, sub := range subnets {
				orderedGroups = append(orderedGroups, sub.CIDRStr)
			}
			orderedGroups = append(orderedGroups, "Standalone Hosts")

			// Flatten back into filtered slice
			var flattened []PingWrapperInterface
			for _, gName := range orderedGroups {
				if gHosts, ok := groupMap[gName]; ok {
					flattened = append(flattened, gHosts...)
				}
			}
			filtered = flattened
		} else {
			m.sortWrappersSlice(filtered, getCachedStats)
		}
	} else {
		m.sortWrappersSlice(filtered, getCachedStats)
	}

	// Update cache
	m.cachedWrappers = filtered
	m.cacheInvalidated = false

	return filtered
}

func (m *HostListModel) sortWrappersSlice(slice []PingWrapperInterface, getCachedStats func(PingWrapperInterface) PWStats) {
	sortWrappersSliceGeneric(slice, m.sortMode, getCachedStats)
}

func applyFilterAndSort(
	wrappers []PingWrapperInterface,
	filter FilterMode,
	sortMode SortMode,
	hidden map[string]bool,
	getCachedStats func(PingWrapperInterface) PWStats,
) []PingWrapperInterface {
	var filtered []PingWrapperInterface

	for _, wrapper := range wrappers {
		if hidden[wrapper.Host()] {
			continue
		}

		stats := getCachedStats(wrapper)
		isOnline := stats.state && stats.error_message == ""
		seen := stats.has_ever_received

		switch filter {
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

	sortWrappersSliceGeneric(filtered, sortMode, getCachedStats)

	return filtered
}

func sortWrappersSliceGeneric(slice []PingWrapperInterface, sortMode SortMode, getCachedStats func(PingWrapperInterface) PWStats) {
	switch sortMode {
	case SortByName:
		sort.Slice(slice, func(i, j int) bool {
			statsI := getCachedStats(slice[i])
			statsJ := getCachedStats(slice[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			if onlineI != onlineJ {
				return onlineI
			}

			nameI := statsI.GetHostRepr()
			nameJ := statsJ.GetHostRepr()
			if nameI == "" {
				nameI = slice[i].Host()
			}
			if nameJ == "" {
				nameJ = slice[j].Host()
			}
			return nameI < nameJ
		})
	case SortByStatus:
		sort.Slice(slice, func(i, j int) bool {
			statsI := getCachedStats(slice[i])
			statsJ := getCachedStats(slice[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""
			if onlineI != onlineJ {
				return onlineI
			}
			return slice[i].Host() < slice[j].Host()
		})
	case SortByRTT:
		sort.Slice(slice, func(i, j int) bool {
			statsI := getCachedStats(slice[i])
			statsJ := getCachedStats(slice[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			if onlineI != onlineJ {
				return onlineI
			}

			return statsI.lastrtt < statsJ.lastrtt
		})
	case SortByLastSeen:
		sort.Slice(slice, func(i, j int) bool {
			statsI := getCachedStats(slice[i])
			statsJ := getCachedStats(slice[j])
			onlineI := statsI.state && statsI.error_message == ""
			onlineJ := statsJ.state && statsJ.error_message == ""

			if onlineI != onlineJ {
				return !onlineI
			}

			if !onlineI && !onlineJ {
				if statsI.lastrecv == 0 && statsJ.lastrecv == 0 {
					return slice[i].Host() < slice[j].Host()
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
				nameI = slice[i].Host()
			}
			if nameJ == "" {
				nameJ = slice[j].Host()
			}
			return nameI < nameJ
		})
	case SortByIP:
		sort.Slice(slice, func(i, j int) bool {
			statsI := getCachedStats(slice[i])
			statsJ := getCachedStats(slice[j])
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
			return slice[i].Host() < slice[j].Host()
		})
	}
}

type subnetGroup struct {
	CIDRStr string
	IPNet   *net.IPNet
}

type renderRow struct {
	isHeader   bool
	groupName  string
	hostIndex  int
}

func extractSubnets(rawInputs []string) []subnetGroup {
	var subnets []subnetGroup
	for _, input := range rawInputs {
		trimmed := strings.TrimSpace(input)
		if trimmed == "" {
			continue
		}
		_, ipnet, err := net.ParseCIDR(trimmed)
		if err == nil && ipnet != nil {
			duplicate := false
			for _, s := range subnets {
				if s.CIDRStr == trimmed {
					duplicate = true
					break
				}
			}
			if !duplicate {
				subnets = append(subnets, subnetGroup{
					CIDRStr: trimmed,
					IPNet:   ipnet,
				})
			}
		}
	}
	return subnets
}

func getHostGroup(host string, iprepr string, subnets []subnetGroup) string {
	if len(subnets) == 0 {
		return ""
	}

	if iprepr != "" {
		ip := net.ParseIP(iprepr)
		if ip != nil {
			for _, sub := range subnets {
				if sub.IPNet.Contains(ip) {
					return sub.CIDRStr
				}
			}
		}
	}

	ip := net.ParseIP(host)
	if ip != nil {
		for _, sub := range subnets {
			if sub.IPNet.Contains(ip) {
				return sub.CIDRStr
			}
		}
	}

	return "Standalone Hosts"
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

func truncateDisplay(s string, width int) string {
	if width <= 0 {
		return ""
	}
	if runewidth.StringWidth(s) <= width {
		return s
	}
	if width <= 3 {
		return runewidth.Truncate(s, width, "")
	}
	return runewidth.Truncate(s, width, "...")
}

func displayPad(s string, width int) string {
	s = truncateDisplay(s, width)
	padding := width - runewidth.StringWidth(s)
	if padding <= 0 {
		return s
	}
	return s + strings.Repeat(" ", padding)
}
