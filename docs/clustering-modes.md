# Up - Cluster modes

> Up runs its nodes in one of two modes, chosen with `CLUSTER_MODE`:
>
> - **`shared`** (default) — every node points at the **same** MariaDB/MySQL
>   database. This is the original topology and its reference is
>   [clustering.md](clustering.md).
> - **`federated`** — every node keeps **its own** database and the nodes
>   synchronise the configuration over a signed peer API. This document is the
>   reference for that mode.
>
> **Status: implemented.** All phases (0, 0.5, 1, 2, 3, 4, 5 and 6) are in the
> code and verified against two to four nodes with one database each. The
> synchronisation identity (`uuid`, `origin_node_id`, `revision`), the idempotent
> backfill, the signed peer API with the settle time, the outbox/pull/apply
> protocol with the LWW merge, the manifest/snapshot healing pass for **all seven
> entities** (monitors, groups, their memberships, templates, status pages with
> their selection, channels and their links), the federated voting with the
> derived leader and the notification election, and the opt-in
> channels/settings/session-secret sync with push are all shipped. A federated
> node **boots**: `internal/config/validate.go` refuses it only when
> `CLUSTER_ENABLED` or `CLUSTER_PEER_API` is not `true`.
>
> | Phase | Deliverable | Status |
> |---|---|---|
> | 0 | sync identity, backfill, `CLUSTER_MODE` skeleton | **done** |
> | 0.5 | `run_on=some` + `run_on_nodes` (probe + vote membership) | **done** |
> | 1 | peer registry, signed ping, settle time | **done** |
> | 2 | outbox, pull/apply, LWW, manifest/snapshot, monitors + groups | **done** (all seven entities) |
> | 3 | status pages (+ items/groups) and templates | **done** |
> | 4 | federated voting, derived leader, leader-owned notifications | **done** |
> | 5 | Admin UI, observability, conflict log | **done** (section 21) |
> | 6 | opt-in channels/settings/session-secret sync, push sync | **done** |
>
> Sections 2-19 below were written while the mode was being designed and are kept
> because they explain **why** the protocol looks the way it does; section 22
> records every place where the shipped code **deviates from that design**, and
> section 23 lists what the exit criteria verification actually exercised. Where a
> design paragraph and section 22 disagree, section 22 is the accurate one.

## 1. Goal

Let every node run **its own MariaDB/MySQL** and still behave like one product:

- **Configuration sync** — monitors, monitor groups (+ members), templates and
  status pages (+ their monitor/group selection) are created or edited on *any*
  dashboard and converge on all of them.
- **Cross-node voting** — `ANY_NODE_FAILS`, `ALL_NODES_FAIL` and `QUORUM` keep
  working even though a node only owns its own heartbeats.
- **One notification per transition** — without a shared lock table.
- Still no broker, no shared cache, no service discovery and no third-party
  component: nodes talk to each other over HTTP(S).

### Non-goals for v1

- Replicating heartbeat history (uptime stays per node, see decision **D5**).
- Sharing API tokens, IP rules or per-node rate limits.
- Field-level merge of concurrent edits.

## 2. Why the current cluster cannot simply be pointed at two databases

Everything the cluster does today is built on rows that only exist once:

| Concern | Current mechanism | Federated replacement |
|---|---|---|
| Node registry and liveness | `nodes.last_heartbeat`, written by every node into the shared table | local `nodes` table fed by HTTP pings (the `NodeOfflineSeconds` / `NodeHeartbeatSeconds` constants stay as they are) |
| Primary role | `nodes.is_primary` column | **derived** from the local online view (lowest `node_id` by default), never stored |
| `run_on=primary` | `ClusterService.IsPrimary` reads the column | same method, mode-aware implementation |
| Cross-node votes | `StatsService.LatestPerNode` over the shared `heartbeats` table | new `peer_votes` table filled from a peer endpoint |
| Transition detection | one `monitor_states` row read by every node | local per node (the inputs are the votes and the strategy, so the verdicts agree) |
| Notification election | `INSERT … ON CONFLICT DO NOTHING` into `notification_locks` | **deterministic election** over the online ring; local locks kept for per-node de-duplication |
| Identity of monitors / status pages | auto-increment `id`, meaningful inside one database only | global `uuid` + `origin_node_id` + `revision` (groups and templates already carry a `uuid`) |
| Picking up another node's edit | the node reads the changed row from the shared table | the edit arrives through the sync channel and is applied into the local database |

The scheduler needs **no change**: `scheduler/workers.go` already asks
`cluster.IsPrimary()` and the existing reconcile pass
(`SCHEDULER_RECONCILE_SECONDS`) already compares the running workers with the
local database, so a monitor that arrives through sync starts without a restart.

## 3. Topology

```mermaid
flowchart TB
  subgraph N1["up-node-1 (own MariaDB)"]
    A1["scheduler + checkers"]
    D1[("db-node-1")]
    S1["sync service<br/>outbox + pull loop"]
  end
  subgraph N2["up-node-2 (own MariaDB)"]
    A2["scheduler + checkers"]
    D2[("db-node-2")]
    S2["sync service<br/>outbox + pull loop"]
  end
  S1 -->|"GET /api/cluster/sync/changes\|votes\|manifest<br/>(HMAC, cluster key)"| S2
  S2 -->|"same endpoints, opposite direction"| S1
  U["operator browser"] --> N1
  U --> N2
  M["SMTP / Webhook"] <-- "one elected sender" --- N1
  M <-- "one elected sender" --- N2
```

Every node keeps its own database, its own heartbeat history and its own WebSocket
hub. The only shared state is what the nodes exchange over the peer API, plus the
cluster private key established at join time.

## 4. Identity and merge rule

Every synced row carries three extra columns:

| Column | Meaning |
|---|---|
| `uuid` | global identity, generated by the node that creates the row (`BeforeCreate`, the pattern `MonitorGroup`/`MonitorTemplate` already use); never changes, never reused |
| `origin_node_id` | the node that created the row, kept even after the row is edited elsewhere |
| `revision` | monotonic counter, incremented by the node that performs the edit |

Merge is a **total order** on `(revision, updated_at, origin_node_id)`:
last-writer-wins per record, and every node reaches the same winner without
coordinating. Revision comes first on purpose, so a wrong clock cannot make an
old edit win.

Consequences to accept and document:

- Two nodes editing the *same* record within one sync interval: one edit wins,
  the other is stored in the conflict log (`sync_conflicts`) and reported in the
  Admin UI. The losing value is never silently lost.
- Edits to *different* fields of the same record have the same fate (no field
  level merge) — acceptable for configuration that is written once, and the
  reason decision **D1** exists.
- A delete writes a **tombstone** (`sync_outbox.action = delete` plus a marker in
  `sync_objects`) instead of only removing the row, so a late upsert cannot
  resurrect it. Tombstones expire after `CLUSTER_SYNC_TOMBSTONE_DAYS`; a cursor
  that points into the compacted region triggers the full resync path below.

## 5. Sync protocol

### 5.1 Outbox + cursor (steady state)

1. Every local write to a synced entity appends a row to `sync_outbox`
   (`entity`, `uuid`, `action`, `origin_node_id`, `revision`, `payload`,
   `payload_hash`, `created_at`). This is the only write path the sync cares
   about; nothing has to scan for differences.
