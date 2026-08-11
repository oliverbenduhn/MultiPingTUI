# Web Status Server API

This document describes the local HTTP API served by `mping` on
`127.0.0.1:<web-port>` (default `8080`). The server is started by the TUI
and mirrors its live view; it can also run standalone with `-webserver`.

All responses set:

```http
Cache-Control: no-store
Connection: close
Content-Type: application/json
```

Keep-alives are disabled at the `http.Server` level. Bodies are small
(< 1 MiB) and tailored for polling. There is **no authentication**: the
listener is bound to `127.0.0.1` and is intended for local dashboards
only. Do not expose it on a public interface without a reverse proxy
that adds auth and TLS.

---

## 1. Endpoint catalogue

| Path | Method | Purpose |
|---|---|---|
| [`/`](#get-) | GET | HTML live view |
| [`/dashboard`](#get-dashboard) | GET | HTML dashboard |
| [`/api/dashboard`](#get-api-dashboard) | GET | JSON dashboard metrics |
| [`/text`](#get-text) | GET | Plain-text summary |
| [`/json`](#get-json) | GET | JSON array of host statuses |
| [`/state`](#get-state) | GET | View + statuses + timestamp |
| [`/view`](#get-view) | GET | Current `ServerView` |
| [`/view`](#post-view) | POST | Patch `ServerView` |
| [`/trace`](#get-trace) | GET | Poll trace state for a key |
| [`/trace`](#post-trace) | POST | Start a traceroute for a key |

---

## 2. Common shapes

### `HostStatus`

The shape of one host as it appears in `/json`, `/state`, and
`/api/dashboard`:

```jsonc
{
  "key": "localhost (127.0.0.1)",
  "host": "localhost",
  "ip": "127.0.0.1",
  "online": true,
  "rtt": "320µs",
  "last_reply": "12ms ago",
  "online_time": "1h23m",
  "last_loss_ago": "2m ago",        // omitted if no recorded loss
  "last_loss_duration": "30s",      // omitted if no recorded loss
  "error": "",                      // non-empty if the wrapper failed
  "subnet_group": "192.168.1.0/24"  // omitted if no CIDR matches
}
```

### `ServerView`

```jsonc
{
  "filter": 1,
  "sort": 4,
  "rate": 1,
  "hidden": { "192.168.1.55": true },
  "cols": [1, 2, 3, 4, 5, 6],
  "group_by_subnet": true,
  "raw_inputs": ["localhost", "192.168.1.0/24"]
}
```

| Field | Type | Allowed values |
|---|---|---|
| `filter` | int | `0` All / `1` Smart / `2` Online / `3` Offline |
| `sort` | int | `0` Name / `1` Status / `2` RTT / `3` Last Seen / `4` IP |
| `rate` | int | `0` 100ms / `1` 1s / `2` 5s / `3` 30s |
| `hidden` | object<string,bool> | Only entries with `value: true` are honoured. Unknown host keys are silently dropped. Maximum 10 000 entries, 512 chars per key. |
| `cols` | int[] | Subset of `[1..6]`, no duplicates, normalized to ascending order. |
| `group_by_subnet` | bool | |
| `raw_inputs` | string[] | Echo of the host strings (read-only). |

### `TransitionEvent`

```jsonc
{
  "Host": "github.com",
  "IP": "140.82.121.4",
  "State": true,
  "When": "2024-05-08T12:34:56.789Z",
  "Duration": 30000000000  // nanoseconds; 0 if unknown
}
```

Used in `RecentTransitions` from `/api/dashboard`.

### `DashboardStats`

```jsonc
{
  "total": 254,
  "online": 42,
  "offline": 198,
  "never_seen": 14,
  "uptime": "1h23m",
  "avg_rtt": "12.5ms",
  "health_percent": 16.53,
  "recent_transitions": [ /* TransitionEvent, up to 20 */ ],
  "top_offline":  [ /* HostStatus, up to 5, alphabetical */ ],
  "top_rtt":      [ /* HostStatus, up to 5, by RTT descending */ ],
  "rtt_dist": [
    { "label": "<5ms",     "count": 12 },
    { "label": "5-20ms",   "count": 18 },
    { "label": "20-50ms",  "count": 7  },
    { "label": "50-100ms", "count": 3  },
    { "label": ">100ms",   "count": 2  }
  ]
}
```

---

## 3. Endpoints

### GET `/`

Returns the interactive HTML view. The page uses vanilla JS to poll
`/state` every second. It applies user interactions (column reorder,
hidden host toggle, filter change) by sending `POST /view` and reading
back the echoed `ServerView`. Resizable columns are implemented with
mouse drag; column widths are stored locally in `localStorage`, not
persisted to the config file.

Content-Type: `text/html; charset=utf-8`

### GET `/dashboard`

Standalone dashboard page (totals, RTT distribution, top offline, top
RTT, recent transitions). Polls `/api/dashboard` every 2 seconds.

Content-Type: `text/html; charset=utf-8`

### GET `/api/dashboard`

Returns the [`DashboardStats`](#dashboardstats) object. `top_offline`
and `top_rtt` are capped at 5 entries each; the rest are dropped.

```http
GET /api/dashboard HTTP/1.1
Host: 127.0.0.1:8080
```

```json
{
  "total": 254,
  "online": 42,
  "offline": 212,
  "never_seen": 14,
  "uptime": "1h23m45s",
  "avg_rtt": "12.5ms",
  "health_percent": 16.53543307086614,
  "recent_transitions": [ /* ... */ ],
  "top_offline": [ /* ... */ ],
  "top_rtt": [ /* ... */ ],
  "rtt_dist": [ /* ... */ ]
}
```

### GET `/text`

Plain-text table. One host per line. Columns: `host`, `state`,
`rtt`, `last_reply`, `last_loss`. Useful for shell scripts that don't
want to parse JSON.

```text
localhost (127.0.0.1)  online   320µs   12ms ago   -
github.com (140.82.121.4)  online   18ms    1s ago     -
nas.lan (192.168.1.10)  offline  -       never      -
```

### GET `/json`

JSON array of `HostStatus`. Same shape as the `statuses` array in
`/state`. Order respects the current `ServerView.sort` and the filter
is applied.

```json
[
  { "key": "localhost (127.0.0.1)", "host": "localhost", "ip": "127.0.0.1",
    "online": true, "rtt": "320µs", "last_reply": "12ms ago", "online_time": "1h23m" }
]
```

### GET `/state`

```json
{
  "view": { /* ServerView */ },
  "statuses": [ /* HostStatus[] */ ],
  "updated": "2024-05-08T12:34:56.789Z"
}
```

### GET `/view`

Returns the current `ServerView`.

### POST `/view`

Patch the current `ServerView`. Only fields present in the JSON are
applied; everything else is left alone.

Body schema (`viewPatch`):

```jsonc
{
  "filter": 1,            // optional, validated 0..3
  "sort":   4,            // optional, validated 0..4
  "rate":   1,            // optional, validated 0..3
  "hidden": {              // optional, sanitized
    "192.168.1.55": true,
    "unknown.host": true   // dropped (host not in current set)
  },
  "cols": [1, 2, 4, 5],   // optional, normalized to ascending, deduped, 1..6
  "group_by_subnet": true // optional
}
```

Limits:

| Limit | Value |
|---|---|
| Max body size | 1 MiB (`maxViewPatchBytes`) |
| Max entries in `hidden` | 10 000 (`maxHiddenViewItems`) |
| Max chars per `hidden` key | 512 (`maxHiddenKeyLength`) |

Response: the resulting `ServerView` after the patch.

Side effect: the TUI picks up the change on its next tick (≤ 1 s) via
`syncViewFromStatusServer()`. The change is **not** persisted to disk
unless you also quit the TUI normally.

Errors:

| Status | Body | Cause |
|---|---|---|
| 400 | `invalid JSON body` | Body is not parseable as JSON. |
| 405 | `method not allowed` (`Allow: GET, POST`) | Wrong method. |
| 413 | (silent) | Body > 1 MiB (`MaxBytesReader`). |

### GET `/trace`

Query the trace state for a given host key.

```http
GET /trace?key=localhost%20%28127.0.0.1%29 HTTP/1.1
```

Response (`traceResponse`):

```jsonc
{
  "key": "localhost (127.0.0.1)",
  "running": false,
  "target": "127.0.0.1",
  "started_at": "2024-05-08T12:34:56.789Z",
  "finished_at": "2024-05-08T12:35:14.123Z",
  "took_ms": 7334,
  "error": "",
  "output": "traceroute to 127.0.0.1 (127.0.0.1), 15 hops max\n..."
}
```

If no trace has been run for the key yet, `running` is `false` and the
`started_at`/`finished_at` fields are zero.

Errors:

| Status | Body | Cause |
|---|---|---|
| 400 | `missing key` | Query param absent or empty. |

### POST `/trace`

Start a traceroute for the given key.

```jsonc
// Request
{ "key": "localhost (127.0.0.1)" }
```

Validation:

- Key is trimmed; empty after trim → 400.
- Key must match a current wrapper's `Host()` string → 404 `unknown key`.
- The server must be able to derive a target (the wrapper's resolved
  IP, or stripped `host:port`) → 400 `unable to derive target`.
- At most 2 concurrent traces (`traceSem`); a third request gets
  `429 Too Many Requests`.

Response: the current `traceResponse` for the key (running: true,
output empty). The client should poll `GET /trace` until
`running == false` and `finished_at` is set.

The trace runs in a goroutine with a 20 s timeout. The body of the
trace is the captured stdout+stderr of `traceroute` (Linux/BSD/macOS),
`tracepath`, or `tracert` (Windows).

### Method-not-allowed responses

For read-only endpoints (`/json`, `/text`, `/state`, `/view`,
`/api/dashboard`, `/dashboard`, `/`):

```http
HTTP/1.1 405 Method Not Allowed
Allow: GET
Content-Type: text/plain; charset=utf-8

method not allowed
```

For write endpoints (`/view`, `/trace`):

```http
HTTP/1.1 405 Method Not Allowed
Allow: GET, POST
```

---

## 4. Transition log file (`-log`)

The `-log` flag writes one NDJSON line per state transition. **It is not
an HTTP endpoint**, but it shares the `TransitionEvent` schema.

```json
{"Timestamp":"2024-05-08T12:34:56.789Z","UnixNano":1715169296789000000,"Host":"github.com","Ip":"140.82.121.4","Transition":"down to up","State":true}
```

| Field | Type | Notes |
|---|---|---|
| `Timestamp` | string | RFC3339Nano UTC |
| `UnixNano` | int64 | Same instant in nanoseconds |
| `Host` | string | Host string as supplied (incl. scheme) |
| `Ip` | string | Resolved IP |
| `Transition` | string | `up to down` or `down to up` |
| `State` | bool | `true` if alive, `false` if timed out |

The writer is buffered and flushed every 500 ms (`TransitionWriter`).
The first observation never writes a transition line (see
`PWStats.skip_next_up_highlight` in [`AGENTS.md`](../AGENTS.md) §2.3).

---

## 5. Polling recommendations

The HTML pages self-poll `/state` / `/api/dashboard`. If you build your
own client:

| Use case | Endpoint | Poll interval |
|---|---|---|
| Live table | `/state` | 1–2 s |
| Dashboard cards | `/api/dashboard` | 2–5 s |
| Traceroute follow | `/trace` | 1 s while `running == true`, stop otherwise |

---

## 6. Security notes

- The server binds to `127.0.0.1` only — it is not reachable from
  another host by default.
- There is no authentication; do not expose the port publicly without
  putting it behind an authenticating reverse proxy.
- `MaxBytesReader` caps request bodies. Trace targets are validated by
  `isValidTracerouteTarget` (whitelist of hostname characters; no
  leading `-`) to prevent flag injection into the `traceroute` binary.
- All HTML rendering goes through `appendText(div, 'span', value)` —
  no template interpolation of host data. See the regression test
  `TestDashboardScriptDoesNotInjectHostHTML`.