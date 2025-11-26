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

var Version = "v1.0.5"
var CommitHash = "dev"
var BuildTimestamp = "1970-01-01T00:00:00"
var Builder = "go version go1.xx.y os/platform"
var DebugMode = false
var SkipDNS = false

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
	options := Options{}
	options.privileged = flag.Bool("privileged", false, "switch to privileged mode (default if run as root or on windows; ineffective with '-s')")
	options.size = flag.Int("size", 24, "pure-go ICMP packet size (without header's 28 Bytes (note: values to test common limits: 1472 or 8972))\nnot relevant for system's ping, refer to system's ping man page and ping-options option")
	options.system = flag.Bool("s", false, "uses system's ping")
	options.system_ping_options = flag.String("ping-options", "", "quoted options to provide to system's ping (ex: \"-Q 2\"), implies '-s', refer to system's ping man page")
	options.quiet = flag.Bool("q", false, "quiet mode, disable live update")
	options.log = flag.String("log", "", "transition log `filename`")
	options.update = flag.Bool("update", false, "check and update to latest version (source github)")
	options.tui = flag.Bool("tui", true, "use interactive TUI mode (default) (deprecated, use -notui)")
	options.notui = flag.Bool("notui", false, "disable interactive TUI mode")
	options.hostfile = flag.String("hostfile", "", "file with hosts (one per line, CIDR allowed)")
	options.webPort = flag.Int("web-port", 8080, "port for web status server in TUI mode (0 to disable)")
	options.pprofAddr = flag.String("pprof", "", "start pprof http server at this addr (e.g., localhost:6060); disabled by default")
	once := flag.Bool("once", false, "ping once and exit")
	onlyOnline := flag.Bool("only-online", false, "show only online hosts (initial filter)")
	onlyOffline := flag.Bool("only-offline", false, "show only offline hosts (initial filter)")
	debug := flag.Bool("debug", false, "enable debug output")
	noDNS := flag.Bool("no-dns", false, "skip reverse DNS lookups (faster startup for large subnets)")
	flag.Usage = usage
	flag.Parse()

	if *debug {
		DebugMode = true
	}

	if *noDNS {
		SkipDNS = true
	}

	if *options.notui {
		*options.tui = false
	}

	if *options.pprofAddr != "" {
		go startPprof(*options.pprofAddr)
	}

	var rawHosts []string
	if *options.hostfile != "" {
		fileHosts, err := loadHostsFromFile(*options.hostfile)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error reading host file: %v\n", err)
			os.Exit(1)
		}
		rawHosts = append(rawHosts, fileHosts...)
	}
	rawHosts = append(rawHosts, flag.Args()...)
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

	if *options.update {
		selfUpdate()
		return
	}

	if *once {
		if len(hosts) == 0 {
			fmt.Println("no host provided")
			return
		}
		RunPingOnce(hosts, *onlyOnline, *onlyOffline, *options.log)
		return
	}

	if len(*options.system_ping_options) > 0 {
		*options.system = true
	}

	if len(hosts) == 0 && !*options.tui {
		fmt.Println("no host provided")
		return
	}

	quitSig := make(chan bool)
	quitFlag := false

	transition_writer := &TransitionWriter{}
	if *options.log != "" {
		transition_writer.Init(*options.log, &quitFlag)
		defer transition_writer.Close()
	}

	wh := &WrapperHolder{}
	wh.InitHosts(hosts, options, transition_writer)

	// TUI mode (default, interactive)
	if *options.tui && !*options.quiet {
		initialFilter := determineInitialFilter(*onlyOnline, *onlyOffline)
		err := RunTUI(wh, transition_writer, initialFilter, *options.webPort)
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

	if !*options.quiet {
		display := NewDisplay(wh)
		display.SetFilter(*onlyOnline, *onlyOffline)
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
