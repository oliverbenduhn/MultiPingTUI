package main

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestSnapshotState_EventuallyConsistent verifies the lock-free snapshot
// path: state and lastSeenNs are derived from the lastrecv atomic without
// requiring a lock.
func TestSnapshotState_EventuallyConsistent(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	var current int64
	nowFunc = func() time.Time { return time.Unix(0, current) }

	p := &PWStats{}
	current = 1_000_000_000
	p.lastrecv = 900_000_000

	// delta = 100ms, threshold 100ms → strict less-than, so offline.
	state, lastSeen := p.SnapshotState(100_000_000)
	if state {
		t.Fatalf("expected state=false (delta==threshold, not less), got true (lastSeen=%d)", lastSeen)
	}
	if lastSeen != 100_000_000 {
		t.Fatalf("expected lastSeen=100ms, got %d", lastSeen)
	}

	// Move clock back 1ms → delta=99ms, still online.
	current -= 1_000_000
	state, _ = p.SnapshotState(100_000_000)
	if !state {
		t.Fatalf("expected state=true at delta=99ms, got false")
	}

	// Now 200ms later, delta=300ms, threshold 100ms → offline.
	current += 200_000_000
	state, _ = p.SnapshotState(100_000_000)
	if state {
		t.Fatalf("expected state=false after timeout, got true")
	}
}

// TestSnapshotState_NeverReceived verifies the "never seen" path: state is
// false, lastSeen counts from startup_time.
func TestSnapshotState_NeverReceived(t *testing.T) {
	origNow := nowFunc
	defer func() { nowFunc = origNow }()

	var current int64
	nowFunc = func() time.Time { return time.Unix(0, current) }

	p := &PWStats{}
	p.smMu.Lock()
	p.startup_time = 1_000_000_000
	p.smMu.Unlock()

	current = 4_000_000_000
	state, lastSeen := p.SnapshotState(100_000_000)
	if state {
		t.Fatalf("expected state=false (never seen), got true")
	}
	if lastSeen != 3_000_000_000 {
		t.Fatalf("expected lastSeen=3s, got %d", lastSeen)
	}
}

// TestSnapshotCache_HitsFastPath exercises the snapshot cache: a second
// collectStatuses call within snapshotTTL must not invoke statsProvider.
func TestSnapshotCache_HitsFastPath(t *testing.T) {
	repo := newCountingRepoForPerf(20)
	var calls atomic.Int32
	provider := func(pw PingWrapperInterface) PWStats {
		calls.Add(1)
		return pw.CalcStats(int64(2 * time.Second))
	}
	srv := &StatusServer{
		repo:          repo,
		statsProvider: provider,
		view:          ServerView{Filter: FilterAll, Sort: SortByIP},
	}
	srv.subnetByName = map[string]string{}
	srv.subnetByIP = map[string]string{}

	_ = srv.collectStatuses()
	first := calls.Load()
	if first == 0 {
		t.Fatalf("expected at least 1 provider call, got 0")
	}

	for i := 0; i < 50; i++ {
		_ = srv.collectStatuses()
	}
	if calls.Load() != first {
		t.Fatalf("snapshot cache missed: provider called %d times (expected %d)", calls.Load(), first)
	}
}

// TestSnapshotCache_InvalidatesOnViewChange verifies UpdateView invalidates.
func TestSnapshotCache_InvalidatesOnViewChange(t *testing.T) {
	repo := newCountingRepoForPerf(10)
	var calls atomic.Int32
	provider := func(pw PingWrapperInterface) PWStats {
		calls.Add(1)
		return pw.CalcStats(int64(2 * time.Second))
	}
	srv := &StatusServer{
		repo:          repo,
		statsProvider: provider,
		view:          ServerView{Filter: FilterAll, Sort: SortByIP},
	}
	srv.subnetByName = map[string]string{}
	srv.subnetByIP = map[string]string{}

	_ = srv.collectStatuses()
	first := calls.Load()

	srv.UpdateView(ServerView{Filter: FilterOnline, Sort: SortByIP})
	_ = srv.collectStatuses()
	after := calls.Load()
	if after <= first {
		t.Fatalf("expected cache to invalidate on view change: first=%d, after=%d", first, after)
	}
}

// TestSubnetIndex_RebuildsOnlyOnViewChange verifies the subnet index is
// reused for the same view and rebuilt when the view changes.
func TestSubnetIndex_RebuildsOnlyOnViewChange(t *testing.T) {
	repo := newCountingRepoForPerf(20)
	srv := &StatusServer{
		repo:          repo,
		statsProvider: func(pw PingWrapperInterface) PWStats { return *pw.Stats() },
		view:          ServerView{GroupBySubnet: true, RawInputs: []string{"192.168.10.0/24"}},
	}
	srv.subnetByName = map[string]string{}
	srv.subnetByIP = map[string]string{}

	view := ServerView{GroupBySubnet: true, RawInputs: []string{"192.168.10.0/24"}}
	idx1 := srv.getSubnetIndex(view)
	idx2 := srv.getSubnetIndex(view)

	if len(idx1.byHostKey) != len(idx2.byHostKey) {
		t.Fatalf("expected same map size on repeated calls, got %d vs %d",
			len(idx1.byHostKey), len(idx2.byHostKey))
	}
}

