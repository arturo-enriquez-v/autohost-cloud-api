# Agent gRPC API

How the node agent talks to the API. The REST API for users is in [openapi.yaml](openapi.yaml); this
document covers the gRPC service the agent uses. The message and field reference is
[`proto/node_agent/v1/node_agent.proto`](../proto/node_agent/v1/node_agent.proto): every message and field
is commented there.

- Service: `node_agent.v1.NodeAgentService`
- Server: the API, on `GRPC_PORT` (default `9090`; with `SERVICE_MODE=grpc` it falls back to `PORT`)
- Client: `autohost-agent` (`internal/transport/grpc_client.go`)
- Generated Go code: `internal/grpc/nodepb` here, and a copy in the agent's `internal/transport/nodepb`.
  Regenerate with `make proto` and copy both `.pb.go` files to the agent.

## Authentication

Every RPC is authenticated; the node ID always comes from the credentials, never from a message.

| Credential | How it is sent | Notes |
|---|---|---|
| Node token | `authorization: Bearer autohost-node_...` metadata | Issued at enrollment. Must exist and not be revoked. |
| Client certificate | mTLS, certificate CN = node ID | Issued at enrollment, renewed by the agent through `POST /v1/enrollments/renew-cert`. |

- With both, the certificate's node must match the token's node (`PERMISSION_DENIED` otherwise).
- With only a certificate, the node must still have an active token: certificates last a year and cannot be
  revoked at the TLS layer, so deleting the node or revoking its token is what cuts access.
- Server settings: `GRPC_TLS_ENABLED` (default on) and `GRPC_MTLS_MODE`: `require` (default: a client
  certificate is mandatory), `optional` (verified if sent) or `none`.

Authentication failures return `UNAUTHENTICATED` (missing, unknown or revoked token; missing certificate in
`require` mode; node no longer enrolled).

## RPCs

| RPC | Type | Use |
|---|---|---|
| `RegisterCommands` | client stream → one response | Announces the agent's commands. |
| `Connect` | bidirectional stream | The agent's session: everything else. |
| `ReportMetrics` | unary | Legacy. Only authenticates and logs; current agents use `Connect`. |

## Connection lifecycle

```
agent                                          API
  │── RegisterCommands (stream of commands) ──▶│  upsert node commands
  │◀─────────────── RegisterCommandsResponse ──│
  │── Connect ────────────────────────────────▶│  node online, resume its state (see below)
  │── MetricPayload (now, then every 5 s) ────▶│
  │── HeartbeatPayload (every 30 s) ──────────▶│
  │── HypervisorInfoPayload (once, KVM only) ─▶│
  │◀──────────── jobs and requests at any time │
  │── results, streams ───────────────────────▶│
  ╳  stream ends (error, revocation, restart)   node offline, alert, terminals closed
  │  …10 s later the agent starts again with RegisterCommands
```

**When the stream opens**, the API:
1. marks the node online (in memory and, with Redis, in `nodes:<id>:online` for other API processes);
2. re-sends `StreamContainersPayload` and `StreamLogsPayload` if users are still subscribed to those
   streams from before the disconnection;
3. re-sends a `ConfigureHealthCheckPayload` for every enabled service monitor (the agent drops its checks
   when the stream ends);
4. re-sends pending `COMMAND_TYPE_INSTALL_APP` jobs. Other jobs created while the node was offline stay
   pending and are not re-sent.

**While the stream is open**, the API re-checks the node's enrollment every minute and closes the stream
with `UNAUTHENTICATED` if the node was deleted or its tokens revoked. Both sides send keepalive pings every
20 s; the server accepts pings at most every 10 s.

**When the stream ends**, the API marks the node offline, closes its terminal sessions and sends the
offline alert if the node has one configured. The agent reconnects after 10 s.

Only one stream per node is tracked: a new `Connect` from the same node replaces the previous one.

### Sending to a node

The API queues messages for a node in a buffer of 64. If the node is not connected, or the buffer is full,
sending fails at once (`NOT_FOUND` / `UNAVAILABLE` inside the API) and the caller decides what to do: jobs
stay pending, and REST endpoints that need a live answer report the node as unavailable (for example `503`
in `POST /v1/log-diagnostics/collect`, or a `server-error` event in the SSE streams).

## Message flows

`NodeMessage` (agent → API) and `ServerMessage` (API → agent) each carry exactly one payload in a `oneof`.

### Periodic reports (agent → API)

