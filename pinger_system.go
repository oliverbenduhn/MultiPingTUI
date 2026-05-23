package main

import (
	"bufio"
	"fmt"
	"net"
	"os"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"
)

type SystemPingWrapper struct {
	host         string
	ip           *net.IPAddr
	hstring      string
	stats        *PWStats
	cmd          *exec.Cmd
	ping_options string
	mu           sync.RWMutex
}

var time_extractor = regexp.MustCompile(`time[=<]([\d\.]+) *(.?s)`)
var time_extractor_non_local = regexp.MustCompile(`[=<]([\d\.]+) *(.?s)`)

func (w *SystemPingWrapper) Start() {
	// Use host as initial display name (DNS lookup happens later via periodic updates)
	displayHost := w.host
	w.hstring = fmt.Sprintf("%s (%s)", displayHost, w.ip.String())
	w.stats.SetHostRepr(displayHost)
	w.stats.iprepr = w.ip.String()

	var path string

	// Looks like an ipv6 ? search for ping6
	// Some systems doesn't have ping6 because ping handle both v4 and v6
	// so not finding ping6 is not necessarily a problem
	if strings.Contains(w.ip.String(), ":") {
		path, _ = exec.LookPath("ping6")
	}

	if path == "" {
		var err error
		path, err = exec.LookPath("ping")
		if err != nil {
			w.mu.Lock()
			w.stats.error_message = err.Error()
			w.mu.Unlock()
			return
		}
	}

	args, err := shlex.Split(w.ping_options)
	if err != nil {
		w.mu.Lock()
		w.stats.error_message = err.Error()
		w.mu.Unlock()
		return
	}

	extractor := time_extractor

	if runtime.GOOS == "windows" {
		args = append(args, "-t")
		extractor = time_extractor_non_local
	}
	args = append(args, w.ip.String())

	w.cmd = exec.Command(path, args...)
	w.cmd.Env = append(w.cmd.Environ(), "LANG=C")
	r, err := w.cmd.StdoutPipe()
	if err != nil {
		w.mu.Lock()
		w.stats.error_message = err.Error()
		w.mu.Unlock()
		return
	}
	scanner := bufio.NewScanner(r)
	if err := w.cmd.Start(); err != nil {
		w.mu.Lock()
		w.stats.error_message = err.Error()
		w.mu.Unlock()
		return
	}

	cmdString := w.cmd.String()
	go func() {
		// Read line by line and process it
		for scanner.Scan() {
			line := scanner.Text()
			extracted := extractor.FindAllStringSubmatch(line, -1)
			if len(extracted) > 0 {
				rtt, rttString := parseSystemPingRTT(extracted[0][1], extracted[0][2])
				w.mu.Lock()
				w.stats.has_ever_received = true
				w.stats.lastrecv = time.Now().UnixNano()
				w.stats.lastrtt = rtt
				w.stats.lastrtt_as_string = rttString
				w.stats.error_message = ""
				w.mu.Unlock()
			}
		}
		err := w.cmd.Wait()
		w.mu.Lock()
		if err != nil {
			w.stats.error_message = fmt.Sprintf("%v exited: %v", cmdString, err)
		} else {
			w.stats.error_message = fmt.Sprintf("%v exited", cmdString)
		}
		w.mu.Unlock()
	}()
}

func (w *SystemPingWrapper) Stop() {
	w.mu.RLock()
	cmd := w.cmd
	w.mu.RUnlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(os.Interrupt)
}

func (w *SystemPingWrapper) Host() string {
	return w.hstring
}

func (w *SystemPingWrapper) CalcStats(timeout_threshold int64) PWStats {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.ComputeState(timeout_threshold)
	return *w.stats
}

func (w *SystemPingWrapper) Stats() *PWStats {
	w.mu.RLock()
	defer w.mu.RUnlock()
	s := *w.stats
	return &s
}

func (w *SystemPingWrapper) SetHostRepr(h string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stats.SetHostRepr(h)
}

func parseSystemPingRTT(value, unit string) (time.Duration, string) {
	unit = strings.TrimSpace(unit)
	if unit == "" {
		unit = "ms"
	}
	raw := strings.TrimSpace(value) + unit
	d, err := time.ParseDuration(raw)
	if err != nil {
		return 0, raw
	}
	return d, round(d, 2).String()
}