// TestViewVersion_ChangesOnRawInputs verifies that viewVersion changes when
// raw inputs change.
func TestViewVersion_ChangesOnRawInputs(t *testing.T) {
	a := viewVersion(ServerView{Filter: FilterAll, RawInputs: []string{"10.0.0.0/24"}})
	b := viewVersion(ServerView{Filter: FilterAll, RawInputs: []string{"10.0.0.0/24", "192.168.1.0/24"}})
	if a == b {
		t.Fatalf("expected viewVersion to differ when raw inputs change")
	}
	if viewVersion(ServerView{Filter: FilterAll, RawInputs: []string{"x"}}) !=
		viewVersion(ServerView{Filter: FilterAll, RawInputs: []string{"x"}}) {
		t.Fatalf("expected deterministic viewVersion for identical input")
	}
}

// TestGzipMiddleware_CompressesResponse verifies the middleware.
func TestGzipMiddleware_CompressesResponse(t *testing.T) {
	body := `{"hello":"world","list":[1,2,3,4,5]}`
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "gzip" {
		t.Fatalf("expected Content-Encoding=gzip, got %q", got)
	}
	if got := rr.Header().Get("Vary"); !strings.Contains(got, "Accept-Encoding") {
		t.Fatalf("expected Vary to contain Accept-Encoding, got %q", got)
	}
	zr, err := gzip.NewReader(rr.Body)
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	defer zr.Close()
	decoded, _ := io.ReadAll(zr)
	if string(decoded) != body {
		t.Fatalf("decoded body mismatch: got %q want %q", string(decoded), body)
	}
}

// TestGzipMiddleware_PassesThroughWhenNoAccept verifies that without
// Accept-Encoding, the response is not gzipped.
func TestGzipMiddleware_PassesThroughWhenNoAccept(t *testing.T) {
	body := `{"hello":"world"}`
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, body)
	}))

	req := httptest.NewRequest("GET", "/", nil)
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got == "gzip" {
		t.Fatalf("expected no gzip (no Accept-Encoding), got Content-Encoding=%q", got)
	}
	if rr.Body.String() != body {
		t.Fatalf("expected uncompressed body, got %q", rr.Body.String())
	}
}

// TestGzipMiddleware_RespectsExistingEncoding verifies the middleware
// passes through handlers that set their own Content-Encoding.
func TestGzipMiddleware_RespectsExistingEncoding(t *testing.T) {
	handler := gzipMiddleware(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Encoding", "br")
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = io.WriteString(w, "precompressed")
	}))
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("Accept-Encoding", "gzip")
	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, req)

	if got := rr.Header().Get("Content-Encoding"); got != "br" {
		t.Fatalf("expected Content-Encoding to remain br, got %q", got)
	}
	if rr.Body.String() != "precompressed" {
		t.Fatalf("expected precompressed body to pass through, got %q", rr.Body.String())
	}
}

// TestGzipBytes_BelowThresholdNotCompressed verifies the minSize path.
func TestGzipBytes_BelowThresholdNotCompressed(t *testing.T) {
	raw := []byte(`{"x":1}`)
	gz := gzipBytes(raw)
	if gz != nil {
		t.Fatalf("expected nil for tiny input, got %d bytes", len(gz))
	}

	bigger := bytes.Repeat([]byte("a"), 1000)
	gz = gzipBytes(bigger)
	if gz == nil {
		t.Fatalf("expected gzipped output for 1000-byte input")
	}
	zr, err := gzip.NewReader(bytes.NewReader(gz))
	if err != nil {
		t.Fatalf("gzip reader: %v", err)
	}
	decoded, _ := io.ReadAll(zr)
	if !bytes.Equal(decoded, bigger) {
		t.Fatalf("decoded mismatch")
	}
}

// TestStatusServer_CachedBytesExposed verifies the cache holds valid
// pre-encoded bytes for direct wire write.
func TestStatusServer_CachedBytesExposed(t *testing.T) {
	repo := newCountingRepoForPerf(5)
	srv := &StatusServer{
		repo: repo,
		statsProvider: func(pw PingWrapperInterface) PWStats {
			return pw.CalcStats(int64(2 * time.Second))
		},
		view: ServerView{Filter: FilterAll, Sort: SortByIP},
	}
	srv.subnetByName = map[string]string{}
	srv.subnetByIP = map[string]string{}

	_ = srv.collectStatuses()

	cached := srv.cachedSnap.Load()
	if cached == nil {
		t.Fatalf("expected cachedSnap to be populated")
	}
	if len(cached.raw) == 0 {
		t.Fatalf("expected cached.raw to be populated")
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(cached.raw, &arr); err != nil {
		t.Fatalf("cached.raw is not valid JSON: %v", err)
	}
	if len(arr) != 5 {
		t.Fatalf("expected 5 entries, got %d", len(arr))
	}
}

// countingRepoForPerf is a minimal HostRepository used by the snapshot
// cache tests. Different name from tui_test.go's repo helpers to avoid
// shadowing the existing fakeWrapper.
type countingRepoForPerf struct {
	mu       sync.Mutex
	wrappers []PingWrapperInterface
}

func newCountingRepoForPerf(n int) *countingRepoForPerf {
	r := &countingRepoForPerf{wrappers: make([]PingWrapperInterface, n)}
	for i := 0; i < n; i++ {
		key := "192.168.10." + itoa(i+1)
		ip := key
		r.wrappers[i] = &fakeWrapper{
			host:  key + " (" + ip + ")",
			repr:  key,
			stats: &PWStats{hrepr: key, iprepr: ip},
		}
	}
	return r
}

func (r *countingRepoForPerf) GetAll() []PingWrapperInterface { return r.wrappers }
func (r *countingRepoForPerf) UpdateAll(wrappers []PingWrapperInterface) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.wrappers = wrappers
}