2. Each node polls every online peer every `CLUSTER_SYNC_SECONDS`:
   `GET /api/cluster/sync/changes?since=<last seen id>&limit=<batch>`.
3. The batch is applied inside **one transaction**: resolve each `uuid` to a
   local id through `sync_objects`, apply the merge rule, upsert the local row
   and its links (notification links, group members, status page items), then
   advance `sync_peers.last_change_id`.
4. Local changes made by the apply path publish the same events as a local edit
   (`monitor.`, `statusPage.`, `cluster.`), so the WebSocket dashboards and the
   scheduler reconcile pass react exactly as they do today.

Pull (rather than push) is deliberate: a node behind NAT needs no inbound
connection, and a node that was offline for a day heals itself by catching up
from its own cursor. An optional `POST /api/cluster/sync/now` ping keeps latency
low without replacing the loop (`CLUSTER_SYNC_PUSH`, section 13).

### 5.2 Manifest and full resync (healing)

Once every `CLUSTER_SYNC_MANIFEST_SECONDS` (default 10 min), a node pulls
`GET /api/cluster/sync/manifest`, which answers per entity
`{count, max_revision, checksum}` where the checksum is a hash over the sorted
`uuid:revision` pairs. A mismatch, or a cursor older than the tombstone
retention, triggers a paged `GET /api/cluster/sync/snapshot?entity=&page=`
(deterministic uuid order) that reconciles the whole entity without touching the
heartbeats. The pass is the sync counterpart of `scheduler/reconcile.go` and is
what makes the protocol self-healing instead of "eventually maybe".

### 5.3 A batch, concretely

```json
{
  "changes": [
    {
      "id": 4188,
      "entity": "monitor",
      "uuid": "6f1a…",
      "action": "upsert",
      "origin_node_id": "up-node-1",
      "revision": 12,
      "payload": { "name": "API health", "type": "http", "config": { "url": "https://api.example.com/health" } },
      "payload_hash": "9c1b…",
      "updated_at": "2026-01-01T10:00:00Z"
    }
  ],
  "next_since": 4188,
  "has_more": false,
  "latest_revision": 12,
  "protocol_version": 1
}
```

References inside `payload` are uuids, never local ids: `notification_uuids`,
`group_uuids`, `monitor_uuids` (status page items). The receiving node resolves
them through `sync_objects`; a reference to an object it does not have yet is
kept as a pending link and retried on the next apply (the order of changes inside
a batch is not relied upon).

## 6. Voting without a shared database

Today `EvaluateAll` reads `stats.LatestPerNode(ids)` from the shared table. In
federated mode each node publishes its own verdicts and reads the peers':

| Piece | Design |
|---|---|
| Published | `GET /api/cluster/sync/votes` returns, per monitor uuid, the latest heartbeat of *that node only*: `{status, latency_ms, message, checked_at, important}` |
| Stored | `peer_votes(monitor_uuid, node_id, status, latency_ms, message, checked_at, fetched_at)` |
| Fetched | the same loop as the changes pull (or every `CLUSTER_SYNC_SECONDS / 2` for a fresher vote) |
| Freshness | unchanged: `voteWindow(monitor)` = `max(2 × interval, 120 s)` |
| Merged | a federated implementation of `EvaluateAll` merges local heartbeats + `peer_votes` and calls the **unchanged** `AggregateVotes` pure function |

Because `AggregateVotes` is untouched, every existing strategy test
(`cluster_evaluate_test.go`) keeps its meaning, and `VoteProvider` /
`MonitorService.Decorate` need no change at all: they receive the same maps.

Offline nodes are dropped from the vote exactly as today (heartbeat older than
`NodeOfflineSeconds`), and `node_unavailable_strategy` still decides between
`IGNORE` and `MARK_DEGRADED`.

## 7. Leader without a shared row

`nodes.is_primary` cannot exist when there is no shared table, so the role is
derived from the local online view:

- `lowest_id` (default): the online node with the smallest `node_id` is the
  leader, provided it has been continuously online for
  `CLUSTER_LEADER_SETTLE_SECONDS` (default 60 s, twice the ping interval). Every
  node computes the same winner from its own view.
- `explicit`: `CLUSTER_LEADER_NODE_ID` pins the leader (for an ordered
  deployment).
- `hash`: a rendezvous hash (HRW) over the online node ids, used when the
  `NOTIFY_ELECTION=hash` owner is preferred for notifications too. Never
  `% len(candidates)`: modulo reassigns most monitors whenever the membership
  changes.

`IsPrimary()` returns `leaderNodeID == cfg.NodeID`, so `run_on=primary` and the
`PRIMARY_ONLY` sender keep working.

The settle time exists because leadership is not only about who alerts: the
scheduler asks `IsPrimary()` through `handles()` for every monitor on every
reconcile pass, so a flapping view would start and stop the same probe on two
nodes at once — doubling the load on a target that `run_on=primary` was chosen to
protect — and would move the notification duty mid-incident. One minute of
stability is cheaper than either.

During a partition two nodes may each believe they are the leader. With
`NOTIFY_ELECTION=leader` that can still produce a duplicate alert, which
`origin` removes at the cost of a single alerting point (section 8). `QUORUM`
reports `degraded` when a majority of the known nodes is unreachable, which
surfaces the situation in the UI instead of hiding it.

## 8. Notification election without a shared lock

`notification_locks` relied on a unique index in the shared database. Federated
mode computes the sender instead of racing for it.

**The node that decides a transition is the node that sends it.** Every node
still evaluates the aggregate status (the dashboard and the status pages need a
local verdict), but only the *owner* compares it with its previous value and
dispatches. Keeping decision and dispatch on the same node removes the failure
mode where two nodes compute a different `event` for one incident: a `QUORUM`
denominator that differs by one fetched vote already flips `DEGRADED` into
`DOWN`, and a ring keyed on `event` would then elect two different senders and
send two notifications for the same outage.

Owner selection, always from the local view:

1. The candidate set is the **online** nodes, sorted by `node_id`.
2. The owner is the **lowest `node_id`** that has been continuously online for
   `CLUSTER_LEADER_SETTLE_SECONDS` (default 60 s, twice the ping interval). The
   settle time is what keeps a peer blip or a rejoining node from taking the duty
   mid-incident. It is the same rule the `lowest_id` leader derivation uses in
   section 7, so "who probes `run_on=primary`" and "who alerts" move together on
   failover.
3. The owner keeps a **local** `notification_locks` row per
   `(monitor_id, event, bucket)` so it cannot send twice for one window
   (`NotificationLockWindowSeconds` = 60 s for status events, the day for
   certificate events). The table is no longer the cluster-wide guarantee, only a
   per-node one.
4. Neither the owner nor the bucket is derived from the wall clock, so two nodes
   whose clocks disagree cannot elect different senders. The liveness view is the
   only shared input.
5. The winner is recorded in `notification_logs.node_id`, so which node sent what
   stays observable exactly as today.

The failure mode to accept and document: if the owner dies, alerting pauses until
the next low `node_id` has been online past the settle time — about 90 s with the
defaults. That is the price of at-most-once, and the alternatives below trade it
for possible duplicates or for a single point of alerting.

