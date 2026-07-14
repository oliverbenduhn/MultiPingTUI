package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

type countingWrapper struct {
	host  string
	count int
	stats PWStats
}

func (w *countingWrapper) Start()             {}
func (w *countingWrapper) Stop()              {}
func (w *countingWrapper) Host() string       { return w.host }
func (w *countingWrapper) Stats() *PWStats    { s := w.stats; return &s }
func (w *countingWrapper) SetHostRepr(string) {}
func (w *countingWrapper) CalcStats(int64) PWStats {
	w.count++
	return w.stats
}

func TestStopBeforeStartDoesNotPanic(t *testing.T) {
	assertNoPanic := func(name string, fn func()) {
		t.Helper()
		defer func() {
			if r := recover(); r != nil {
				t.Fatalf("%s panicked: %v", name, r)
			}
		}()
		fn()
	}

	assertNoPanic("ProbingWrapper", func() {
		(&ProbingWrapper{}).Stop()
	})
	assertNoPanic("SystemPingWrapper", func() {
		(&SystemPingWrapper{}).Stop()
	})
	assertNoPanic("TCPPingWrapper", func() {
		(&TCPPingWrapper{}).Stop()
	})
}

func TestTUITickUpdatesStatsCacheOnce(t *testing.T) {
	repo := NewMemoryHostRepository()
	wrapper := &countingWrapper{
		host: "example",
		stats: PWStats{
			hrepr:  "example",
			iprepr: "127.0.0.1",
		},
	}
	repo.UpdateAll([]PingWrapperInterface{wrapper})

	model := NewTUIModel(nil, repo, nil, FilterAll, NewGlobalStatistics())
	before := wrapper.count
	// Init does a one-shot pre-warm of statsCache so the first View() shows
	// data immediately (B2 fix). Each tick must do exactly one update on top.
	model.lastTickTime = time.Now().Add(-time.Second)
	_, _ = model.Update(tickMsg(time.Now()))

	if wrapper.count != before+1 {
		t.Fatalf("expected one CalcStats call per tick (was %d before, %d after tick)",
			before, wrapper.count)
	}
}

func TestDashboardScriptDoesNotInjectHostHTML(t *testing.T) {
	repo := NewMemoryHostRepository()
	server := &StatusServer{
		repo:          repo,
		statsProvider: func(PingWrapperInterface) PWStats { return PWStats{} },
		view:          ServerView{Cols: []int{1, 2, 3, 4, 5, 6}},
		globalStats:   NewGlobalStatistics(),
	}

	req := httptest.NewRequest("GET", "/dashboard", nil)
	rec := httptest.NewRecorder()
	server.dashboardHtmlHandler(rec, req)
	body := rec.Body.String()

	for _, unsafePattern := range []string{
		"+ t.Host +",
		"+ h.host +",
		"+ h.rtt +",
		"+ h.last_reply +",
	} {
		if strings.Contains(body, unsafePattern) {
			t.Fatalf("dashboard still contains unsafe host HTML interpolation pattern %q", unsafePattern)
		}
	}
	if !strings.Contains(body, "appendText(div, 'span', h.host") {
		t.Fatalf("dashboard script should render host values through textContent helper")
	}
}

func TestTCPPingStopIsIdempotent(t *testing.T) {
	w := &TCPPingWrapper{
		loopTicker: time.NewTicker(time.Hour),
		stopChan:   make(chan struct{}),
	}
	w.Stop()
	w.Stop()
}

func TestExpandCIDRRejectsHugeNetworks(t *testing.T) {
	_, err := ExpandCIDR("10.0.0.0/8")
	if !errors.Is(err, ErrCIDRTooLarge) {
		t.Fatalf("expected ErrCIDRTooLarge, got %v", err)
	}

	ips, err := ExpandCIDR("192.0.2.0/30")
	if err != nil {
		t.Fatalf("unexpected /30 error: %v", err)
	}
	if len(ips) != 2 {
		t.Fatalf("expected 2 usable hosts for /30, got %d", len(ips))
	}
}

func TestViewHandlerSanitizesHiddenHosts(t *testing.T) {
	repo := NewMemoryHostRepository()
	repo.UpdateAll([]PingWrapperInterface{
		&countingWrapper{host: "known"},
	})
	server := &StatusServer{
		repo:   repo,
		view:   ServerView{Hidden: map[string]bool{}},
		traces: make(map[string]*webTraceState),
	}

	body := bytes.NewBufferString(`{"hidden":{"known":true,"unknown":true,"":true,"known-false":false}}`)
	req := httptest.NewRequest(http.MethodPost, "/view", body)
	rec := httptest.NewRecorder()
	server.viewHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var view ServerView
	if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
		t.Fatalf("decode view: %v", err)
	}
	if len(view.Hidden) != 1 || !view.Hidden["known"] {
		t.Fatalf("expected only known hidden host, got %#v", view.Hidden)
	}
}

func TestTUITruncateDisplayPreservesUTF8(t *testing.T) {
	got := truncateDisplay("münchen.example", 8)
	if !utf8.ValidString(got) {
		t.Fatalf("expected valid UTF-8, got %q", got)
	}
	if got == "münchen.example" {
		t.Fatalf("expected truncation, got %q", got)
	}
}

