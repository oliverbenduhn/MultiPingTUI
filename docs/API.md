# HTTP Status Server API

`mping` exposes a read-mostly HTTP status mirror on `127.0.0.1` when run with `-webserver <port>` (default if flag present without value: see main.go). The TUI is unaffected — the server is a sidecar for dashboards and scripts.

All endpoints return JSON unless noted. `Content-Type: application/json`, `Cache-Control: no-store`, `Connection: close`.

Base URL in examples: `http://127.0.0.1:8080`.

## Endpoints

| Method | Path | Purpose |
|---|---|---|
| GET | `/` | HTML status page |
| GET | `/dashboard` | HTML dashboard with charts |
| GET | `/api/dashboard` | JSON aggregated dashboard data |
| GET | `/text` | Plain-text host table (column-aligned) |
| GET | `/json` | Host status snapshot, all hosts |
| GET | `/state` | Same as `/json` (alias, kept for compatibility) |
| GET | `/view` | Current view settings (filter, sort, columns, hidden list) |
| POST | `/view` | Patch view settings (JSON merge) |
| GET | `/trace?key=<host>` | Latest traceroute result for host |
| POST | `/trace` | Start new traceroute for host |

## `GET /json` and `GET /state`

Per-host status. Identical payload.

```json
{
  "view": { "filter": 1, "sort": 0, "rate": 1, "hidden": {}, "cols": [1,2,3,4,5,6], "group_by_subnet": false, "raw_inputs": [] },
  "statuses": [
    {
      "key": "localhost",
      "host": "localhost",
      "ip": "127.0.0.1",
      "online": true,
      "rtt": "1.2ms",
      "last_reply": "200ms ago",
      "online_time": "5m",
      "last_loss_ago": "10m ago",
      "last_loss_duration": "2s",
      "subnet_group": ""
    }
  ],
  "updated": "2026-01-15T12:00:00Z"
}
```

Fields are omitted when empty (`error`, `last_loss_ago`, `last_loss_duration`, `subnet_group`).

## `GET /api/dashboard`

Aggregated metrics for the HTML dashboard.

## `GET /text`

Plain-text column-aligned table. No JSON wrapping. Suitable for `curl | less` or piping to `awk`.

## `GET /view`

```json
{
  "filter": 1,
  "sort": 0,
  "rate": 1,
  "hidden": {"old-host": true},
  "cols": [1,2,3,4,5,6],
  "group_by_subnet": false,
  "raw_inputs": ["localhost", "192.168.1.0/24"]
}
```

`filter` / `sort` / `rate` are integer enums (see below).

## `POST /view`

Patch view settings. Body is a partial object; omitted fields stay unchanged. Body size capped at 1 MB.

```json
{
  "filter": 2,
  "sort": 2,
  "hidden": {"old-host": true, "another": false}
}
```

Limits: `hidden` capped at 10,000 entries, each key max 512 bytes (sanitized server-side).

### Enum values

| Field | Value | Meaning |
|---|---|---|
| `filter` | 0 | All |
| `filter` | 1 | Smart (online or ever-replied) |
| `filter` | 2 | Online only |
| `filter` | 3 | Offline only |
| `sort` | 0 | Name |
| `sort` | 1 | Status |
| `sort` | 2 | RTT |
| `sort` | 3 | Last seen |
| `sort` | 4 | IP (numeric, IPv4/IPv6-aware) |
| `rate` | 0 | 100ms |
| `rate` | 1 | 1s |
| `rate` | 2 | 5s |
| `rate` | 3 | 30s |

Out-of-range or unknown enum values are silently ignored — the existing value is kept.

## `GET /trace?key=<host>`

Latest traceroute result for a host. Returns the in-flight or last completed trace; polls until `done: true` if a trace is running.

```json
{
  "key": "google.com",
  "target": "142.250.74.206",
  "seq": 42,
  "hops": [
    {"ttl": 1, "ip": "192.168.1.1", "rtt_ms": 1.2, "host": "router.local"},
    {"ttl": 2, "ip": "10.0.0.1", "rtt_ms": 8.4, "host": ""}
  ],
  "done": true,
  "started_at": "2026-01-15T12:00:00Z",
  "finished_at": "2026-01-15T12:00:03Z"
}
```

## `POST /trace`

Start a new traceroute. Body:

```json
{"key": "google.com"}
```

Returns `429 Too Many Requests` if 4 concurrent traceroutes are already running (configurable via `traceSem` in `status_server.go`).

## Error responses

| Status | When |
|---|---|
| 400 | Missing required field, invalid JSON, oversized body |
| 404 | Unknown host key on `/trace` |
| 405 | Wrong HTTP method for endpoint |
| 413 | Body over 1 MB |
| 429 | Traceroute concurrency limit |
| 500 | JSON encoding failure (server-side bug) |

## Authentication

None. The server binds to `127.0.0.1` only and is intended for local use. Do not expose without a reverse proxy that enforces auth.

## Examples

```bash
# Live host table
curl -s http://127.0.0.1:8080/text

# Switch filter to Offline via API
curl -X POST -d '{"filter":3}' http://127.0.0.1:8080/view

# Trigger traceroute
curl -X POST -d '{"key":"google.com"}' http://127.0.0.1:8080/trace

# Poll until done
while ! curl -s 'http://127.0.0.1:8080/trace?key=google.com' | jq -e '.done'; do sleep 1; done
```