Alternatives, selectable with `CLUSTER_NOTIFY_ELECTION`:

| Value | Behaviour | Trade-off |
|---|---|---|
| `leader` (default) | the sticky lowest-id online node above | at most one alert per transition; alerting pauses for one settle period when that node dies |
| `hash` | owner per monitor: rendezvous (HRW) over `monitor_uuid` | spreads the load; a split view can still double-send, so the clocks must agree (NTP) |
| `origin` | the node that created the monitor sends | no duplicates from a split view; alerting for the monitors it owns stops while it is down |

> `hash` must use **rendezvous** hashing, never `fnv1a(key) % len(candidates)`:
> plain modulo reassigns roughly half of the monitors every time a node joins or
> leaves, so a single peer blip would hand most monitors to a different owner. It
> must also be keyed on `monitor_uuid` alone — putting `event` or a wall-clock
> bucket in the key reintroduces both the two-senders bug and the clock
> dependency (see decision **D3**).

## 9. Liveness (implemented)

Two things are kept apart on purpose:

- the **local** row says "this process is alive": `ClusterService.Ping` refreshes
  it every 30 s, exactly as before;
- the **peer** rows say "they answer me over HTTP": `ClusterService.PingPeers`
  runs on the same 30 s ticker and sends a signed
  `GET /api/cluster/sync/ping` to every node of the registry (`nodes.api_url`),
  concurrently, with a 10 s timeout.

The outcome is stored in `sync_peers` (section 12):

| Field | Written by | Meaning |
|---|---|---|
| `last_success_at` | a successful outbound ping | the peer answered |
| `last_seen_at` | that, **plus** a valid inbound request | liveness works in both directions, so a node reachable through a one way firewall still shows up |
| `online_since` | set on success, **cleared on failure** | how long the peer has been *continuously* reachable — the settle input |
| `last_error`, `last_error_at` | a failed ping | why it failed |
| `status` | both | `online`, `error`, `incompatible` (protocol or mode mismatch), `unknown` |

A failed ping deliberately **does not** touch `nodes.last_heartbeat`: this node's
`Sweep` stays the only writer of `nodes.status`, so the documented thresholds
(`< 60 s` online, `60-120 s` degraded, `>= 120 s` offline) keep a single owner and
a single failed probe never flaps the peer status.

Recovery is what the settle time is for: restarting a peer resets its
`online_since`, so it is not settled for another `CLUSTER_LEADER_SETTLE_SECONDS`,
while a peer that never went down keeps its original `online_since` untouched.

Observed on a two node lab (one shared database, `CLUSTER_PEER_API=true`):

- both nodes reported the other `online` with `peer_version=dev` and
  `protocol_version=1`;
- the peer that had been reachable for more than a minute was `settled=true`
  while the one just seen was `settled=false`;
- stopping a node produced `status=error`, a populated `last_error` and
  `online_since=NULL` within one ping cycle, and its `nodes.status` went
  `degraded` at 89 s without anyone touching the heartbeat;
- restarting it produced a *fresh* `online_since` (`settled=false`) while the
  peer that stayed up kept its `settled=true` and its original timestamp;
- the unreachable/reachable lines are logged **on a transition only**, so a
  healthy cluster stays quiet.

## 10. What syncs, and what stays local

| Entity | v1 | Notes |
|---|---|---|
| `monitors` (+ notification links, group members) | sync | the core of the feature |
| `monitor_groups` (+ members) | sync | already has a uuid |
| `monitor_templates` | sync | already has a uuid |
| `status_pages` (+ `status_page_monitors`, `status_page_groups`) | sync | referenced monitors/groups resolve by uuid |
| `heartbeats` | local | see **D5** |
| `monitor_states`, `notification_logs`, `monitor_certificates` | local | runtime, derived or node-specific |
| notification channels (`notifications`) | opt-in | `CLUSTER_SYNC_NOTIFICATIONS=true`; the payload carries secrets, so it must only be enabled over TLS (decision **D4**) |
| `settings` | opt-in | whitelist only (app name, default locale, default theme); the cluster key is exchanged at join |
| `session_secret` | opt-in | without it a login is valid on one dashboard only (decision **D4**) |
| `api_tokens`, `ip_rules`, per-node rate limits | never | security configuration stays a per-node concern |

### 10.1 Which nodes probe a monitor (`run_on`)

`run_on` survives federated mode **unchanged**, because it is a pure function of
the synchronised monitor row plus the local member view: every node evaluates the
same rule on the same row and reaches the same answer without coordinating. It
selects two things at once — the **probe set** (the scheduler) and the **vote set**
(`participatesInVoting`) — and the notification owner can only send what the vote
set produced:

| `run_on` | Probe set | Who alerts | If a probe node dies |
|---|---|---|---|
| `all` (default) | every node | the owner | resilient: any survivor still has votes, and takes the notification duty after the settle time |
| `primary` | the derived leader | the same leader | probing and alerting fail over **together** |
| `node` | one node | the owner | no data at all from that monitor → `PENDING`, and **no** alert from any node |
| `some` (D11) | the listed nodes | the owner | same as `node`, as soon as the last listed node is gone |

The `node`/`some` row is a data problem, not a permission problem: the owner
cannot alert about a verdict nobody produced. It is also **not a regression** — in
shared mode today a pinned monitor whose node is dead has no fresh heartbeats
either, so it sits in `PENDING` just the same. The UI should show it as "no node
matches this selection" rather than leaving an unexplained gap.

Two consequences worth stating:

- For a monitor the owner does **not** probe itself (`node` / `some`), the owner
  learns the verdict from `peer_votes`, so alerting can lag by up to
  `CLUSTER_SYNC_SECONDS` (15 s by default). For `all` and `primary` the owner
  probes directly and the latency stays ~0, which is a good reason to keep `all`
  the default.
- A `PENDING` monitor reports `PENDING`; it never becomes `DOWN` behind the
  operator's back just because its voters disappeared.

## 11. Peer API

All endpoints are authenticated with the cluster private key through an
HMAC-SHA256 signature. The key itself is **not** sent on these routes: the
signature already proves that the caller knows it, so transmitting it as well
would only add exposure. (The join endpoint keeps its original `X-Cluster-Key`
behaviour, because that is the bootstrap and there is nothing to sign yet.)

| Header | Content |
|---|---|
| `X-Cluster-Node` | sender `NODE_ID` (signed) |
| `X-Cluster-Timestamp` | unix seconds, UTC (signed) |
| `X-Cluster-Nonce` | 16 random bytes, hex, unique per request (signed) |
| `X-Cluster-Signature` | base64url HMAC-SHA256 of the canonical string |

The canonical string is `\n`-joined and covers the method, the request URI (path
**and** query — signing only the path would let a captured request be replayed
with a different `?since=`), the SHA-256 of the body, the timestamp, the nonce
and the node id. The target is the URI as the **receiver** sees it, so a peer
published behind a path prefix (`http://host/up`) signs
`/up/api/cluster/sync/ping`.

Rejected, all with `403`: a missing or wrong signature, a timestamp outside
±5 minutes (past or future), and a nonce already used inside that window.
`utils.SecureCompare` keeps the signature comparison constant time. The rejection
reason is logged server side, never returned.

