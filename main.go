package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	probing "github.com/prometheus-community/pro-bing"
)

var Version = "v1.1.9"
var CommitHash = "dev"
var BuildTimestamp = "1970-01-01T00:00:00"
var Builder = "go version go1.xx.y os/platform"
var DebugMode = false
var SkipDNS = false
var TimeoutThresholdNS int64 = int64(2 * time.Second)

// Options keeps the pinger constructors independent from flag parsing.
type Options struct {
	quiet               *bool
	privileged          *bool
	size                *int
	system              *bool
	log                 *string
	update              *bool
	system_ping_options *string
	tui                 *bool
	notui               *bool
	hostfile            *string
	webPort             *int
	pprofAddr           *string
	adaptiveInterval    *bool
}

func main() {
	config := LoadConfig()

	if config.Debug {
		DebugMode = true
	}

	if config.NoDNS {
		SkipDNS = true
	}

	if config.NoTui {
		config.Tui = false
	}

	// Auto-fallback: if pure Go ping likely lacks permissions (unprivileged user),
	// try a quick probe to detect "permission denied" and switch to system ping (-s).
	// This avoids showing everything as offline when raw ICMP is blocked.
	if !config.System && !config.Privileged && !probePureGoAllowed() {
		fmt.Fprintf(os.Stderr, "permission denied for raw ICMP; falling back to system ping (-s). Use -privileged or setcap to enable pure Go ping.\n")
		config.System = true
	}

	// System ping is slower and can be scheduled irregularly when many hosts are probed in parallel.
	// Use a more forgiving timeout threshold in that case to avoid false "offline" flaps.
	if config.System {
		TimeoutThresholdNS = int64(5 * time.Second)
	} else {
		TimeoutThresholdNS = int64(2 * time.Second)
	}

	// If mping is started without any hosts and there is no config yet, create a commented default.
	if config.Tui && config.HostFile == "" && len(config.Args) == 0 {
		if path, err := userSettingsPath(); err == nil {
			_ = ensureConfigFile(path)
		}
	}

	userSettings, settingsErr := LoadUserSettings()
	if settingsErr != nil {
		fmt.Fprintf(os.Stderr, "warning: invalid config, ignoring (%v)\n", settingsErr)
		userSettings = DefaultUserSettings()
	}

	if config.EditConfig {
		path, err := userSettingsPath()
		if err != nil {
			fmt.Fprintf(os.Stderr, "failed to locate config: %v\n", err)
			os.Exit(1)
		}
		if err := RunConfigEditor(path); err != nil {
			fmt.Fprintf(os.Stderr, "config editor error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	if config.PprofAddr != "" {
		go startPprof(config.PprofAddr)
	}

	var rawHosts []string
	explicitHosts := false
	if config.HostFile != "" {
		fileHosts, err := loadHostsFromFile(config.HostFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading host file: %v\n", err)
			os.Exit(1)
		}
		rawHosts = append(rawHosts, fileHosts...)
		explicitHosts = true
	}
	if len(config.Args) > 0 {
		explicitHosts = true
	}
	rawHosts = append(rawHosts, config.Args...)

	// If no hosts were provided, fall back to persisted settings (TUI mode).
	if len(rawHosts) == 0 && config.Tui {
		rawHosts = append([]string{}, userSettings.Hosts...)
	}

	// Persist explicit host arguments into config for next run.
	if explicitHosts {
		if path, err := userSettingsPath(); err == nil {
			_ = ensureConfigFile(path)
			userSettings.Hosts = append([]string{}, rawHosts...)
			_ = SaveUserSettings(userSettings)
		}
	}
	var hosts []string
	hasSubnet := false

	for _, arg := range rawHosts {
		// Try to expand as CIDR
		ips, err := ExpandCIDR(arg)
		if err == nil {
			if DebugMode {
				fmt.Fprintf(os.Stderr, "DEBUG: Expanded %s to %d IPs\n", arg, len(ips))
			}
			hosts = append(hosts, ips...)
			hasSubnet = true
		} else if errors.Is(err, ErrCIDRTooLarge) {
			fmt.Fprintf(os.Stderr, "error expanding subnet: %v\n", err)
			os.Exit(1)
		} else {
			// Not a CIDR, treat as single host
			hosts = append(hosts, arg)
		}
	}

	// Auto-enable adaptive interval when scanning subnets (unless explicitly disabled)
	if hasSubnet && !config.AdaptiveInterval {
		config.AdaptiveInterval = true
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG: Auto-enabled adaptive interval mode (subnet detected)\n")
		}
	}

	// Adaptive intervals use 10s for never-seen hosts, so timeout must be at least 12s
	// to avoid false offline flapping. Override the earlier system/non-system threshold.
	if config.AdaptiveInterval {
		TimeoutThresholdNS = int64(12 * time.Second)
		if DebugMode {
			fmt.Fprintf(os.Stderr, "DEBUG: Timeout threshold set to 12s (adaptive interval mode)\n")
		}
	}

	if DebugMode {
		fmt.Fprintf(os.Stderr, "DEBUG: Total hosts to ping: %d\n", len(hosts))
	}

	if config.Update {
		selfUpdate()
		return
	}

	if config.Once {
		if len(hosts) == 0 {
			fmt.Println("no host provided")
			return
		}
		RunPingOnce(hosts, config.OnlyOnline, config.OnlyOffline, config.Log)
		return
	}

	if len(config.SystemPingOptions) > 0 {
		config.System = true
	}

	if len(hosts) == 0 && !config.Tui {
		fmt.Println("no host provided")
		return
	}

	quitFlag := false

	transition_writer := &TransitionWriter{}
	if config.Log != "" {
		transition_writer.Init(config.Log, &quitFlag)
		defer transition_writer.Close()
	}

	options := Options{
		quiet:               &config.Quiet,
		privileged:          &config.Privileged,
		size:                &config.Size,
		system:              &config.System,
		log:                 &config.Log,
		update:              &config.Update,
		system_ping_options: &config.SystemPingOptions,
		tui:                 &config.Tui,
		notui:               &config.NoTui,
		hostfile:            &config.HostFile,
		webPort:             &config.WebPort,
		pprofAddr:           &config.PprofAddr,
		adaptiveInterval:    &config.AdaptiveInterval,
	}

	// Initialize Repository and Service
	repo := NewMemoryHostRepository()
	ps := NewPingService(repo, options, transition_writer)
	ps.InitHosts(hosts)

	globalStats := NewGlobalStatistics()

	// TUI mode (default, interactive)
	if config.Tui && !config.Quiet {
		initialFilter := determineInitialFilter(config.OnlyOnline, config.OnlyOffline)
		if !config.OnlyOnline && !config.OnlyOffline && validFilterMode(userSettings.View.Filter) {
			initialFilter = userSettings.View.Filter
		}
		err := RunTUI(ps, repo, transition_writer, initialFilter, config.WebPort, globalStats, userSettings, rawHosts)
		if err != nil {
			fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
			os.Exit(1)
		}
		return
	} else {
		ps.Start()

		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, os.Interrupt, syscall.SIGTERM)
		defer signal.Stop(sigChan)

		if !config.Quiet {
			display := NewDisplay(repo)
			display.SetFilter(config.OnlyOnline, config.OnlyOffline)
			display.Start()
			ticker := time.NewTicker(100 * time.Millisecond)

			for !quitFlag {
				select {
				case <-sigChan:
					quitFlag = true
				case <-ticker.C:
					display.Update()
				}
			}

			ticker.Stop()
			display.Stop()
		} else {
			fmt.Print(VersionString())
			ticker := time.NewTicker(100 * time.Millisecond)
			defer ticker.Stop()
			for !quitFlag {
				select {
				case <-sigChan:
					quitFlag = true
				case <-ticker.C:
				}
			}
		}
		ps.Stop()
	}
}

