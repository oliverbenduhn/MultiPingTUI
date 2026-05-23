package main

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

type tracerouteResult struct {
	host   string
	seq    int
	target string
	output string
	err    error
	took   time.Duration
}

func buildTracerouteCommand(target string) (string, []string, error) {
	if runtime.GOOS == "windows" {
		if _, err := exec.LookPath("tracert"); err != nil {
			return "", nil, errors.New("tracert not found in PATH")
		}
		return "tracert", []string{"-d", "-h", "15", "-w", "1000", target}, nil
	}

	if _, err := exec.LookPath("traceroute"); err == nil {
		return "traceroute", []string{"-n", "-m", "15", "-w", "1", "-q", "1", target}, nil
	}
	if _, err := exec.LookPath("tracepath"); err == nil {
		return "tracepath", []string{"-n", target}, nil
	}

	return "", nil, errors.New("traceroute/tracepath not found in PATH")
}

func isValidTracerouteTarget(target string) bool {
	t := strings.TrimSpace(target)
	if t == "" {
		return false
	}
	// Target cannot start with a hyphen to prevent flag injection.
	if strings.HasPrefix(t, "-") {
		return false
	}
	// Allow only safe RFC-compliant characters for hostnames/IPs.
	for _, r := range t {
		if !((r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' || r == ':' || r == '[' || r == ']') {
			return false
		}
	}
	return true
}

func runTraceroute(ctx context.Context, host, target string, seq int) tracerouteResult {
	start := time.Now()
	if !isValidTracerouteTarget(target) {
		return tracerouteResult{
			host:   host,
			seq:    seq,
			target: target,
			err:    errors.New("invalid traceroute target"),
			took:   0,
		}
	}

	cmdName, args, err := buildTracerouteCommand(target)
	if err != nil {
		return tracerouteResult{host: host, seq: seq, target: target, err: err}
	}

	cmd := exec.CommandContext(ctx, cmdName, args...)
	out, runErr := cmd.CombinedOutput()
	took := time.Since(start)

	if ctx.Err() != nil {
		// Preserve partial output if we timed out.
		runErr = fmt.Errorf("%w (timeout after %s)", ctx.Err(), took.Round(time.Second))
	}

	return tracerouteResult{
		host:   host,
		seq:    seq,
		target: target,
		output: string(out),
		err:    runErr,
		took:   took,
	}
}