The nonce cache is **in memory**, not the `sync_nonces` table the first draft of
this document listed: the window is five minutes and every peer endpoint is either
a read or an idempotent pull, so losing the cache on a restart costs nothing,
while a write per request would sit on the hot path of every pull.

| Method | Path | Purpose | Status |
|---|---|---|---|
| GET | `/api/cluster/sync/ping` | identity, version, protocol version, mode, uptime, server time | implemented |
| GET | `/api/cluster/sync/status` | **local** admin view: peers, settle state, last success, last error (session auth) | implemented |
| GET | `/api/cluster/sync/changes?since=&limit=` | outbox batch + `next_since`, `has_more`, `latest_revision` | implemented |
| GET | `/api/cluster/sync/manifest` | per entity `{count, max_revision, checksum}` | implemented |
| GET | `/api/cluster/sync/snapshot?entity=&page=` | paged full state, uuid order | implemented |
| GET | `/api/cluster/sync/votes` | latest verdict per monitor uuid (voting input) | implemented |
| GET | `/api/cluster/sync/settings` | synchronised settings whitelist and session secret (per the opt-in switches) | implemented |
| POST | `/api/cluster/sync/now` | ask a peer to pull immediately | implemented |

The routes are always registered, so a node with the peer API disabled answers a
clear `403` instead of a `404` (or worse, the SPA fallback). They carry **no IP
filter**: a peer is not a dashboard visitor, an operator's dashboard rules must
not be able to block a node, and the signature is the control.

Outbound calls reuse the guards written for the join flow: the `clusterPrimaryURL`
validator (as `peerURL`), `CheckRedirect: http.ErrUseLastResponse` and an
`io.LimitReader` on the response. A ping additionally caps the response at 1 MiB
and uses a 10 s timeout — much shorter than the join's 20 s, because a liveness
probe runs every 30 s and a silent peer must not hold the loop.

## 12. Data model

New tables (added to `database.MigrationModels()`):

| Table | Columns |
|---|---|
| `sync_outbox` | `id`, `entity`, `uuid`, `action` (`upsert`/`delete`), `origin_node_id`, `revision`, `payload` (JSON, null on delete), `payload_hash`, `created_at` |
| `sync_objects` | `uuid` (PK), `entity`, `local_id`, `origin_node_id`, `revision`, `deleted_at`, `updated_at` |
| `sync_peers` | `peer_node_id` (PK), `peer_name`, `peer_api_url`, `peer_version`, `protocol_version`, `status`, `last_seen_at`, `last_success_at`, `online_since`, `last_error`, `last_error_at`, plus the cursor columns `last_change_id` / `last_manifest_at` / `last_manifest_ok`, `updated_at` — all implemented |
| `peer_votes` | `monitor_uuid` + `node_id` (composite PK), `status`, `latency_ms`, `message`, `checked_at`, `fetched_at` |
| `sync_conflicts` | `id`, `entity`, `uuid`, `kept_origin`, `kept_revision`, `lost_origin`, `lost_revision`, `detected_at` |
| `sync_pending_links` | an unresolved reference (a change that arrived before its target), with its attempt count |
| `sync_dead_letters` | a change that could not be applied, with its attempt count (section 22.9) |

`sync_nonces` is no longer in the list: replay protection lives in an in-memory
cache, see section 11.

Column additions (**phase 0, implemented**):

- `monitors`: `uuid` (unique), `origin_node_id`, `revision`
- `status_pages`: `uuid` (unique), `origin_node_id`, `revision`
- `monitor_groups`, `monitor_templates`: `origin_node_id`, `revision` (uuid exists)
- `monitor_states`, `heartbeats`, `notification_locks`: unchanged

The `uuid` column of `monitors` and `status_pages` is **nullable**, while the one
of `monitor_groups`/`monitor_templates` is `NOT NULL`. This is deliberate: those
two tables already had rows when the column was introduced, and MySQL/MariaDB
refuse a unique index over several empty strings. A NULL is not compared by a
unique index, so `AutoMigrate` adds the column and the index in a single pass and
`database.Backfill` fills the NULLs immediately after; the `BeforeCreate` hook of
both models means a row created by the application is never NULL. Verified
against a real MariaDB: the upgrade of a populated schema and the second boot
both succeed (see section 18).

A **backfill** step runs after `AutoMigrate` (new
`internal/database/backfill.go`, called between `Migrate` and `Seed` in
`cmd/server/main.go`): every row still missing a value gets `uuid = UUID()`,
`origin_node_id = NODE_ID` and `revision = 1`. It is **idempotent by
construction** — each of the three statements per table selects only the rows
that are still missing a value, so the second run matches nothing and reports
`sync identity backfill: nothing to do`. That is why it is safe (and expected) to
run on every boot, and why it needs no version guard.

Deletes keep today's hard-delete semantics; the tombstone lives in
`sync_outbox`/`sync_objects`, so no existing behaviour or test changes.

## 13. Configuration

| Variable | Default | Meaning |
|---|---|---|
| `CLUSTER_MODE` | `shared` | `shared` (every node points at the same database) or `federated` (one independent database per node, section 2). The default keeps an existing deployment untouched (section 20) |
| `CLUSTER_PEER_API` | `false` | enables the signed peer API and the peer ping loop. Independent of `CLUSTER_MODE` on purpose — it works in shared mode too, where it cross-checks liveness over HTTP instead of trusting a shared row. Federated mode **requires** it (`internal/config/validate.go`) |
| `CLUSTER_SYNC_SECONDS` | `15` | pull interval per peer |
| `CLUSTER_SYNC_BATCH` | `500` | maximum changes per pull |
| `CLUSTER_SYNC_MANIFEST_SECONDS` | `600` | healing/reconcile pass |
| `CLUSTER_SYNC_TOMBSTONE_DAYS` | `30` | tombstone retention before a full resync is required |
| `CLUSTER_LEADER_ELECTION` | `lowest_id` | how the leader is DERIVED when there is no shared row: `lowest_id` (the settled node with the smallest `node_id`) or `explicit`. `hash` is deliberately not a leader mode — `IsPrimary()` is one boolean for the whole node, while hash ownership is per monitor, so it lives in `CLUSTER_NOTIFY_ELECTION` (§22.12) |
| `CLUSTER_LEADER_NODE_ID` | - | required when `CLUSTER_LEADER_ELECTION=explicit` |
| `CLUSTER_NOTIFY_ELECTION` | `leader` | which node alerts in federated mode: `leader` (the derived leader), `hash` (rendezvous over the monitor uuid) or `origin` (the node that created the monitor) |
| `CLUSTER_LEADER_SETTLE_SECONDS` | `60` | how long a node must be continuously online before it may take the leader role (and the notification duty) instead of the current one |
| `CLUSTER_SYNC_NOTIFICATIONS` | `false` | sync the notification channels (secret-bearing) |
| `CLUSTER_SYNC_SETTINGS` | `false` | sync the non-secret settings whitelist (app name, default locale, default theme). API tokens, IP rules and the per-node rate limits are never synchronised |
| `CLUSTER_SYNC_SESSION_SECRET` | `false` | sync the session secret (decision **D4**), so one login works on every dashboard. It is a separate switch because the secret signs session cookies. **Consequence to expect:** a node that has just generated its own secret adopts the cluster's rather than replacing it, so joining does not log anyone out (seeded defaults are the oldest value there is, §22.16) |
| `CLUSTER_SYNC_PUSH` | `false` | ask the peers to pull as soon as this node publishes something, instead of waiting a full interval. Best effort by design: a push that fails costs nothing, because the periodic pull still delivers. Pull stays the default so a node behind NAT needs no inbound access |
| `CLUSTER_INSECURE_SKIP_VERIFY` | `false` | explicit opt-in for a self-signed peer TLS certificate (logged loudly) |