func VersionString() string {
	return fmt.Sprintf("mping %v-%v\n", Version, CommitHash)
}

func VersionStringLong() string {
	return fmt.Sprintf("mping %v-%v (MultiPingTUI, built on %v using %v)\nhttps://github.com/oliverbenduhn/MultiPingTUI\n\n", Version, CommitHash, BuildTimestamp, Builder)
}

func determineInitialFilter(onlyOnline, onlyOffline bool) FilterMode {
	switch {
	case onlyOnline && !onlyOffline:
		return FilterOnline
	case onlyOffline && !onlyOnline:
		return FilterOffline
	default:
		return FilterAll
	}
}

// probePureGoAllowed returns false if a one-off pure Go ping fails due to permissions.
func probePureGoAllowed() bool {
	pinger, err := probing.NewPinger("127.0.0.1")
	if err != nil {
		return true // don't block if we can't construct
	}
	pinger.Count = 1
	pinger.Timeout = 500 * time.Millisecond
	pinger.Interval = 100 * time.Millisecond
	pinger.SetPrivileged(false)
	if err := pinger.Run(); err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "permission") || strings.Contains(msg, "operation not permitted") {
			return false
		}
	}
	return true
}

func usage() {
	fmt.Print(VersionStringLong())
	fmt.Fprintf(flag.CommandLine.Output(), "Usage of %s:\n", os.Args[0])
	flag.PrintDefaults()
	fmt.Println(`  host [hosts...]

Hosts can have the following form:
- hostname or ip or ip://hostname => ping (implementation used depends on '-s' flag)
- tcp://hostname:port or tcp://[ipv6]:port => tcp probing
    While using ip addresses, tcp:// can take IPv4 or IPv6 (w/ brackets), tcp4:// can only take IPv4 and tcp6:// only IPv6 (w/ brackets)

Hint on address family can be provided with the following form:
- ip://hostname and tcp://hostname resolves as default
- ip4://hostname and tcp4://hostname resolves as IPv4
- ip6://hostname and tcp6://hostname resolves as IPv6

Notes about implementation: tcp implementation between probing (S/SA/R) and full handshake depends on the platform`)
}

func loadHostsFromFile(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	var hosts []string
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(stripYAMLComment(scanner.Text()))
		if line == "" {
			continue
		}
		hosts = append(hosts, line)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return hosts, nil
}

// startPprof launches a pprof HTTP server on the given address.
func startPprof(addr string) {
	fmt.Fprintf(os.Stderr, "pprof listening on http://%s/debug/pprof/\n", addr)
	if err := http.ListenAndServe(addr, nil); err != nil && !errors.Is(err, http.ErrServerClosed) {
		fmt.Fprintf(os.Stderr, "pprof server error: %v\n", err)
	}
}