func TestParseSystemPingRTT(t *testing.T) {
	got, label := parseSystemPingRTT("12.34", "ms")
	if got != 12340*time.Microsecond {
		t.Fatalf("expected 12.34ms, got %s", got)
	}
	if label != "12.34ms" {
		t.Fatalf("expected rounded label 12.34ms, got %q", label)
	}

	got, label = parseSystemPingRTT("1", "s")
	if got != time.Second {
		t.Fatalf("expected 1s, got %s", got)
	}
	if label != "1s" {
		t.Fatalf("expected label 1s, got %q", label)
	}
}

func TestDashboardAggregatesRTTByDuration(t *testing.T) {
	repo := NewMemoryHostRepository()
	fast := &countingWrapper{host: "fast"}
	slow := &countingWrapper{host: "slow"}
	noRTT := &countingWrapper{host: "nortt"}
	repo.UpdateAll([]PingWrapperInterface{fast, slow, noRTT})

	stats := map[string]PWStats{
		"fast": {
			state:             true,
			has_ever_received: true,
			lastrtt:           9 * time.Millisecond,
			lastrtt_as_string: "9ms",
		},
		"slow": {
			state:             true,
			has_ever_received: true,
			lastrtt:           100 * time.Millisecond,
			lastrtt_as_string: "100ms",
		},
		"nortt": {
			state:             true,
			has_ever_received: true,
		},
	}
	server := &StatusServer{
		repo:        repo,
		view:        ServerView{},
		globalStats: NewGlobalStatistics(),
		statsProvider: func(wrapper PingWrapperInterface) PWStats {
			return stats[wrapper.Host()]
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/dashboard", nil)
	rec := httptest.NewRecorder()
	server.dashboardApiHandler(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	var dashboard DashboardStats
	if err := json.Unmarshal(rec.Body.Bytes(), &dashboard); err != nil {
		t.Fatalf("decode dashboard: %v", err)
	}
	if dashboard.AvgRTT != "54.5ms" {
		t.Fatalf("expected avg RTT over hosts with RTT only, got %q", dashboard.AvgRTT)
	}
	if len(dashboard.TopRTT) < 2 {
		t.Fatalf("expected top RTT entries, got %#v", dashboard.TopRTT)
	}
	if dashboard.TopRTT[0].Host != "slow" || dashboard.TopRTT[1].Host != "fast" {
		t.Fatalf("expected duration sort slow before fast, got %#v", dashboard.TopRTT)
	}
}

func TestLoadHostsFromFileIgnoresComments(t *testing.T) {
	path := filepath.Join(t.TempDir(), "hosts.txt")
	content := "localhost\n# comment\nexample.com # inline comment\n\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write hostfile: %v", err)
	}

	hosts, err := loadHostsFromFile(path)
	if err != nil {
		t.Fatalf("load hostfile: %v", err)
	}

	want := []string{"localhost", "example.com"}
	if !sameStringSlice(hosts, want) {
		t.Fatalf("expected %#v, got %#v", want, hosts)
	}
}

func TestParseUserSettingsKeepsHashInsideQuotedHost(t *testing.T) {
	settings, err := parseUserSettings([]byte(`hosts:
  - "host#one"
  - 'host#two'
view:
  filter: 1
  sort: 4
  rate: 1
  cols: [1,2]
  hidden: {}
`))
	if err != nil {
		t.Fatalf("parse settings: %v", err)
	}

	want := []string{"host#one", "host#two"}
	if !sameStringSlice(settings.Hosts, want) {
		t.Fatalf("expected hosts %#v, got %#v", want, settings.Hosts)
	}
}

func TestParseHostsInputReturnsCIDRLimitError(t *testing.T) {
	_, err := parseHostsInput("10.0.0.0/8")
	if !errors.Is(err, ErrCIDRTooLarge) {
		t.Fatalf("expected ErrCIDRTooLarge, got %v", err)
	}

	hosts, err := parseHostsInput("192.0.2.0/30 example.com")
	if err != nil {
		t.Fatalf("unexpected parse error: %v", err)
	}
	want := []string{"192.0.2.1", "192.0.2.2", "example.com"}
	if !sameStringSlice(hosts, want) {
		t.Fatalf("expected %#v, got %#v", want, hosts)
	}
}

func TestStatusServerReadHandlersRejectPost(t *testing.T) {
	server := &StatusServer{
		repo:          NewMemoryHostRepository(),
		view:          ServerView{},
		statsProvider: func(PingWrapperInterface) PWStats { return PWStats{} },
	}

	req := httptest.NewRequest(http.MethodPost, "/json", nil)
	rec := httptest.NewRecorder()
	server.jsonHandler(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
	if allow := rec.Header().Get("Allow"); allow != http.MethodGet {
		t.Fatalf("expected Allow GET, got %q", allow)
	}
}

func TestStatusServerPrunesTraceStates(t *testing.T) {
	server := &StatusServer{traces: make(map[string]*webTraceState)}
	now := time.Now()
	for i := 0; i < maxTraceStates; i++ {
		key := fmt.Sprintf("old-%03d", i)
		server.traces[key] = &webTraceState{
			startedAt:  now.Add(time.Duration(i) * time.Second),
			finishedAt: now.Add(time.Duration(i) * time.Second),
		}
	}

	server.getOrCreateTraceState("new")

	if len(server.traces) != maxTraceStates {
		t.Fatalf("expected %d trace states after pruning, got %d", maxTraceStates, len(server.traces))
	}
	if _, ok := server.traces["new"]; !ok {
		t.Fatal("expected new trace state to be retained")
	}
	if _, ok := server.traces["old-000"]; ok {
		t.Fatal("expected oldest trace state to be pruned")
	}
}