`CLUSTER_PEER_API=true` requires `CLUSTER_ENABLED=true` and a `NODE_ID` (the
signature is built on it). `CLUSTER_MODE=federated` requires `CLUSTER_ENABLED=true`,
a `NODE_ID` and `CLUSTER_PEER_API=true`; it boots and runs. Both are validated in
`internal/config/validate.go` with the same "report every problem at once" style.

## 14. Code impact map

The mode spans `internal/models/sync.go`,
`internal/services/cluster_sign.go`, `internal/services/cluster_peers.go`,
`internal/services/cluster_peer_status.go`, the `internal/services/sync*.go`
family, `internal/middleware/peer.go` and `internal/handlers/sync.go`, plus the
peer routes, the `PingPeers` call on the maintenance ticker, the sync migrations
and the configuration variables.

| Area | Change |
|---|---|
| `internal/models/sync.go` | `SyncPeer`, `SyncChange`, `SyncObject`, `PeerVote`, `SyncConflict`, `SyncPendingLink`, `SyncDeadLetter` and the pure `Settled` / `PickLeader` / `NotificationCandidates` helpers; the `uuid`/`origin_node_id`/`revision` identity lives in the entity models |
| `internal/services/sync*.go` | `sync.go` (service + loops), `sync_emit.go`, `sync_apply.go`, `sync_apply_entities.go`, `sync_manifest.go`, `sync_votes.go`, `sync_settings.go`, `sync_push.go` |
| `internal/services/cluster.go`, `cluster_evaluate.go`, `cluster_nodes.go` | mode-aware `Nodes`/`Sweep`/`IsPrimary`; federated `EvaluateAll` merging local + `peer_votes`; `AggregateVotes` untouched |
| `internal/services/cluster_notify.go` | `claimEvent` picks the shared lock (shared mode) or the deterministic election (federated mode) |
| `internal/handlers/sync.go` (new), `router.go`, `router_admin.go` | the peer endpoints plus the local `/api/cluster/sync/status` admin view |
| `internal/middleware/` | HMAC + nonce verification for the peer routes |
| `internal/database/` | new models in `MigrationModels()`; `backfill.go` |
| `internal/config/` | the new variables + validation |
| `cmd/server/app.go` | construct the sync service, wire it into the cluster service, start the loops next to `sched.StartMaintenance` |
| `web/src/views/admin/AdminClusterView.vue`, `types-platform.ts`, 3 locales | mode badge, derived leader, per-peer sync table (cursor lag, last manifest, last error), conflict log, "Sync now" |
| `web/scripts/e2e-cluster-federated.mjs` (new) | end-to-end validation with two nodes and two databases (the rig is documented in `development.md` §9; there is no bundled federated compose file) |

The sync core (merge order, cursor arithmetic, notification ownership, checksums,
manifest diff) is written as pure functions so it can be unit-tested without a
database — the same approach that makes `AggregateVotes` testable today. The
repository has no database in its Go tests (no sqlite driver, no `gorm.Open` in
any `_test.go`), so the DB-touching paths are covered by the e2e scripts and by
the operational recipes in `development.md` (decision **D6**, deferred: a
test-only sqlite driver is still an option, it simply did not justify a new
dependency in phase 0).

## 15. Phases

| Phase | Deliverable | Exit criteria |
|---|---|---|
| 0 | uuids, `origin_node_id`, `revision`, backfill, `CLUSTER_MODE` skeleton — **done** | `go test ./...` unchanged, migration idempotent (verified against MariaDB 11), `CLUSTER_MODE=shared` behaviour identical |
| 0.5 | **`run_on=some`** (decision **D11**): `run_on_nodes`, the shared membership rule in the probe and vote paths, UI + templates | **done** (independent of federated mode, and it works in shared mode too) |
| 1 | peer registry, HTTP liveness, signed request middleware, `ping`, **ownership settle time** | a two-node pair reports each other online; unsigned/replayed calls rejected — **done** |
| 2 | outbox, pull/apply, LWW merge, tombstones, manifest/snapshot, **monitors + groups** | a monitor created on node A is scheduled by node B after one interval — **done** (all seven entities) |
| 3 | **status pages** (+ items/groups) and templates | a status page created on A answers publicly on B with the right monitors — **done** |
| 4 | federated voting (`peer_votes`), derived leader, notification election | `ALL_NODES_FAIL`/`QUORUM` behave as documented; one e-mail per transition in a two-node test — **done** |
| 5 | Admin UI, observability, conflict log, docs | mode + peer lag visible in the UI; a conflict surfaces without reading logs — **done** |
| 6 | optional: channels/settings/session-secret sync over TLS, push-based low-latency sync | opt-in features documented and off by default — **done** |

## 16. Failure modes

| Situation | Behaviour |
|---|---|
| A peer is offline | its votes drop out after 2 min; `IGNORE`/`MARK_DEGRADED` decides what the operator sees |
| The same record is edited on two nodes | deterministic LWW; the loser lands in `sync_conflicts` and the UI |
| Network partition | both sides may see themselves as leader; `NOTIFY_ELECTION=leader` can then double-alert, `origin` removes that risk at the cost of a single alerting point, and `QUORUM` can report `DOWN` on an isolated minority because its denominator is the *reachable* nodes (decision **D7**) |
| Cursor points at a compacted tombstone | the manifest pass triggers a full snapshot resync |
| Clock skew | ordering is revision-first, so skew only affects the tiebreak |
| Version drift between nodes | `protocol_version` mismatch refuses to sync with a clear log line |
| Sync storm / huge batch | per-peer backoff, one in-flight pull per peer, `CLUSTER_SYNC_BATCH` cap |
| A node restored from a backup | its cursor is stale but valid: changes since then are replayed and duplicates are absorbed by the merge rule |

## 17. Security

- Every peer URL passes the existing SSRF guard; redirects are never followed.
- Requests are HMAC-signed with the cluster key (timestamp + nonce), so a leaked
  log line cannot be replayed.
- The cluster key is never logged; sync payloads are size-capped.
- Secrets (SMTP password, webhook URL, session secret) only sync behind an
  explicit opt-in and must run over TLS; `CLUSTER_INSECURE_SKIP_VERIFY` exists
  only for internal labs and logs a warning on every boot.
- `docs/security.md` has a "peer trust model" section: every node is trusted
  with the configuration, which is the same trust the shared database implies
  today.

## 18. Tests and documentation

- **Unit (pure):** merge order, cursor arithmetic, tombstone expiry, manifest
  diff, the canonical peer request and the settle rule, conflict detection.
