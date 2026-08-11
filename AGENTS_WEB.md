# AGENTS_WEB.md — Webserver-specific rules

This file extends [`AGENTS.md`](./AGENTS.md) with rules specific to the
HTTP status server. Read `AGENTS.md` first; this document assumes it.

The webserver code lives entirely in `status_server.go`.

---

## 1. Architecture

```mermaid
flowchart TB
    main["main / RunTUI"] -->|StartStatusServer| SS[StatusServer]
    SS -->|mux| H1["/ htmlHandler"]
    SS --> H2["/dashboard dashboardHtmlHandler"]
    SS --> H3["/api/dashboard dashboardApiHandler"]
    SS --> H4["/text textHandler"]
    SS --> H5["/json jsonHandler"]
    SS --> H6["/state stateHandler"]
    SS --> H7["/view viewHandler GET/POST"]
    SS --> H8["/trace traceHandler GET/POST"]
    SS -->|viewMu| View[(ServerView)]
    SS -->|traceMu| Traces[(map[string]*webTraceState)]
    SS -->|traceSem cap=2| TraceSem[(chan struct{} cap=2)]
    SS -->|repo| Repo[HostRepository]
    SS -->|globalStats| Globals[GlobalStatistics]
```

`StatusServer` is a single struct holding the `*http.Server`, the
`viewMu`/`traceMu` mutexes, the trace cache, and a reference to the
`HostRepository` plus a `StatsProvider` function (typically
`TUIModel.getCachedStats` or the equivalent closure used by
`-webserver` mode).

---

## 2. HTTP server configuration

```go
server.srv = &http.Server{
    Addr:              listener.Addr().String(),
    Handler:           mux,
    ReadHeaderTimeout: 2 * time.Second,
    IdleTimeout:       5 * time.Second,
    ReadTimeout:       3 * time.Second,
    WriteTimeout:      10 * time.Second,
    MaxHeaderBytes:    1 << 20, // 1 MB
}
server.srv.SetKeepAlivesEnabled(false)
```

Key invariants:

1. **Keep-alives are disabled** — this is on purpose. With keep-alives
   enabled, idle conns can leak goroutines when the TUI shuts down.
2. **All timeouts are aggressive**. This is a local-only server; clients
   are expected to be local dashboards polling at 1–5 s.
3. **`listener` is closed via `srv.Shutdown(ctx)`**, not `srv.Close`,
   to allow in-flight requests to finish.

### 2.1 Lifecycle

- `StartStatusServer(...)` returns a running server. Caller is
  responsible for `defer server.Stop()`.
- `Stop()` uses a 2 s shutdown context, which is more than enough for
  a local server.
- If the port is `0`, the function returns `(nil, nil)`. Callers must
  handle `nil` (the TUI does; pure webserver mode fails fast).

---

## 3. Concurrency model

| Lock | Protects | Readers | Writers | Held for |
|---|---|---|---|---|
| `viewMu` (RWMutex) | `s.view ServerView` | every `View()`, every `GET /view` | `POST /view` (after validation) | entire `viewPatch` decode + apply |
| `traceMu` (RWMutex) | `s.traces map[string]*webTraceState` | `GET /trace`, `pruneTraceStatesLocked`, `snapshotTrace` | `startTrace`, `finishTrace`, `getOrCreateTraceState` | one map operation |
| `traceSem` (chan) | limits concurrent traceroutes to 2 | n/a | `POST /trace` | entire traceroute lifetime |

### 3.1 Trace-state FIFO pruning

```go
const maxTraceStates = 256
```

When a new key would exceed the cap, the oldest entries (by
`startedAt`) are pruned. `getOrCreateTraceState` calls
`pruneTraceStatesLocked(keepKey)` after insertion. This bounds memory
even if clients poll `POST /trace` repeatedly with new keys.

---

## 4. Request handlers

### 4.1 Method guard

Every handler that accepts multiple methods uses:

```go
if !allowMethods(w, r, http.MethodGet[, http.MethodPost]) {
    return
}
```