| Payload | When | What the API does |
|---|---|---|
| `HeartbeatPayload` | Every 30 s | Updates `last_seen_at` and the online flag. |
| `MetricPayload` | Every 5 s | Sends it to live viewers (`GET /v1/node-metrics/{nodeId}/live`); stores at most one every 15 s and checks alert thresholds on those. |
| `HypervisorInfoPayload` | Once per session, only on KVM hosts | Stores the node's VM capacity. |

### Request → response

The API sends a request with a correlation ID and the agent answers with the same ID.

| API sends | Agent answers | Correlation | Triggered by |
|---|---|---|---|
| `ExecuteJobPayload` | `JobResultPayload` (`COMPLETED` or `FAILED`) | `job_id` | `POST /v1/jobs`, backups, marketplace, Caddy, VMs, clusters, scheduled tasks... |
| `CollectLogsPayload` | `LogDiagnosticPayload` | `request_id` | `POST /v1/log-diagnostics/collect` |
| `RequestNetworkStatsPayload` | `NetworkStatsPayload` | `request_id` | `GET /v1/node-network/{nodeId}` (waits 3 s) |
| `FileOperationRequestPayload` | `FileOperationResponsePayload` | `request_id` | `/v1/nodes/{nodeId}/files/*`, public shares |

Jobs: the agent remembers completed job IDs and answers `COMPLETED` without running a job twice. The job's
`params` is a JSON object whose shape depends on `command_name`. File transfers (`FILE_OP_PUSH_TO_URL`,
`FILE_OP_PULL_FROM_URL`) only coordinate over the stream: the bytes go over HTTP to `transfer_url` with the
one-time `auth_token`.

### Subscriptions

The API starts a stream when the first user subscribes and stops it when the last one leaves.

| Start | Agent sends | Stop | REST endpoint |
|---|---|---|---|
| `StreamLogsPayload` | `LogEntryPayload`, one per line | `StopLogsPayload` | `GET /v1/node-logs/{nodeId}/stream` |
| `StreamContainersPayload` | `ContainerListPayload`, a full snapshot on each change | `StopContainersPayload` | `GET /v1/node-containers/{nodeId}/stream` |

A new `StreamLogsPayload` replaces the running log stream (one per node).

### Health checks

| API sends | When |
|---|---|
| `ConfigureHealthCheckPayload` | A service monitor is created or updated (disabling it sends `enabled: false`), and for every enabled monitor on each reconnection |
| `StopHealthCheckPayload` | A monitor is deleted |

When the REST process does not hold the node's stream, it publishes `{"action": "configure" | "stop",
"monitor_id": ...}` on the Redis channel `nodes:<nodeId>:monitors:cmd`; the gRPC process holding the stream
reloads the monitor from the database and sends the payload.

Each `ConfigureHealthCheckPayload` (re)starts the check: the agent runs it right away, then every
`interval_seconds`, and sends a `HealthCheckResultPayload` after each run. It keeps the consecutive failure
count across restarts, applies auto-heal itself and reports the updated `auto_heal_state`, which the API
stores and sends back on the next configuration.

### Web terminal

Bridges the WebSocket of `GET /v1/nodes/{nodeId}/terminal` to a PTY on the node, keyed by `session_id`.

```
browser ─WS─▶ API ── StartTerminalPayload ─────────▶ agent  (spawns the shell)
browser ─WS─▶ API ── TerminalInputPayload / Resize ─▶ agent
browser ◀─WS─ API ◀─ TerminalOutputPayload ───────── agent
browser ─WS─▶ API ── CloseTerminalPayload ──────────▶ agent  (user closed it)
browser ◀─WS─ API ◀─ TerminalClosedPayload ───────── agent  (shell exited or failed)
```

### Unused messages

`RequestProfilePayload` / `ProfileDataPayload` (pprof profiling) are defined but the API never sends the
request and ignores the response. `ReportMetrics` is kept for old agents.

## Changing the protocol

Agents and API servers of different versions are connected at the same time, so:

- never renumber, reuse or change the type of a field; to remove one, mark it `reserved`;
- add fields instead of changing the meaning of existing ones, and make the zero value mean "not set";
- a new `oneof` payload is ignored by an older peer (the API logs `unknown payload`), so the sender must
  cope with never getting an answer;
- after editing the proto: `make proto`, copy `internal/grpc/nodepb/*.pb.go` to the agent's
  `internal/transport/nodepb/`, and update this document if a flow changes.
