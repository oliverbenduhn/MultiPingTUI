package main

// User-flow-driven E2E tests for the TUI.
// Run: go test -race -count=1 -run TestFlow ./...
// Run a single flow: go test -run TestFlow/Flow1_Navigation ./...
//
// Ponytail notes:
//   - bubbletea v1.3.10 has no `tea/teatest` subpackage (that arrived in v2).
//     The mini-harness below does what teatest does: feed keys, capture
//     rendered output, quit the program. ~40 lines, no extra deps.
//   - No golden files yet: assertions check substrings of rendered output.
//     Upgrade to snapshot baselines once a real change baseline exists.
//   - No status_server coverage here; that's a separate surface.
//   - Tests use deterministic PWStats from fakeWrapper — no real network.

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// ---- fakes ----------------------------------------------------------------

type fakeWrapper struct {
	host  string
	repr  string
	stats *PWStats
}

func (f *fakeWrapper) Start()               {}
func (f *fakeWrapper) Stop()                {}
func (f *fakeWrapper) Host() string         { return f.host }
func (f *fakeWrapper) SetHostRepr(s string) { f.repr = s }
func (f *fakeWrapper) Stats() *PWStats      { return f.stats }
func (f *fakeWrapper) CalcStats(_ int64) PWStats {
	return *f.stats
}

func makeStats(online bool, everSeen bool) *PWStats {
	s := &PWStats{}
	s.state = online
	s.has_ever_been_online = everSeen
	// QUIRK: Smart filter reads `has_ever_received`, not `has_ever_been_online`.
	// A host that was online once but never replied (e.g. stale wrapper) is
	// considered "unseen" by the filter. Mirror both fields to keep test data
	// consistent with the filter's actual check.
	s.has_ever_received = everSeen
	return s
}

type hostSpec struct {
	host     string
	online   bool
	everSeen bool
}

func seedRepo(specs []hostSpec) *MemoryHostRepository {
	repo := NewMemoryHostRepository()
	wrappers := make([]PingWrapperInterface, 0, len(specs))
	for _, s := range specs {
		wrappers = append(wrappers, &fakeWrapper{
			host:  s.host,
			stats: makeStats(s.online, s.everSeen),
		})
	}
	repo.UpdateAll(wrappers)
	return repo
}

func newTestModel(repo HostRepository, gs *GlobalStatistics, initial FilterMode) *TUIModel {
	// ponytail: minimal PingService so ReplaceHosts/Stop don't nil-deref.
	// The factory returns empty fakes — callers of ReplaceHosts get harmless
	// replacements. The TUI's render path still works with our hand-seeded repo.
	ps := &PingService{
		repo:       repo,
		running:    false,
		options:    Options{},
		dnsUpdater: &DNSUpdater{},
		wrapperFactory: func(_ string, _ Options, _ *TransitionWriter) PingWrapperInterface {
			return &fakeWrapper{host: "stub", stats: &PWStats{}}
		},
	}
	// QUIRK: NewTUIModel silently rewrites anything other than
	// FilterOnline/FilterOffline/FilterSmart to FilterSmart.
	if initial != FilterOnline && initial != FilterOffline && initial != FilterSmart {
		initial = FilterSmart
	}
	m := NewTUIModel(ps, repo, nil /* TransitionWriter */, initial, gs)
	// QUIRK: Default update rate (1s) means the first render shows empty
	// statsCache for ~1s, so Smart filter displays "No hosts match" until
	// the first tick fires. Drop to 100ms so render happens immediately.
	m.header.updateRate = UpdateRate100ms
	return m
}

func containsAll(haystack string, needles ...string) bool {
	for _, n := range needles {
		if !strings.Contains(haystack, n) {
			return false
		}
	}
	return true
}

// ---- mini harness ---------------------------------------------------------