- **httptest:** the peer endpoints against a fake store (the pattern already used
  in `internal/checkers/checker_test.go` and `cluster_join_test.go`), and the
  peer middleware against a stub authenticator: valid, unsigned, tampered, stale
  and replayed, plus the body being readable by the handler after it was hashed.
- **e2e:** `web/scripts/e2e-cluster-federated.mjs` with two nodes and two
  databases: create/edit/delete on each side, status page propagation, voting
  with one node stopped, one notification per transition.
- **e2e (identity):** `npm run e2e:backfill` checks through the API that every
  synchronised row carries a well formed and **distinct** `uuid`, that
  `revision >= 1`, that a freshly created monitor gets its identity from
  `BeforeCreate`, and that `/api/cluster/status` reports `mode: "shared"` on the
  shared-mode instance it runs against.
- **Docs:** this document (renamed from `clustering-federated.md`) is the
  reference for the federated mode; `clustering.md` is the reference for the
  shared mode. `database.md`, `architecture.md`, `security.md`, `api.md`,
  `monitors.md`, `README.md` and `development.md` describe the shipped behaviour.

**How phase 1 proved its exit criteria** (two instances, one database,
`CLUSTER_PEER_API=true`):

- *"a two-node pair reports each other online"* → both
  `/api/cluster/sync/status` listed the other node `online` with its version and
  `protocol_version`; the settle state flipped to `true` only after
  `CLUSTER_LEADER_SETTLE_SECONDS`, restarting a node reset its `online_since` and
  stopping one produced `status=error` + `online_since=NULL` within one cycle.
- *"unsigned/replayed calls rejected"* → 8 signed-request checks against each of
  the two real servers (16/16): unsigned, tampered signature, signature not
  covering the query, stale timestamp, future timestamp and replayed nonce are all
  `403`, while a valid signature is `200`.
- *the peer API disabled* → the route still answers `403`, not `404` and not the
  SPA fallback.

**How phase 0 proved its own exit criteria** without a database in the test
suite:

- *"`go test ./...` unchanged"* → nothing reads the new columns yet, so the suite
  is the regression gate.
- *"migration idempotent"* → structural: every backfill statement selects only the
  rows still missing a value. Confirmed against a real MariaDB 11 by upgrading a
  populated pre-phase-0 schema (the `ALTER TABLE … ADD uuid` +
  `ADD UNIQUE KEY` pair that a naive `NOT NULL` column would have failed on) and
  booting a second time, which logged `sync identity backfill: nothing to do`.
- *"`uuid` is never empty"* → `npm run e2e:backfill`.

## 19. Open decisions

| Id | Decision | Options | Recommendation |
|---|---|---|---|
| **D1** | Conflict model | LWW per record / owner-authoritative / field-level merge | **LWW per record** with a conflict log; configuration is written rarely and an owner-authoritative model needs edit proxying between nodes |
| **D2** | Leader election | `lowest_id` / `explicit` / `hash` | **`lowest_id`**, plus `CLUSTER_LEADER_SETTLE_SECONDS` so a blip cannot move the role; `explicit` stays available for ordered deployments |
| **D3** | Notification election | `leader` / `hash` (HRW) / `origin` | **decided: `leader`** — the sticky lowest-id online node owns both the decision and the send. `hash` (rendezvous over `monitor_uuid`, never modulo, never keyed on `event` or a wall-clock bucket) is the load-sharing option; `origin` is the partition-safe one, at the cost of alerting stopping while that node is down |
| **D4** | Notification channels and session secret | never / opt-in / always | **opt-in for both**; without the session secret a login stays valid on one dashboard only |
| **D5** | Heartbeat history | node-local / replicate a rolling window | **node-local**: the per-node breakdown already exists, and replicating the biggest table defeats the purpose |
| **D6** | DB-level Go tests | keep pure + e2e / add `gorm.io/driver/sqlite` (test-only) | **deferred**: no new dependency. The pure helpers are unit-tested, the migration/backfill and the apply path were verified operatively (two boots against MariaDB 11, plus `e2e:backfill` and `e2e:cluster-federated`) |
| **D7** | Partition alerting rule | keep `QUORUM` = majority of the *reachable* nodes / add a majority-of-all-known variant | the current rule lets an isolated minority report `DOWN` from a single vote; **shipped as-is** (see §22.11 — the admin view now shows `reachable / known`), and `run_on=some` makes it likelier (section 10.1) |
| **D8** | Leadership settle time | none / 2× ping / 90 s | **2× ping (60 s)**: long enough to absorb a blip, short enough to be a real failover |
| **D9** | Notification ownership stickiness | per-monitor / per-bucket | **per-monitor** — implied by D3, and it removes the wall-clock dependency entirely |
| **D10** | "No third-party component" | hard requirement / negotiable | **hard**: D3 keeps federated mode broker-free. A shared Redis/etcd lock would make the election trivial and correct, so revisit this decision before accepting duplicates instead |
| **D11** | Node selection for probing | `all` / `primary` / `node` / + subset / + labels | **subset in v1**: `run_on=some` + `run_on_nodes` (comma separated, like `tags`), with `node_id` and `run_on=node` untouched for compatibility. Labels are the follow-up once the member view carries them |

## 20. Migration and compatibility

1. `CLUSTER_MODE` defaults to `shared`, so an existing deployment is untouched
   until the operator opts in.
2. The uuid/backfill work of phase 0 has shipped **without** enabling federated
   mode: it is prerequisite plumbing with no behaviour change, and it was
   verified on a populated database (section 18).
3. `CLUSTER_MODE=federated` requires `CLUSTER_PEER_API=true`. There is no fallback to a
   local-only node: a node that claims to be federated without serving the peer API would
   take part in nothing while its own database silently diverges. The requirement lives in
   `internal/config/validate.go`.
4. A cluster cannot be half-shared/half-federated: a `shared` node and a
   `federated` node exchange nothing (the protocol version is rejected), which
   keeps the two modes from silently mixing.
5. Moving from shared to federated is a deliberate migration: provision one
   database per node, point every node at its own, restart, and let the phase 2/3
   sync converge the configuration. Heartbeats start fresh per node.
6. **The order of operations.** Start from the shared deployment, then per node:
   provision the new database, create the tables (`migrations` run at boot), set
   `CLUSTER_MODE=federated`, `CLUSTER_PEER_API=true` and `NODE_ID` (unique and
   **never reused**: it is the identity the merge rules key on), then restart it and
   join the cluster with the primary's key (section 9 of docs/development.md). The
   join is what registers the peer, and it is also what carries the key, so
   repeating `CLUSTER_PRIVATE_KEY` on every node is optional — pin it there only if
   you want the key fixed in advance, and then the same value is required
   everywhere. Converge one
   node at a time: a node that has just switched is still empty and will pull a
   full first sync from any peer that already holds the configuration, so it is
   safe to bring up, and a node that fails validates is still a working
   single-node install with its own database.
7. **The empty node bootstraps itself.** There is no separate seed step. A fresh
   node joins, its manifest disagrees with the peer's (0 rows against N), and the
   snapshot pull transfers the configuration in one round. That path is deliberately
   the same one used after a long disconnect, which is why it needs no special case.
