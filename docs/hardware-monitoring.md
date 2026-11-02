# Hardware monitoring (systems, containers, SMART)

Up monitors HTTP endpoints. This document describes the second half of the
product: the **hardware** of the machines themselves - CPU, memory, disks,
network, Docker/Podman containers and S.M.A.R.T. health - shown in the
**Systems** page of the dashboard.

The feature is not a collector written here. It speaks the protocol of the
[Beszel](https://github.com/henrygd/beszel) agent (MIT), so the operator runs the
regular, well tested agent on the machine and Up becomes its hub. Nothing of the
collector (cgo, docker client, smartctl bindings) is linked into the server.

- Package: `internal/beszel` (the protocol, ~700 lines, no other dependency than
  `fxamacker/cbor/v2`).
- Models: `internal/models/system*.go`.
- Services: `internal/services/system*.go`.
- UI: `/systems` (`web/src/views/SystemsView.vue`) and `/systems/:id`.

## 1. How the connection works

```
   machine                                   Up (node A)                 Up (node B)
┌──────────────┐   1. WS /api/beszel/agent-connect   ┌──────────┐        ┌──────────┐
│ beszel-agent │ ──────────────────────────────────► │  handler │        │          │
│              │   2. CheckFingerprint (CBOR)        │  lease   │        │          │
│              │ ◄────────────────────────────────── │  poller  │        │          │
│              │   3. GetData every 60 s             │  ingest  │        │          │
│              │ ◄────────────────────────────────── │          │        │          │
└──────────────┘                                     └──────────┘        └──────────┘
                                                           │                  ▲
                                                           └─── peer API ─────┘
                                                             (history of a system
                                                              this node owns)
```

The agent is the **client**: it dials the hub, so no inbound port is opened on the
monitored machine and a NAT or a dynamic address is not a problem. The exchange is
CBOR over a WebSocket:

1. the agent presents `X-Token` (the system credential) and `X-Beszel` (its
   version);
2. the hub proves it knows the `KEY` the agent was started with, by signing the
   token (ed25519) - the agent refuses every other request until it verifies that
   signature - and asks for the machine fingerprint in the same message;
3. the hub polls `GetData` once a minute, and `GetSmartData` on its own, slower
   cadence.

The wire structs live in `internal/beszel/protocol.go`, `entities.go` and
`containers.go`, with the integer CBOR keys copied from the agent (they are the
contract: renumbering one would make an older agent decode the wrong shape).
Agents **older than 0.18** are refused at the handshake with a readable reason,
because that is the release whose response envelope this build speaks.

## 2. Setting a machine up

1. **Systems → Add system**. The dialog shows two things:
   - the **agent key** (`KEY`), the same for the whole installation;
   - after saving, the **token** - shown once - and a ready to paste
     `docker run` line.

```sh
docker run -d --name up-agent --restart unless-stopped \
  --network host \
  -v /var/run/docker.sock:/var/run/docker.sock:ro \
  -v /var/lib/beszel-agent:/var/lib/beszel-agent \
  -e KEY="ssh-ed25519 AAAA..." \
  -e TOKEN="<the token>" \
  -e HUB_URL="https://up.example.com" \
  henrygd/beszel-agent:0.21.1
```

2. The system appears as **pending**, then as soon as the first sample arrives
   (immediately on connect, then once a minute) as **reporting**.

The token is stored as a SHA-256 hash; the plaintext is shown once and cannot be
recovered (rotating it is a button away). The `KEY` is **derived** from the
cluster private key (`internal/beszel/keys.go`): every node of a cluster therefore
signs with the same key, which is what lets a peer take an agent over after a
failure. Regenerating the cluster key rotates it too, and then every agent has to
be restarted with the new value.

### Security model, stated plainly

- The agent needs `/var/run/docker.sock` to report containers. That is
  **root-equivalent access on the machine it runs on** (the socket is the Docker
  API). Mount it read-only, and do not run the agent on a machine whose Docker
  daemon you would not hand to a monitoring tool.
- The token authenticates the machine; the `KEY` authenticates Up to the machine.
  A leaked token alone is not enough: the fingerprint of the machine is bound on
  the first handshake, so a copy of the token started elsewhere is refused.
- The token travels in a header, so the agent should reach Up over TLS (or a
  private network). The agent's `HUB_HEADERS` covers a proxy that adds
  authentication of its own.

## 3. Data model

| Table | Written by | Meaning |
|---|---|---|
| `systems` | any node (operator edit) | name, host notes, enabled, token hash, fingerprint, identity of the machine, last sample |
| `system_leases` | the node holding the agent connection | who is talking to the agent, until when |
| `system_metrics` | the owner node | one row per minute: CPU, memory, swap, root filesystem, network, disk I/O, load, temperature, uptime, container count |
| `system_containers` | the owner node | the latest state of each container (a mirror of the last sample) |
| `system_container_metrics` | the owner node | one row per running container per minute |
| `system_smart` | the owner node | the last SMART report, as the agent sent it (JSON) |

The charts are downsampled **when they are read** (`system_stats.go`): a window is
split into ~240 buckets and the database returns one averaged row per bucket, so a
30 day chart is the same size as a one hour chart and the browser never renders
43 000 points.

History is pruned by the housekeeping loop (every 6 h) with
`system_retention_days` (default 30 days, `0` = never purge, Admin > Settings).
The container snapshots and the SMART report are never pruned: they are the latest
observation, not history.

## 4. Ownership, failover and clusters

Exactly one node may talk to an agent at a time: two pollers would double every row
and the charts would interleave two histories.

- The node that **accepted** the connection takes the lease (`system_leases`) and
  renews it every 30 s; the lease is valid for 90 s, so one slow database does not
  hand a healthy system away.
- A lease whose **node stopped reporting heartbeats** (the same
  `models.NodeOfflineSeconds` rule the rest of the cluster uses) is stale even
  before it expires, and the next node that sees the agent takes it.
- A clean **shutdown releases the lease** (`application.stop` ->
  `SystemService.CloseAgents`), so a rolling restart hands the agents over at once.
- If a node keeps its socket but loses the lease (it was unreachable for longer
  than the validity), its renew fails and the connection is closed with the reason
  "another node took over this system": writing samples nobody owns is exactly what
  the lease exists to prevent.

The agent retries every 10 s while it has no connection, so it lands on another
node (through the load balancer or DNS) and takes the lease there. That is the whole
failover story for the default dial-out mode.

**Shared mode** (one database for every node) needs nothing else: the lease row is
shared, and the history the owner writes is what every node reads.

**Federated mode** (one database per node) splits the data the same way heartbeats
are split (decision **D5** of `docs/clustering-modes.md`):

- the **configuration** of a system is a synchronised entity (`system`): name, host
  notes, enabled flag, token hash, fingerprint. Every node can therefore show the
  machine and accept its agent if the connection moves;
- the **samples stay local to the owner**, because they are one machine's
  observation of one instant - there is nothing to reconcile;
- a node that does not own the system **reads its history from the owner** through
  the signed peer endpoint `GET /api/cluster/sync/systems/history?uuid=&hours=`, and
  the detail page says where the charts came from. A peer that cannot be reached
  never breaks the page: it renders the local knowledge (name, host, owner) with
  empty charts instead of an error.

A node that just restarted does not steal an agent from a node that has been up for
hours: `ClusterService.CanOwnAgent` applies the same settle time the notification
election uses.

## 5. HTTP API

The dashboard routes are session authenticated (`registerAdmin`); the agent route
is not (the agent has no session).

| Method | Path | Purpose |
|---|---|---|
| GET | `/api/systems` | every machine, decorated with its status and owner node |
| POST | `/api/systems` | create; answers with the one time token and the docker line |
| GET | `/api/systems/overview` | the last sample of every machine (the cards) |
| GET | `/api/systems/agent-key` | the `KEY` value, its fingerprint and the minimum agent version |
| GET/PUT/DELETE | `/api/systems/:id` | read, edit, delete (the delete removes the history) |
| POST | `/api/systems/:id/token` | rotate the agent token (answers with the new one) |
| GET | `/api/systems/:id/history?hours=` | the detail page: row + charts + containers + SMART |
| GET | `/api/systems/:id/metrics?hours=` | only the charts |
| GET | `/api/systems/:id/containers` | the containers of the machine |
| GET | `/api/systems/:id/containers/:name/metrics?hours=` | one container's history |
| GET | `/api/systems/:id/smart` | the stored SMART report |
| POST | `/api/systems/:id/smart/refresh` | ask the agent now (owner node only) |
| GET | `/api/systems/containers` | every container of every machine |
| GET | `/api/systems/smart` | every SMART report |
| GET | `/api/beszel/agent-connect` | the WebSocket the agent dials (token in `X-Token`) |
| GET | `/api/cluster/sync/systems/history` | peer API: the history of a system this node owns |

## 6. Limitations worth knowing

- **Poll interval is fixed at 60 s.** The agent's own collector resolves one
  minute, so a shorter interval would re-read a cache. It is
  `models.SystemSampleInterval`, not a setting.
- **SMART needs `smartctl` and the device.** The container image ships it, but the
  device has to be visible inside the container (`--device` or a privileged
  sandbox). Without it the report is simply empty, and the page says so.
- **Container history is per minute per running container.** A machine with fifty
  containers writes 72 000 rows a day; the retention is what keeps that bounded
  (30 days by default, and `0` means never purge).
- **No hub-dial (SSH) mode.** Up never dials an agent: only the agent dials Up.
  That is what keeps every node behind NAT friendly, and it is the mode Beszel
  recommends.
- **The identity of a machine is its fingerprint.** Reinstalling the agent with a
  fresh data directory produces a new fingerprint and the handshake is refused
  until the operator rotates the token (which clears the binding).
- **A regenerated cluster key rotates the agent KEY.** Every agent must then be
  restarted with the new value; Admin > Cluster warns about it.
- The `systems` UI strings are shipped in English for the locales that have no
  translation yet (the keys exist everywhere, so the parity check stays green).

## 7. Tests

Unit tests run with the normal suite:

```sh
go test ./internal/beszel/ ./internal/services/ ./internal/handlers/
```

What they pin: the handshake signature verifies with the public half of the
host key (and a foreign key is refused), the CBOR sample decodes into the same
numbers the charts show, the version gate, the token hashing, the status derived
from the freshness of the last sample, the downsampling step of every window, and
the route tree (a static segment next to a parameter panics at registration, and
a build alone does not catch it).

Two opt-in integration checks need a MariaDB/MySQL:

```sh
docker run --rm -d --name up-verify -p 127.0.0.1:3307:3306 \
  -e MARIADB_ROOT_PASSWORD=verify -e MARIADB_DATABASE=up \
  -e MARIADB_USER=up -e MARIADB_PASSWORD=verify mariadb:11
# boot the server once against it, so the schema exists
export UP_SCRATCH_DSN='up:verify@tcp(127.0.0.1:3307)/up?charset=utf8mb4&parseTime=true&loc=UTC'
go test ./internal/services/ -run TestSystemLeaseLive -v
go test ./internal/handlers/ -run TestAgentEndToEnd -v
docker rm -f up-verify
```

`TestSystemLeaseLive` checks the ownership rules against a real engine (claim,
refusal while a live node holds it, takeover of an expired lease, takeover from a
dead node, release). `TestAgentEndToEnd` drives the whole chain: a simulated agent
opens the WebSocket, authenticates with the token and the host key, answers a
sample, and the assertions read the stored history, the containers and the SMART
row back through the API service.

## 8. Protocol attribution

The CBOR actions, the request envelope, the sample structs and the fingerprint
handshake are those of [Beszel](https://github.com/henrygd/beszel) by Henry
Wolfe, MIT licensed. The structs here are a hand written mirror of the fields Up
reads (the agent is not a library, and importing it would drag the whole collector
in), with the integer keys kept identical on purpose.
