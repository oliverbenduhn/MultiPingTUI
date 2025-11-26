package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
	"github.com/pterm/pterm"
)

// ExpandCIDR takes a CIDR string (e.g. "192.168.1.0/24") and returns a list of all IPs in that subnet.
// It returns nil if the string is not a valid CIDR.
func ExpandCIDR(cidr string) ([]string, error) {
	ip, ipnet, err := net.ParseCIDR(cidr)
	if err != nil {
		return nil, err
	}

	var ips []string
	for ip := ip.Mask(ipnet.Mask); ipnet.Contains(ip); inc(ip) {
		ips = append(ips, ip.String())
	}

	// Remove network and broadcast addresses if applicable (simple heuristic)
	if len(ips) > 2 {
		ips = ips[1 : len(ips)-1]
	}
	return ips, nil
}

type OnceResult struct {
	IP       string
	Hostname string
	Status   string
}

func RunPingOnce(hosts []string, onlyOnline, onlyOffline bool, logFile string) {
	fmt.Printf("Pinging %d targets...\n", len(hosts))

	var wg sync.WaitGroup
	results := make(chan OnceResult, len(hosts))

	// Limit concurrency to avoid file descriptor limits
	sem := make(chan struct{}, 100)

	for _, host := range hosts {
		wg.Add(1)
		go func(target string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			// Simple heuristic: if it looks like an IP, use it directly, otherwise let pinger resolve it
			// But pro-bing handles resolution.
			// However, for our "ping once" mode, we want to be robust.

			pinger, err := probing.NewPinger(target)
			if err != nil {
				if !onlyOnline {
					results <- OnceResult{IP: target, Hostname: "-", Status: fmt.Sprintf("Error (%v)", err)}
				}
				return
			}

			pinger.Count = 1
			pinger.Timeout = 1 * time.Second
			pinger.SetPrivileged(true) // Try privileged first
			if runtime.GOOS == "linux" {
				pinger.SetDoNotFragment(true)
			}

			// Fallback for unprivileged if needed
			if runtime.GOOS != "windows" && os.Getuid() != 0 {
				pinger.SetPrivileged(false)
			}

			err = pinger.Run()
			if err != nil {
				if !onlyOnline {
					results <- OnceResult{IP: target, Hostname: "-", Status: fmt.Sprintf("Error (%v)", err)}
				}
				return
			}

			// Get resolved IP address
			ipAddrObj := pinger.IPAddr()
			ipAddr := ipAddrObj.String()

			// Perform reverse DNS lookup (with timeout)
			hostname := "-"
			if !SkipDNS {
				hostname = hostDisplayName(target, ipAddrObj)
			}
			// If hostname is same as IP, show "-" for cleaner output
			if hostname == ipAddr || hostname == target {
				hostname = "-"
			}

			if pinger.Statistics().PacketsRecv > 0 {
				if !onlyOffline {
					results <- OnceResult{IP: ipAddr, Hostname: hostname, Status: "Online"}
				}
			} else {
				if !onlyOnline {
					results <- OnceResult{IP: ipAddr, Hostname: hostname, Status: "Offline"}
				}
			}
		}(host)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	// Collect and sort results for consistent output
	var resultList []OnceResult
	for res := range results {
		resultList = append(resultList, res)
	}

	// Write to log file if specified
	if logFile != "" {
		f, err := os.Create(logFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error creating log file: %v\n", err)
		} else {
			defer f.Close()

			// Create JSON output structure for Ansible compatibility
			type HostEntry struct {
				IP       string `json:"ip"`
				Hostname string `json:"hostname"`
				Status   string `json:"status"`
				Online   bool   `json:"online"`
			}

			type JSONOutput struct {
				Timestamp string      `json:"timestamp"`
				Total     int         `json:"total"`
				Online    int         `json:"online_count"`
				Offline   int         `json:"offline_count"`
				Hosts     []HostEntry `json:"hosts"`
			}

			output := JSONOutput{
				Timestamp: time.Now().Format(time.RFC3339),
				Total:     len(resultList),
				Hosts:     make([]HostEntry, 0, len(resultList)),
			}

			// Convert results to structured format
			for _, res := range resultList {
				online := res.Status == "Online"
				if online {
					output.Online++
				} else {
					output.Offline++
				}

				hostname := res.Hostname
				if hostname == "-" {
					hostname = ""
				}

				output.Hosts = append(output.Hosts, HostEntry{
					IP:       res.IP,
					Hostname: hostname,
					Status:   res.Status,
					Online:   online,
				})
			}

			// Write pretty-printed JSON
			encoder := json.NewEncoder(f)
			encoder.SetIndent("", "  ")
			if err := encoder.Encode(output); err != nil {
				fmt.Fprintf(os.Stderr, "Error writing JSON: %v\n", err)
			} else {
				fmt.Fprintf(os.Stderr, "Results written to %s (JSON format, %d online, %d offline)\n",
					logFile, output.Online, output.Offline)
			}
		}
	}

	// Print header with color
	headerStyle := pterm.NewStyle(pterm.FgLightCyan, pterm.Bold)
	headerStyle.Printf("%-15s", "IP Address")
	fmt.Print(" │ ")
	headerStyle.Printf("%-40s", "Hostname")
	fmt.Print(" │ ")
	headerStyle.Println("Status")

	pterm.Println(pterm.LightBlue("────────────────┼──────────────────────────────────────────┼──────────"))

	// Print results with colors
	for _, res := range resultList {
		// Color IP address in cyan
		pterm.FgCyan.Printf("%-15s", res.IP)
		fmt.Print(" │ ")

		// Color hostname (or show gray dash if none)
		if res.Hostname == "-" {
			pterm.FgGray.Printf("%-40s", res.Hostname)
		} else {
			pterm.FgLightBlue.Printf("%-40s", res.Hostname)
		}
		fmt.Print(" │ ")

		// Color status based on state
		switch {
		case res.Status == "Online":
			pterm.FgGreen.Println("✓ Online")
		case res.Status == "Offline":
			pterm.FgRed.Println("✗ Offline")
		default:
			pterm.FgYellow.Println("⚠ " + res.Status)
		}
	}
}

func inc(ip net.IP) {
	for j := len(ip) - 1; j >= 0; j-- {
		ip[j]++
		if ip[j] > 0 {
			break
		}
	}
}