`allowMethods` writes `405 Method Not Allowed` and the right `Allow:`
header. The test `TestStatusServerReadHandlersRejectPost` enforces
this for read-only handlers.

### 4.2 Common response headers

For JSON responses:

```go
w.Header().Set("Content-Type", "application/json")
w.Header().Set("Cache-Control", "no-store")
w.Header().Set("Connection", "close")
```

For HTML responses: `Content-Type: text/html; charset=utf-8` plus the
same `Cache-Control` and `Connection`.

### 4.3 Body-size cap (POST)

`POST /view` uses:

```go
body := http.MaxBytesReader(w, r.Body, maxViewPatchBytes)
```

`maxViewPatchBytes = 1 << 20` (1 MiB). This prevents slow-loris
attacks on the body and gives an automatic 413 from `MaxBytesReader`.

---

## 5. View state (`/view`)

### 5.1 `ServerView` shape

```go
type ServerView struct {
    Filter        FilterMode
    Sort          SortMode
    Rate          UpdateRate
    Hidden        map[string]bool
    Cols          []int
    GroupBySubnet bool
    RawInputs     []string
}
```

`RawInputs` is **read-only** (echoed back to clients). The TUI sets
this once on startup and never changes it.

### 5.2 `viewPatch`

```go
type viewPatch struct {
    Filter        *FilterMode
    Sort          *SortMode
    Rate          *UpdateRate
    Hidden        map[string]bool
    Cols          []int
    GroupBySubnet *bool
}
```

Each field is a pointer (or `nil`) so the decoder can distinguish
"field absent" from "field set to zero value".

### 5.3 Validation

| Field | Validator |
|---|---|
| `Filter` | `validFilterMode` (0..3) |
| `Sort` | `validSortMode` (0..4) |
| `Rate` | `validUpdateRate` (0..3) |
| `Hidden` | `sanitizeHiddenHosts` |
| `Cols` | `normalizeColumns` (deduped, sorted, in 1..6) |

Invalid values are silently dropped (the existing value is retained).
`Cols` is always normalized even if it was valid.

### 5.4 Hidden-host sanitization

`sanitizeHiddenHosts(patch map[string]bool) map[string]bool`:

1. Build a `known` set of current wrapper host strings.
2. Iterate the patch:
   - Trim whitespace.
   - Skip empty keys.
   - Skip keys longer than `maxHiddenKeyLength` (512).
   - Skip `value == false`.
   - Skip unknown hosts.
3. Cap at `maxHiddenViewItems` (10 000).

This guarantees that a malicious POST cannot bloat memory or
reference stale host strings.

---

## 6. Traceroute endpoint (`/trace`)

### 6.1 `POST /trace`

1. Validate the key (`strings.TrimSpace` + non-empty).
2. Look up the wrapper by `Host()`. Unknown → `404`.
3. Derive the target from `stats.iprepr`; if empty, derive from the
   host string (`deriveTraceTarget`).
4. Try to acquire `traceSem` non-blocking; if full → `429`.
5. Call `startTrace(key, target)` (returns a new `seq`).
6. Spawn a goroutine that runs `runTraceroute(...)` with a 20 s
   context. The goroutine releases `traceSem` via `defer`.
7. Return the current `traceResponse` snapshot.

### 6.2 `GET /trace?key=…`

Returns the current `traceResponse`. Polled at 1 s intervals by the
HTML page until `running == false` and `finished_at` is non-zero.

### 6.3 Target safety

`runTraceroute` calls `isValidTracerouteTarget(target)`:

- Must not start with `-` (flag injection).
- Must contain only `[a-zA-Z0-9.\-:\[\]]`.

This is enforced before exec, not after — `exec.CommandContext` does
not escape arguments.

---

## 7. HTML rendering

The HTML pages (`/`, `/dashboard`) are produced by `fmt.Fprintf` with
string literals. **Host data is never interpolated into the HTML
template directly** — it is rendered through `appendText(div, 'span',
h.host)` which sets `textContent` rather than `innerHTML`. The test
`TestDashboardScriptDoesNotInjectHostHTML` enforces this.

