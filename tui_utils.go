package main

import (
	"net"
	"strings"
)

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

func nextUpdateRate(current UpdateRate) UpdateRate {
	switch current {
	case UpdateRate100ms:
		return UpdateRate1s
	case UpdateRate1s:
		return UpdateRate5s
	case UpdateRate5s:
		return UpdateRate30s
	case UpdateRate30s:
		return UpdateRate100ms
	default:
		return UpdateRate100ms
	}
}

func cloneHiddenHosts(src map[string]bool) map[string]bool {
	dst := make(map[string]bool, len(src))
	for k, v := range src {
		dst[k] = v
	}
	return dst
}

func visibleColumnsList(cols map[int]bool) []int {
	var out []int
	for i := 1; i <= 6; i++ {
		if cols[i] {
			out = append(out, i)
		}
	}
	return out
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
