package main

import (
	"fmt"
	"net"
	"os"
	"runtime"
	"sync"
	"time"

	probing "github.com/prometheus-community/pro-bing"
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

func RunPingOnce(hosts []string, onlyOnline, onlyOffline bool) {
	fmt.Printf("Pinging %d targets...\n", len(hosts))

	var wg sync.WaitGroup
	results := make(chan string, len(hosts))

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
					results <- fmt.Sprintf("%s: Error (%v)", target, err)
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
					results <- fmt.Sprintf("%s: Error (%v)", target, err)
				}
				return
			}

			if pinger.Statistics().PacketsRecv > 0 {
				if !onlyOffline {
					results <- fmt.Sprintf("%s: Online", target)
				}
			} else {
				if !onlyOnline {
					results <- fmt.Sprintf("%s: Offline", target)
				}
			}
		}(host)
	}

	go func() {
		wg.Wait()
		close(results)
	}()

	for res := range results {
		fmt.Println(res)
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
