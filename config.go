package main

import (
	"flag"
)

type Config struct {
	Quiet             bool
	Privileged        bool
	Size              int
	System            bool
	Log               string
	Update            bool
	SystemPingOptions string
	Tui               bool
	NoTui             bool
	HostFile          string
	WebPort           int
	PprofAddr         string
	Once              bool
	OnlyOnline        bool
	OnlyOffline       bool
	Debug             bool
	NoDNS             bool
	Args              []string
}

func LoadConfig() *Config {
	c := &Config{}

	flag.BoolVar(&c.Privileged, "privileged", false, "switch to privileged mode (default if run as root or on windows; ineffective with '-s')")
	flag.IntVar(&c.Size, "size", 24, "pure-go ICMP packet size (without header's 28 Bytes (note: values to test common limits: 1472 or 8972))\nnot relevant for system's ping, refer to system's ping man page and ping-options option")
	flag.BoolVar(&c.System, "s", false, "uses system's ping")
	flag.StringVar(&c.SystemPingOptions, "ping-options", "", "quoted options to provide to system's ping (ex: \"-Q 2\"), implies '-s', refer to system's ping man page")
	flag.BoolVar(&c.Quiet, "q", false, "quiet mode, disable live update")
	flag.StringVar(&c.Log, "log", "", "transition log `filename`")
	flag.BoolVar(&c.Update, "update", false, "check and update to latest version (source github)")
	flag.BoolVar(&c.Tui, "tui", true, "use interactive TUI mode (default) (deprecated, use -notui)")
	flag.BoolVar(&c.NoTui, "notui", false, "disable interactive TUI mode")
	flag.StringVar(&c.HostFile, "hostfile", "", "file with hosts (one per line, CIDR allowed)")
	flag.IntVar(&c.WebPort, "web-port", 8080, "port for web status server in TUI mode (0 to disable)")
	flag.StringVar(&c.PprofAddr, "pprof", "", "start pprof http server at this addr (e.g., localhost:6060); disabled by default")
	flag.BoolVar(&c.Once, "once", false, "ping once and exit")
	flag.BoolVar(&c.OnlyOnline, "only-online", false, "show only online hosts (initial filter)")
	flag.BoolVar(&c.OnlyOffline, "only-offline", false, "show only offline hosts (initial filter)")
	flag.BoolVar(&c.Debug, "debug", false, "enable debug output")
	flag.BoolVar(&c.NoDNS, "no-dns", false, "skip reverse DNS lookups (faster startup for large subnets)")

	flag.Usage = usage
	flag.Parse()

	c.Args = flag.Args()

	return c
}

// usage is moved here or imported from main if exported.
// Since usage() uses VersionStringLong which is in main.go, we might have a cycle if we are not careful.
// But they are in the same package 'main', so it's fine.
