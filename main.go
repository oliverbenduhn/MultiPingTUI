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
	"time"
)

var Version = "v1.0.6"
var CommitHash = "dev"
var BuildTimestamp = "1970-01-01T00:00:00"
var Builder = "go version go1.xx.y os/platform"
var DebugMode = false
var SkipDNS = false

// Options struct is replaced by Config in config.go, but we need to keep Options for compatibility 
// with WrapperHolder.InitHosts signature if we don't change it.
// However, I should update WrapperHolder to use Config or keep Options as an alias/adapter.
// For now, let's adapt Config to Options or update WrapperHolder.
// WrapperHolder.InitHosts takes Options. Let's update WrapperHolder to take Config.

// But wait, I can't change WrapperHolder in this tool call.
// I will define Options here as a type alias or just struct matching Config fields if needed, 
// OR I will update WrapperHolder in the next step.
// Actually, I can just update main to use Config, and create an Options struct that matches what WrapperHolder expects
// populated from Config.
// The original Options struct had pointers.
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

	if config.PprofAddr != "" {
		go startPprof(config.PprofAddr)
	}

	var rawHosts []string
	if config.HostFile != "" {
		fileHosts, err := loadHostsFromFile(config.HostFile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading host file: %v\n", err)
			os.Exit(1)
		}
		rawHosts = append(rawHosts, fileHosts...)
	}
	rawHosts = append(rawHosts, config.Args...)
	var hosts []string

	for _, arg := range rawHosts {
		// Try to expand as CIDR
		ips, err := ExpandCIDR(arg)
		if err == nil {
			if DebugMode {
				fmt.Fprintf(os.Stderr, "DEBUG: Expanded %s to %d IPs\n", arg, len(ips))
			}
			hosts = append(hosts, ips...)
		} else {
			// Not a CIDR, treat as single host
			hosts = append(hosts, arg)
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

	quitSig := make(chan bool)
	quitFlag := false

	transition_writer := &TransitionWriter{}
	if config.Log != "" {
		transition_writer.Init(config.Log, &quitFlag)
		defer transition_writer.Close()
	}

	// Adapter for WrapperHolder which expects Options with pointers
	// This is temporary until we refactor WrapperHolder to use Config
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
	}

	wh := &WrapperHolder{}
	wh.InitHosts(hosts, options, transition_writer)

	// TUI mode (default, interactive)
	if config.Tui && !config.Quiet {
		initialFilter := determineInitialFilter(config.OnlyOnline, config.OnlyOffline)
		wh.Start()
		wh.StartPeriodicDNSUpdates() // Start periodic DNS updates after wrappers are started
		err := RunTUI(wh, transition_writer, initialFilter, config.WebPort)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Error running TUI: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Legacy display mode
	c := make(chan os.Signal, 1)
	signal.Notify(c, os.Interrupt)
	go func() {
		<-c
		wh.Stop()
		quitFlag = true
		quitSig <- true
	}()

	wh.Start()
	wh.StartPeriodicDNSUpdates() // Start periodic DNS updates after wrappers are started

	if !config.Quiet {
		display := NewDisplay(wh)
		display.SetFilter(config.OnlyOnline, config.OnlyOffline)
		display.Start()

		for !quitFlag {
			display.Update()
			time.Sleep(100 * time.Millisecond)
		}

		display.Stop()
	} else {
		fmt.Print(VersionString())
		for !quitFlag {
			wh.CalcStats(2 * 1e9)
			time.Sleep(100 * time.Millisecond)
		}
	}

	<-quitSig

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
		line := strings.TrimSpace(scanner.Text())
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