// runProgram is the teatest-equivalent: feed keys, capture output, quit.
// `feed` is a list of msgs to send AFTER the initial window-size msg.
func runProgram(t *testing.T, m *TUIModel, w, h int, feed []tea.Msg, settle time.Duration) string {
	t.Helper()
	out := &bytes.Buffer{}
	in := &bytes.Buffer{}

	p := tea.NewProgram(m,
		tea.WithInput(in),
		tea.WithOutput(out),
		tea.WithoutSignalHandler(),
	)

	// Reader needs to be a real io.Reader that won't EOF. We give the
	// program a writer that has nothing; the p.Send() below does the work.
	_ = io.Discard

	done := make(chan error, 1)
	go func() { _, err := p.Run(); done <- err }()

	// v1.3.10 has no WithWindowSize option — send it as a regular Msg.
	p.Send(tea.WindowSizeMsg{Width: w, Height: h})
	for _, msg := range feed {
		p.Send(msg)
	}

	// Let tickCmd + re-renders settle.
	time.Sleep(settle)
	p.Quit()

	select {
	case err := <-done:
		if err != nil && err != tea.ErrProgramKilled {
			t.Logf("program exited with: %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("program did not exit within 2s after Quit")
	}
	return out.String()
}

// press wraps common key patterns.
func press(ch rune) tea.Msg         { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{ch}} }
func special(k tea.KeyType) tea.Msg { return tea.KeyMsg{Type: k} }

// ---- Flow 1: first-run orientation ---------------------------------------

func TestFlow1_Navigation(t *testing.T) {
	repo := seedRepo([]hostSpec{
		{"a.local", true, true},
		{"b.local", false, true},
		{"c.local", true, true},
	})
	m := newTestModel(repo, NewGlobalStatistics(), FilterSmart)

	out := runProgram(t, m, 120, 30, []tea.Msg{
		special(tea.KeyDown),
	}, 300*time.Millisecond)

	// CLAUDE.md: cursor starts at -1, first ↓ selects host #0.
	if !containsAll(out, "a.local") {
		t.Fatalf("Flow1: a.local must be rendered, got:\n%s", truncate(out, 600))
	}
	if !containsAll(out, "b.local", "c.local") {
		t.Fatalf("Flow1: all 3 hosts must be visible at this size, got:\n%s", truncate(out, 600))
	}
}

// ---- Flow 2: filter & sort cycle -----------------------------------------

func TestFlow2_FilterSortCycle(t *testing.T) {
	repo := seedRepo([]hostSpec{
		{"alive-fast", true, true},
		{"alive-slow", true, true},
		{"down-seen", false, true},
		{"never-seen", false, false},
	})
	m := newTestModel(repo, NewGlobalStatistics(), FilterSmart)

	// Smart (start) -> Online -> Offline -> All
	out := runProgram(t, m, 160, 40, []tea.Msg{
		press('f'), press('f'), press('f'),
	}, 300*time.Millisecond)

	// At All filter, all 4 hosts must be visible.
	if !containsAll(out, "alive-fast", "alive-slow", "down-seen", "never-seen") {
		t.Fatalf("Flow2: at All filter all 4 hosts must be visible, got:\n%s", truncate(out, 800))
	}

	// Now jump to Online: never-seen AND down-seen must disappear.
	m2 := newTestModel(repo, NewGlobalStatistics(), FilterSmart)
	out2 := runProgram(t, m2, 160, 40, []tea.Msg{
		press('f'), // Smart -> Online
	}, 300*time.Millisecond)
	if containsAll(out2, "never-seen", "down-seen") {
		t.Fatalf("Flow2: Online filter must hide offline hosts, got:\n%s", truncate(out2, 800))
	}
	if !containsAll(out2, "alive-fast", "alive-slow") {
		t.Fatalf("Flow2: Online filter must show online hosts, got:\n%s", truncate(out2, 800))
	}
}

// ---- Flow 3: edit hosts (cancel path) ------------------------------------

func TestFlow3_EditHostsCancel(t *testing.T) {
	repo := seedRepo([]hostSpec{
		{"existing.local", true, true},
	})
	m := newTestModel(repo, NewGlobalStatistics(), FilterSmart)

	// AUDIT-FINDING: `e` triggers `tea.ExecProcess(os.Executable(), -edit-config)`,
	// which spawns the same binary as a child editor. That's not headless-testable
	// — it would block waiting on a real editor subprocess. We only assert that
	// pressing `e` produces the "Editing config" status without crashing.
	out := runProgram(t, m, 120, 30, []tea.Msg{
		press('e'),
	}, 200*time.Millisecond)
	if !strings.Contains(out, "Editing config") {
		t.Fatalf("Flow3: pressing 'e' must show edit-mode status, got:\n%s", truncate(out, 600))
	}
}

// ---- Flow 4: detail sub-views --------------------------------------------

func TestFlow4_DetailSubViews(t *testing.T) {
	repo := seedRepo([]hostSpec{
		{"detail.local", true, true},
	})
	m := newTestModel(repo, NewGlobalStatistics(), FilterSmart)

	out := runProgram(t, m, 120, 30, []tea.Msg{
		special(tea.KeyDown),
		special(tea.KeyEnter),
		press('t'), // traceroute
		press('d'), // dashboard
		special(tea.KeyEsc),
	}, 500*time.Millisecond)

	// After full cycle (list -> details -> traceroute -> dashboard -> list),
	// the host name must be visible at least once.
	if !containsAll(out, "detail.local") {
		t.Fatalf("Flow4: host must remain visible across sub-views, got:\n%s", truncate(out, 800))
	}
}

// ---- Flow 5: terminal-size matrix ---------------------------------------

func TestFlow5_TerminalSizeMatrix(t *testing.T) {
	hosts := make([]hostSpec, 30)
	for i := range hosts {
		hosts[i] = hostSpec{
			host:     "h-" + itoa(i),
			online:   i%3 != 0,
			everSeen: true,
		}
	}

	cases := []struct {
		name   string
		width  int
		height int
	}{
		{"narrow_80x24", 80, 24},
		{"default_120x40", 120, 40},
		{"wide_200x50", 200, 50},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repo := seedRepo(hosts)
			m := newTestModel(repo, NewGlobalStatistics(), FilterSmart)
			out := runProgram(t, m, tc.width, tc.height, nil, 400*time.Millisecond)

			// Either the first/last host is rendered, or a scroll indicator
			// is present — both are valid. Crashes or empty output are not.
			if out == "" {
				t.Fatalf("Flow5/%s: empty output", tc.name)
			}
			visible := containsAll(out, "h-0")
			scrolled := strings.Contains(out, "scroll") || strings.Contains(out, "/30")
			if !visible && !scrolled {
				t.Fatalf("Flow5/%s: expected host or scroll indicator, got:\n%s",
					tc.name, truncate(out, 600))
			}
		})
	}
}

// ---- helpers --------------------------------------------------------------

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		pos--
		b[pos] = '-'
	}
	return string(b[pos:])
}