### 7.1 Why not `html/template`?

The pages have a lot of small, repeated JavaScript fragments and
polling state. Inline string templates keep the code readable and the
binary small. The `textContent`-only contract is enforced by tests.

### 7.2 Adding a new HTML page

1. Add a handler function `(s *StatusServer) xxxxHtmlHandler`.
2. Register in `StartStatusServer` mux.
3. Set the same headers as other HTML handlers.
4. Use `appendText` for any host-supplied data.
5. Poll a JSON endpoint (not the HTML one) to keep responses small.

---

## 8. Dashboard metrics (`/api/dashboard`)

`DashboardStats` is built by `dashboardApiHandler` in a single pass
over the visible wrappers (post-`Hidden` filter). Buckets:

| Label | Range |
|---|---|
| `<5ms` | `< 5 ms` |
| `5-20ms` | `< 20 ms` |
| `20-50ms` | `< 50 ms` |
| `50-100ms` | `< 100 ms` |
| `>100ms` | catch-all (use `< 100 h` as the exclusive upper bound) |

Top lists are capped at 5 entries; sort comparators use the bucket
data, not `RTT`, so they match the public docs.

`avg_rtt` only counts hosts with `lastrtt > 0` (i.e. online hosts
that have reported a real RTT). This is the same rule the TUI's
dashboard uses; test `TestDashboardAggregatesRTTByDuration` locks it.

---

## 9. JSON shapes (the public contract)

The shapes documented in [`docs/API.md`](./API.md) §2 are the public
contract. Adding a field is non-breaking. Renaming or removing a
field is breaking. To rename:

1. Add the new field.
2. Keep emitting the old field with the old name for at least one
   release.
3. Update the docs.
4. Remove the old field after a deprecation period.

---

## 10. Testing checklist for webserver changes

| Concern | Test |
|---|---|
| Method guards | `TestStatusServerReadHandlersRejectPost` |
| Hidden-host sanitization | `TestViewHandlerSanitizesHiddenHosts` |
| Trace-state pruning | `TestStatusServerPrunesTraceStates` |
| Dashboard RTT aggregation | `TestDashboardAggregatesRTTByDuration` |
| No HTML injection | `TestDashboardScriptDoesNotInjectHostHTML` |

For new endpoints, add tests that:

- Send the right methods, expect 200.
- Send wrong methods, expect 405 with the right `Allow`.
- Send malformed JSON, expect 400.
- Send a body larger than `MaxBytesReader`, expect the decoder to
  fail with a wrapped `*http.MaxBytesError`.
- Use a `httptest.NewRecorder` and assert on the response code and
  JSON shape.

---

## 11. Performance notes

- The HTML pages are small (`< 50 KiB`) and re-served every poll;
  caching at the reverse-proxy level is fine because each page
  embeds a fresh `updated` timestamp.
- The dashboard JSON is also small. If you have a `/24`, expect
  `< 50 KiB` for `/state` and `< 5 KiB` for `/api/dashboard`.
- The text handler uses `strings.Builder` — preallocate if you
  expect > 1024 hosts.
- The view patch handler does at most one `repo.GetAll()` per
  request. Avoid iterating the repo more than once in any handler.

---

## 12. Common pitfalls

| Pitfall | Fix |
|---|---|
| Returning `s.view` directly from `View()` | Return a defensive copy via `snapshotView()` — `s.viewMu.RLock()` covers the map read but not external mutation of the returned reference. |
| Forgetting `Cache-Control: no-store` | Polling clients will cache and show stale data. |
| Calling `repo.GetAll()` under `viewMu` | Risk of lock-ordering issues. Acquire `viewMu` after reading the repo, never inside the same lock chain. |
| `traceMu` held while calling `runTraceroute` | Never hold `traceMu` for the duration of the syscall; copy out the bits you need and release the lock. |
| HTML host interpolation in JS | Always use `appendText(div, 'span', value)` — never `el.innerHTML = value`. |
| Returning a 500 from a panic in the handler | `defer recover()` in any goroutine that touches the server; otherwise the panic kills the program. |