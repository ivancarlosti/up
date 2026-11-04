# Plan - Probe URL guard (CodeQL `go/request-forgery` on the HTTP and WebSocket probes)

> Task: close the two **critical** `go/request-forgery` alerts reported by CodeQL
> against `internal/checkers/http.go:119` and `internal/checkers/websocket.go:58`
> without changing what a legitimate monitor can target.

## 1. Context

CodeQL flagged both probes on 2026-11-04:

| Alert | Rule | Severity | Location | Message |
|---|---|---|---|---|
| 7 | `go/request-forgery` | critical | `internal/checkers/http.go:119` | the URL of this request depends on a user-provided value |
| 8 | `go/request-forgery` | critical | `internal/checkers/websocket.go:58` | the WebSocket URL of this request depends on a user-provided value |

The taint is real: `models.MonitorConfig.URL` arrives from the monitors API
(dashboard session or write-scoped API token) and goes straight into
`http.NewRequestWithContext` / `websocket.Dialer.DialContext`. Only a scheme
prefix was checked (`strings.HasPrefix(c.URL, "http://")` in
`models.MonitorConfig.Validate`), which does not constrain the host, the
credentials, the port or the control characters of the value.

The repository already solved the same query once for the cluster join flow:
`services.clusterPrimaryURL` (alert 3, now **fixed**) validates the URL shape
before the request that carries the cluster key is built.

## 2. Goal

- The two alerts close as **fixed** by the next CodeQL analysis, not by a
  dismissal.
- A refused URL is a **down** heartbeat with an explicit message, never a panic
  and never a dial.
- No behaviour change for a real monitor: LAN targets, IP literals, IPv6, ports,
  paths, query strings and `%` escapes keep working.

## 3. Steps

1. `internal/checkers/urlguard.go` - one compiled `probeURLPattern`
   (`^(https?|wss?)://<host>[:port][/path][?query]`, host = DNS name, IPv4 or a
   bracketed IPv6 literal) plus `invalidProbeURLMessage`, which names the schemes
   of the probe that refused the URL.
2. `internal/checkers/http.go` - guard the trimmed URL **before**
   `http.NewRequestWithContext`, and build the request from that same trimmed
   value, so the guarded string is the dialed string (covers HTTP and Keyword,
   which share `httpRequest`).
3. `internal/checkers/websocket.go` - the guard runs before `url.Parse` and the
   handshake; the existing `ws://`/`wss://` check stays, so an `https://` target
   keeps its specific message.
4. `internal/checkers/urlguard_test.go` - a grammar table (accepted and refused
   shapes, mirroring `TestClusterPrimaryURLPattern`) and an end-to-end table
   proving HTTP, Keyword and WebSocket all answer **down** with `invalid url`.
5. `docs/monitors.md` §2 - document the accepted shape and that credentials
   belong in the auth fields; `docs/security.md` §11 - new section on outbound
   probe URLs (SSRF), what is refused and why the guard is a shape check.

## 4. Risks

- **A stored URL that used to work is now refused.** `Normalize` already trims
  the value on save and the UI enforces a scheme, but a hand-edited row with
  embedded credentials or a protocol-relative target becomes **down** loudly.
  Mitigation: the message names the accepted shape, and `auth_type` covers the
  credential case.
- **CodeQL may not accept a regexp as a barrier** (it did for
  `clusterPrimaryURL`). Mitigation: the guard is inline in both call sites, on
  the exact value passed to the sink; if analysis still reports it, the fallback
  is a `// codeql[go/request-forgery]` suppression that references §11, and that
  is a decision to take explicitly, not silently.
- **The guard is not an allow list.** A monitor can still point at
  `169.254.169.254` or a LAN address - by design, otherwise the product cannot
  monitor anything internal. The trust boundary is monitor-write access, which is
  documented in §11.
- **Redirects are not re-validated** by the guard. They are bounded by
  `max_redirects` and produced by `net/http` from the target's own `Location`;
  tightening them would break legitimate redirect chains (http -> https,
  www -> apex) and is out of scope for this alert.

## 5. Verification

```bash
gofmt -l internal/checkers/
go vet ./internal/checkers/
go build ./...
go test ./...            # full suite, EXIT=0
go test ./internal/checkers/ -run 'TestProbeURLPattern|TestProbesRejectUndialableURLs' -v
```

After the push: wait for the CodeQL run, then confirm alerts 7 and 8 are `fixed`
and that no new alert appeared (`gh api repos/ivancarlosti/up/code-scanning/alerts`).