8. **What is not migrated.** `nodes`, `heartbeats`, `check_results`,
   `notification_locks` and the incident timeline are per node and start empty: they
   describe what *this* node observed, and copying them would invent history. The
   monitoring state (`monitor_status`) is recomputed from the local heartbeats within
   one interval. The session secret is only synchronised with the explicit
   `CLUSTER_SYNC_SESSION_SECRET` opt-in, and a node that generated its own adopts the
   cluster's instead of replacing it, so a join invalidates nobody's session (§22.16).
9. **Rolling back** means pointing a node back at the shared database with
   `CLUSTER_MODE=shared`. That is a *replacement*, not a merge: the federated node's
   own database is not imported back, so configuration created on it after the switch
   is lost unless it was synchronised to a peer first. The two modes cannot be mixed
   in a running cluster (item 4).






## 21. The admin view (phase 5, implemented)

`GET /api/cluster/sync/status` (session auth) is the local, per-node view, and the
UI shows all of it in **Admin → Cluster**.

- The peer table reports status, settle state, last success, last error, and two
  things a peer table cannot otherwise show: the **outbox cursor**
  (`sync_peers.last_change_id`) and the **manifest result** (`last_manifest_at` /
  `last_manifest_ok`). A cursor that stops advancing is a *stalled* sync; checksums
  that disagree are *divergence* rather than lag. Both are invisible when the only
  thing on screen is "online / offline", because a peer stays online while nothing
  flows.
- The summary reports the waiting references (and how many have been given up on),
  the merge conflicts, the unapplied changes (and how many were skipped), and the
  visibility split `quorum_reachable / quorum_known` with the settled count — the
  D7 denominator, on the screen (§22.11).
- The **recent conflicts** and the **recent unapplied changes** are listed
  themselves, so a conflict is visible without reading a log file. That is phase 5's
  exit criterion.
- Retention: the conflict log and the dead letters are pruned after
  `CLUSTER_SYNC_TOMBSTONE_DAYS` (30 by default) because they are *history*.
  `sync_pending_links` is **not** pruned: an unresolved reference is an open
  problem, not history — it stops being retried past the attempt cap, and the report
  counts it separately so it cannot be forgotten.


## 22. Deviations from this design

Every item below is a place where the shipped code differs from the sections above,
with the reason. Each one was forced by something the two-node lab exposed; the two
nodes ran with two databases throughout.

1. **Link entities (§5).** Memberships and monitor↔channel links are first-class
   entities (`monitor_group_member`, `monitor_notification`) whose uuid is derived
   from the pair (`uuid5("{kind}|{sorted uuids}")`), instead of lists inside the
   container payload. A membership can be edited from *either* end — the monitor form
   and the group editor both rewrite the same join table — and with lists inside the
   payload that gives two writers of two conflicting versions of one relation. As its
   own entity, a relation has one identity, one revision and one writer per edit, and
   it can be removed without touching its container.
2. **Tombstones are kept; only the outbox is pruned (§12).**
   `CLUSTER_SYNC_TOMBSTONE_DAYS` prunes the **outbox**, not `sync_objects`. A
   tombstone in `sync_objects` is what stops a re-delivered old upsert from
   recreating a deleted row; expiring it would make that guarantee time-limited and
   turn a late delivery into a resurrection. A peer whose cursor points before the
   pruned region is healed by the manifest and the snapshot instead — which is what
   makes the prune safe, and why `cursor_expired` exists.
3. **The snapshot is enumerated from `sync_objects`, not from the entity tables
   (§5).** A relation has no row of its own and a tombstone has none either, so a
   snapshot built from the entity tables could describe the live rows but never a
   deletion — and a missed deletion is exactly what the healing pass exists to
   repair. `sync_objects.payload` therefore stores the last applied or published
   payload (empty for a tombstone), and the snapshot is a page of `sync_objects` in
   uuid order.
4. **`origin_node_id` means "the node that produced the current version", not "the
   creator" (§4).** The tie-break needs two concurrent edits to be distinguishable:
   if origin meant the creator, two edits of the same row would share an origin, the
   tie-break would be a tie, each node would keep its own and they would diverge. The
   emitter stamps the local node on every local write — the row, the payload, the
   envelope and `sync_objects` — and the apply path takes the origin from the
   **envelope**, never from the sender's payload copy.
5. **The outbox carries the VERSION timestamp (§4).** `sync_outbox.updated_at` is the
   `updated_at` of the row on the producing node, not the insert time. The insert
   time is always slightly later than the update it describes, so a node comparing its
   own row's `updated_at` against a peer's insert time compares two different
   quantities and concludes that the other is newer. Measured in the lab: each node
   adopted the other's change and logged the opposite conflict verdict.
6. **Protocol version 2.** The status page selection travels as records (uuid, display
   name, group name, sort order, per-item visibility) rather than two sorted uuid
   lists: the order and the per-item overrides live in the join row, so a receiver
   rebuilding the selection from uuids silently reset both.


7. **A name collision renames the ARRIVING row (§4).** `name` is unique for groups
   and templates and `slug` for pages, so an arriving row can collide with one the
   operator created locally. The local row keeps its name — synchronisation must not
   rename the operator's data behind their back — and the arriving row takes a
   deterministic suffix, so the change still lands and no change is ever blocked by a
   name. The suffix depends only on local occupancy, so re-applying is stable.
8. **A reference that arrives before its target is resolved as LOCAL work (§5).** The
   design said the container would be re-delivered; it cannot be, because it arrives
   at the revision the node already holds and the merge rule skips it — as it must, or
   every re-delivery would look like a new version. The receiver therefore re-writes
   the container's own stored payload, without touching its identity, as soon as the
   target exists. Markers are cleared on resolution, and past
   `maxPendingLinkAttempts` (20) they stop triggering retries and stay listed.
9. **A change that cannot be applied is contained (§16).** One unreadable change would
   otherwise roll back its whole batch, leaving the cursor where it is and queueing
   every later change behind it for ever. The apply degrades to one change per
   transaction, counts the failure in `sync_dead_letters`, and past
   `maxDeadLetterAttempts` (10) skips **only** that change. Skipping is recoverable:
   the identity then disagrees and the manifest heals it with a snapshot. An invalid
   outbox payload is also handed to peers as *absent* rather than as broken JSON,
   because a `json.RawMessage` that cannot be marshalled fails the whole response and
   would make a node unservable to every peer.
10. **The manifest checksum covers `(uuid, revision)` per entity (§5).** The counts
    and the maximum revision are reported as well, but the checksum is over the
    identity set: an identity-level divergence — same revision, different origin or
    timestamp — is not caught by it. Named here because it is a real limit of the
    healing pass, not an oversight.
11. **`QUORUM` keeps the reachable set as its denominator (decision D7).** Unchanged
    on purpose, and now visible: the admin view shows `reachable / known`, so a
    partition that shrinks the denominator is a fact on the screen instead of a
    surprise. An isolated minority can still decide with fewer nodes than the cluster
    has — that is the accepted cost of this rule.
