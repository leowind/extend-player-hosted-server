# extend-player-hosted-server

An example Extend Override app template for **player-hosted servers with persistent sessions**, written in Go. It implements the session DSM (Dedicated Server Management) gRPC contract with `dsSource=custom`: AGS requests a DS asynchronously, the host player registers its own public IP:port through this app's HTTP registration API, and the app reports it to the AGS session.

The app, not AGS, owns the persistent session's lifecycle: persistent sessions never expire on their own (`ttlHours` is ignored), so the app deletes sessions that stay hostless or empty for too long.

This is a template project — clone it, adapt the hosting rules and lifecycle policies, and deploy.

## Build & Test

```bash
make build                           # Build the project
go test ./...                        # Run unit tests
docker compose up --build            # Run locally with Docker
make proto                           # Regenerate proto code
```

Linting: `golangci-lint run` (config in `.golangci.yml`).

## Architecture

AGS invokes this app's gRPC methods instead of its default logic, and host players call the app's HTTP registration API:

```
Game Client → AGS → [gRPC :6565] → This App → dsinformation / delete session → AGS
Host player → [HTTP :8081 register/heartbeat] → This App
```

- `CreateGameSessionAsync` marks the session as awaiting host registration (sync `CreateGameSession` is rejected).
- `POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/register` authorizes the caller (session leader/member), probes `ip:port`, then reports `AVAILABLE`.
- `POST /playerhosted/v1/namespaces/{namespace}/sessions/{sessionId}/heartbeat` keeps the host alive; a lapse reports `DS_ERROR`.
- A sweep (`Registry.StartHeartbeatMonitor`) enforces the lifecycle policies: hostless timeout and empty-session timeout both delete the AGS session.
- The registry is in memory (single replica).

### Key Files

| Path | Purpose |
|---|---|
| `main.go` | Entry point — starts gRPC server, registration HTTP server and lifecycle sweep; wires interceptors and observability |
| `pkg/server/playerhosted/session-dsm.go` | gRPC service implementation (`CreateGameSessionAsync`, `TerminateGameSession`) |
| `pkg/server/playerhosted/registration.go` | Host-facing registration/heartbeat HTTP API, authorization and reachability probe |
| `pkg/client/playerhosted/client.go` | `Registry` — session state, dsinformation callbacks, lifecycle policies (`sweep`, `checkEmpty`, `endSession`) |
| `pkg/server/playerhosted/session-dsm_test.go` | Unit tests and mocks for the session client |
| `pkg/config/config.go` | Environment variable configuration |
| `cmd/demo-setup`, `cmd/demo-ds`, `cmd/demo-client` | End-to-end demo: template setup, host player, joining player |
| `pkg/proto/session-dsm.proto` | gRPC service definition (AccelByte-provided, do not modify) |
| `pkg/pb/` | Generated code from proto (do not hand-edit) |
| `pkg/common/` | Auth interceptor, tracing, logging utilities |
| `pkg/utils/envelope/` | Scope (context + span + logger) used for logging/tracing |
| `docs/player-hosted-overview.md` | Feature overview / announcement page |
| `docker-compose.yaml` | Local development setup |
| `.env.template` | Environment variable template |

## Rules

See `.agents/rules/` for coding conventions, commit standards, and proto file policies.

## Environment

Copy `.env.template` to `.env` and fill in your credentials.

| Variable | Description |
|---|---|
| `AB_BASE_URL` | AccelByte base URL (e.g. `https://test.accelbyte.io`) |
| `AB_CLIENT_ID` | OAuth client ID |
| `AB_CLIENT_SECRET` | OAuth client secret |
| `AB_NAMESPACE` | Target namespace |
| `PLUGIN_GRPC_SERVER_AUTH_ENABLED` | Enable gRPC auth and player token validation (`true` by default) |
| `PLAYERHOSTED_REG_PORT` | Registration HTTP API port (default `8081`) |
| `PLAYERHOSTED_REG_TIMEOUT` | Wait for a host to register before reporting `FAILED_TO_REQUEST` (default `120s`) |
| `PLAYERHOSTED_HEARTBEAT_TIMEOUT` | Report `DS_ERROR` if no heartbeat within this window (default `60s`) |
| `PLAYERHOSTED_HEARTBEAT_INTERVAL` | How often the lifecycle sweep runs (default `15s`) |
| `PLAYERHOSTED_REACHABILITY_TIMEOUT` | TCP dial timeout for the reachability probe (default `5s`) |
| `PLAYERHOSTED_HOSTLESS_SESSION_TIMEOUT` | Delete the session after no live host for this long (default `10m`, `0` disables) |
| `PLAYERHOSTED_EMPTY_SESSION_TIMEOUT` | Delete an AVAILABLE session with no `JOINED`/`CONNECTED` members for this long (default `30m`, `0` disables) |
| `DEMO_*` | Demo binaries only (see `.env.template`) |

The app's client needs `ADMIN:NAMESPACE:{namespace}:SESSION:GAME [UPDATE]` (dsinformation callback), `NAMESPACE:{namespace}:SESSION:GAME [READ]` (GetGameSession for membership checks) and `ADMIN:NAMESPACE:{namespace}:SESSION:GAME [DELETE]` (lifecycle policies), plus the `social` scope.

## Dependencies

- [AccelByte Go SDK](https://github.com/AccelByte/accelbyte-go-sdk) — AGS platform SDK and gRPC plugin utilities
