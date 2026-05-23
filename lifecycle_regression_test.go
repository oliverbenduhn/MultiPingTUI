package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
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
	model.lastTickTime = time.Now().Add(-time.Second)
	_, _ = model.Update(tickMsg(time.Now()))

	if wrapper.count != 1 {
		t.Fatalf("expected one CalcStats call per tick, got %d", wrapper.count)
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