12. **`hash` is an election mode, not a leader mode (§7).** `IsPrimary()` answers one
    question for the whole node ("do I run the `run_on=primary` probes?"), while hash
    ownership is per monitor by construction, so a hash "leader" would be a per-monitor
    answer to a global question. `CLUSTER_LEADER_ELECTION` therefore accepts
    `lowest_id` and `explicit` only, and `hash` lives in `CLUSTER_NOTIFY_ELECTION`,
    where it is meaningful. Both remain deterministic from the local view.
13. **The notification text is the owner's own measurement.** In federated mode a node
    holds only its own heartbeats, so the message and latency that travel with an alert
    are the SENDING node's numbers, while the verdict that triggered it is the cluster
    aggregate. A richer message (the worst node's detail) would mean carrying per-node
    detail in the vote payload; it is deliberately left out of v1.
14. **The notification lock is per node, not per cluster (§8).** Without a shared
    table the unique index cannot elect a single sender across the cluster, so
    ownership is derived (12) and the lock row only stops the OWNER from sending twice
    inside one window. A node that is not the owner returns before writing anything —
    the election must be free of side effects for the nodes that lose it, because they
    ask again on every evaluation pass.
15. **The manifest checksum includes the producer (§5, §22.10).** `(uuid, revision,
    origin_node_id)` rather than `(uuid, revision)`: two nodes can hold the same
    revision from different producers — one adopted a concurrent edit, the other kept
    its own — and that state is a real divergence which a two-component checksum
    reports as agreement.
16. **A seeded default carries an epoch timestamp, not the boot time.** Found in the
    lab, not in review: a node joining with a fresh database wrote its own
    `app_name`/`default_locale`/`default_theme` at boot, that write was newer than the
    cluster's real value, and last-write-wins then reverted the setting on EVERY node.
    It is worse for the session secret: a node that generated its own would log the
    others out. Seeding with `1970-01-01` (`database.seededAt`) makes a value nobody
    chose the oldest there is, so it loses to any value an operator actually set and
    the cluster's value flows in instead. Verified by joining a fourth node: it booted
    at the epoch, then showed the cluster's value on all four nodes.
17. **The apply transaction advances the cursor FIRST.** MariaDB raises error 1020
    ("record has changed since last read") when a transaction writes a row that another
    transaction changed after this one's read view was created, and `sync_peers` is
    exactly that row — the ping loop and the manifest pass write it every few seconds.
    Read after the apply has already read `sync_objects`, the cursor write is refused,
    the batch rolls back and is replayed change by change: correct but slow, and it logs
    a warning on every busy batch (0 in a six-change burst after the fix). The cursor
    stays inside the transaction, so a crash mid-batch still re-pulls instead of losing
    rows — a rollback takes the cursor with it. A row lock (`FOR UPDATE`) does NOT help:
    it becomes the victim of the same error.
18. **A peer is registered by joining, not by configuration.** There is no peer-URL
    environment variable: the registry and `sync_peers` are filled by
    `POST /api/cluster/join` with the primary's key (docs/development.md §9). This is
    what makes the cluster key the only per-node secret the operator must repeat.
19. **Two independently generated session secrets do not converge, and that is the
    intended trade.** `CLUSTER_SYNC_SESSION_SECRET` carries the secret last-write-wins
    like any other setting, and a secret a node generated for itself is seeded at the
    epoch (§22.16) so that it can never overwrite one an operator chose. The
    consequence, seen in the lab: a deliberate secret propagates *to* a node that
    generated its own (the epoch loses), which is what makes one login work on every
    dashboard — but if every node generated its own and nobody ever set one
    deliberately, all the rows sit at the epoch, no value is newer than another and
    the option converges nothing at all, silently. The trigger to remember: set (or
    regenerate) the secret once on one node, which stamps `now()` and is then
    carried to the rest. A deterministic tie-break on equal timestamps would remove
    the caveat; the merge rules already break ties with `origin_node_id`, so it is
    the natural shape, but it means putting the sender's node id in the settings
    payload — a protocol change, deliberately not smuggled into this phase.

## 23. Exit criteria: what has been verified

Verified against **two to four federated nodes, one database each, with the boot guard
REMOVED** — `CLUSTER_MODE=federated` now boots and refuses only when
`CLUSTER_PEER_API=true` is missing — every node running the release code:

- A monitor, a group, its memberships, a template, a status page (with its order and
  per-item overrides), a channel (with its configuration) and a channel link created
  on one node all appear on the other, and deletions arrive as tombstones.
- A monitor created on A is probed by B after one interval, and a status page created
  on A answers publicly on **B** with the right monitors and order (the phase 2 and
  phase 3 exit criteria).
- A concurrent edit made on both nodes in one interval converges: both nodes hold the
  same identity and record the same conflict verdict.
- A change B never received is repaired by the manifest and the snapshot; a pruned
  outbox with a stale cursor heals through `cursor_expired` and the cursor realigns.
- A reference that arrives before its target is materialised once the target exists,
  and its marker is cleared.
- A corrupt outbox payload is contained: the sender keeps serving, the receiver
  records a dead letter, and the manifest delivers the row from the snapshot.
- Both nodes report identical live identity sets per entity, and the manifests agree.
- A monitor created on one node reaches the other in about a second with
  `CLUSTER_SYNC_PUSH=true`, well inside the pull interval (phase 6, part 2).
- The settings whitelist converges last-write-wins **keeping the sender's timestamp**,
  so the two nodes settle instead of overwriting each other on every pass; the per
  node rate limits and the secrets never cross while `CLUSTER_SYNC_SETTINGS` is the
  only flag that is on.
- An **empty** node (0 rows in its own database) needs no seed step: joining is enough
  for the manifest to disagree and the snapshot to bring the whole configuration,
  including every monitor (verified with a node added after the cluster was already
  populated).
- A peer's registry liveness is refreshed on every ping cycle (measured at ~7 s,
  against the 120 s grace), so a healthy federated cluster is never swept offline and
  the vote denominator, the QUORUM denominator and the admin view all keep counting
  it.
- A burst of six changes applies with **zero** `1020` errors and no fallback to the
  one-change-per-transaction path (the cursor ordering of §22.17).
- A freshly joined node's seeded defaults never overwrite the cluster's settings: it
  boots at the epoch and adopts the cluster's value, on all four nodes (§22.16).
- The settings a node *serves* follow its own database even when the row was written
  outside the service, so an operator editing the table cannot leave a node answering
  with the value it booted with.
- The peer surface refuses an unsigned caller: `ping`, `changes` and `settings` all
  answer `403` without the cluster key, which is what the e2e assertion harness checks
  on every run.
- The peer API works against a TLS link this node cannot verify, and only when it is
  asked to: with the self-signed peer in place and `CLUSTER_INSECURE_SKIP_VERIFY` off,
  the ping, the pull and the push all fail with `x509: certificate signed by unknown
  authority` and the peer row reads `error`; with the flag on, all three succeed — the
  ping logs `peer reachable`, `POST /api/cluster/sync/now` answers `200` through the
  certificate, a monitor created on one node still reaches the other, and the boot
  says out loud what is now exposed.

The one exit criterion that cannot be run on this development machine is the
`web/scripts/e2e-cluster-federated.mjs` script itself: `scripts/browser.mjs` needs a
global `WebSocket` (Node 21+), and this environment has Node 18. Its assertions were
therefore validated through an equivalent request-level harness, which is what
produced the last two bullets above.
