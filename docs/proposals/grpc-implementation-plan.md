# gRPC Agent Implementation Plan

## Context

The vrouter-operator currently pushes configuration to VyOS routers inside VMs via QGA (KubeVirt SPDY / Proxmox REST). To support bare metal VyOS routers, a standalone daemon service is needed: the daemon manages agent connections and exposes a control API, while the operator calls that API through a provider adapter.

Design reference: `docs/proposals/grpc-agent-architecture.md`

**Key decisions:**
- **API isolation**: The operator and daemon are fully decoupled. No shared Go imports. The wire protocol (proto file) is the only contract.
- **Separate repository**: `vrouter-daemon` is an independently operable service.
- **Adapter pattern**: The operator implements a `grpc` provider that calls the server's control API.
- **Three binaries**: `vrouter-server` (server only), `vrouter-agent` (agent only), `vrouter-daemon` (mixed mode).
- **Two gRPC services in the daemon**:
  - **ControlService** (operator-facing): unary RPCs for config push, status query
  - **AgentService** (agent-facing): bidirectional streaming for agent connections

---

## Architecture

```
vrouter-operator process               vrouter-daemon process
┌──────────────────────┐               ┌──────────────────────────────┐
│  VRouterConfig       │               │  ControlService (port 50052) │
│  Controller          │   gRPC        │         │                    │
│       │              │──────────────→│  ┌──────▼──────┐             │
│  ┌────▼────────┐     │               │  │  Registry +  │             │
│  │ gRPC Client │     │               │  │  Dispatcher  │             │
│  │ (internal/) │     │               │  └──────┬──────┘             │
│  └─────────────┘     │               │         │                    │
└──────────────────────┘               │  ┌──────▼──────┐             │
                                       │  │ AgentService │             │
                                       │  │ (port 50051) │             │
                                       │  └──────┬──────┘             │
                                       └─────────│────────────────────┘
                                                 │ gRPC bidir stream
                                           VyOS agents
```

**Two separate ports with different network policies:**
- Port 50051 (agent-facing): exposed via LoadBalancer/NodePort for external agents
- Port 50052 (operator-facing): ClusterIP only, internal to the cluster

---

## Proto Definitions

### `proto/control/v1/control.proto` (operator-facing)

```protobuf
syntax = "proto3";
package vrouter.control.v1;

service ControlService {
  rpc IsConnected(IsConnectedRequest) returns (IsConnectedResponse) {}
  rpc GetStatus(GetStatusRequest) returns (GetStatusResponse) {}
  rpc ApplyConfig(ApplyConfigRequest) returns (ApplyConfigResponse) {}
}

message IsConnectedRequest { string agent_id = 1; }
message IsConnectedResponse { bool connected = 1; }

message GetStatusRequest { string agent_id = 1; }
message GetStatusResponse {
  bool   has_status = 1;
  bytes  status_json = 2;
  string agent_version = 3;
}

message ApplyConfigRequest {
  string agent_id = 1;
  bytes  config_payload = 2;
  int32  timeout_seconds = 3;
}

message ApplyConfigResponse {
  bool   success = 1;
  int32  exit_code = 2;
  string stdout = 3;
  string stderr = 4;
  string error_message = 5;
}
```

### `proto/agent/v1/agent.proto` (agent-facing)

```protobuf
syntax = "proto3";
package vrouter.agent.v1;

service AgentService {
  rpc Connect(stream AgentMessage) returns (stream ServerMessage) {}
}

message AgentMessage { string type = 1; bytes payload = 2; }
message ServerMessage { string type = 1; string id = 2; bytes payload = 3; }
```

### API Contract Sharing

The proto files are maintained in `vrouter-daemon` as the source of truth. The operator copies `control.proto` and generates its own Go stubs. CI can verify the copies stay in sync.

---

## Part A: vrouter-daemon Repository

### Repository Structure

Everything under `internal/` — no external Go consumers.

```
vrouter-daemon/
├── proto/
│   ├── control/v1/control.proto     # operator-facing API (source of truth)
│   └── agent/v1/agent.proto         # agent-facing API
├── gen/go/
│   ├── controlpb/                   # generated from control.proto
│   └── agentpb/                     # generated from agent.proto
├── internal/
│   ├── controlapi/                  # ControlService gRPC handler
│   │   ├── service.go
│   │   └── service_test.go
│   ├── agentapi/                    # AgentService gRPC handler (bidir streams)
│   │   ├── service.go
│   │   └── service_test.go
│   ├── registry/                    # agent connection registry
│   │   ├── registry.go              # agentID → stream, thread-safe
│   │   └── registry_test.go
│   ├── dispatch/                    # request-response correlation
│   │   ├── dispatcher.go            # send to agent, wait for ack, timeout
│   │   └── dispatcher_test.go
│   └── config/                      # server configuration (flags, env)
│       └── config.go
├── cmd/
│   ├── vrouter-server/main.go       # server only binary
│   ├── vrouter-agent/main.go        # agent only binary
│   └── vrouter-daemon/main.go       # mixed mode (server + agent)
├── agent/                           # Python VyOS agent (future)
├── Makefile
├── go.mod
└── go.sum
```

### Phase A1: Proto + Server Core

**Goal**: Scaffold repo, define both proto files, implement server internals.

**Deliverables:**

1. Proto definitions: `control.proto` + `agent.proto`, code generation via Makefile
2. `internal/registry/`: agent connection registry (sync.RWMutex map, Register/Deregister/IsConnected/GetStream)
3. `internal/dispatch/`: request-response correlation (send `apply_config` to agent stream, block until `config_ack` or timeout)
4. `internal/agentapi/`: AgentService handler — read `register`, store in registry, pump messages, notify dispatcher on `config_ack`
5. `internal/controlapi/`: ControlService handler — delegates to registry and dispatcher
6. `cmd/vrouter-server/main.go`: starts both gRPC services on two ports

**Verification**: `go test ./internal/...`

### Phase A2: Go Agent + E2E Validation

**Goal**: Build the Go agent binary, validate full protocol end-to-end.

**Deliverables:**

1. `internal/agent/`: agent client library (connect, register, handle messages, reconnect)
2. `cmd/vrouter-agent/main.go`: standalone agent binary
3. `cmd/vrouter-daemon/main.go`: mixed mode binary (server + agent in one process)
4. E2E tests:
   - Start server + agent, call ControlService.ApplyConfig, verify success
   - Agent disconnect / reconnect
   - ApplyConfig timeout (agent unresponsive)
   - Concurrent multiple agents
   - Duplicate agentID handling
   - Graceful shutdown

**Verification**: E2E integration tests pass.

### Phase A3: Python VyOS Agent

**Goal**: Production agent for bare metal VyOS routers.

**Deliverables:**

1. `agent/` (Python): grpcio client, init config merge, commit-confirm, auto-reconnect
2. Configuration: `--server`, `--agent-id`, `--init-config`, `--backend vyos`
3. Systemd service file + setup documentation

**Verification**: Unit tests for init config merge; integration test against Go server.

---

## Part B: vrouter-operator Integration (This Repo)

### Phase B1: API Types + Webhook

**Goal**: Add `grpc` provider type to CRD schema and validation.

**Files to modify:**

- `api/v1/grpc_types.go` (new):
  ```go
  const ProviderGRPC ProviderType = "grpc"
  type GRPCConfig struct {
      AgentID string `json:"agentID"`
  }
  ```

- `api/v1/shared_types.go`:
  - Add `grpc` to `ProviderType` enum
  - Add `GRPC *GRPCConfig` field to `ProviderConfig`

- `internal/webhook/v1/validation.go`:
  - Add `case vrouterv1.ProviderGRPC:` validating `GRPC != nil` and `AgentID != ""`

- Run `make generate && make manifests`

**Verification**: `make test`

### Phase B2: gRPC Provider Adapter

**Goal**: Implement the gRPC provider that calls the vrouter-daemon ControlService.

**New files under `internal/provider/grpc/`:**

- `client.go` — gRPC client wrapping the generated ControlService stubs:
  ```go
  type Client struct {
      pb controlpb.ControlServiceClient
  }

  func (c *Client) IsConnected(ctx context.Context, agentID string) (bool, error) { ... }
  func (c *Client) GetStatus(ctx context.Context, agentID string) (*AgentStatus, error) { ... }
  func (c *Client) ApplyConfig(ctx context.Context, agentID string, payload []byte, timeoutSec int) (*ApplyResult, error) { ... }
  ```

- `provider.go` — Provider adapter:
  - `IsVMRunning` → `client.IsConnected(agentID)`
  - `CheckReady` → `client.IsConnected(agentID)` + `client.GetStatus(agentID)`
  - `WriteFile` → buffer content
  - `ExecScript` → `client.ApplyConfig(agentID, buffered)`, store result, return synthetic PID
  - `GetExecStatus` → return stored result as `ExecStatus{Exited: true, ...}`

- `proto/control.proto` — copied from vrouter-daemon (source of truth)
- `gen/controlpb/` — generated Go stubs

- `internal/provider/provider.go`: add `case vrouterv1.ProviderGRPC:` to factory

If a different transport is needed in the future, add a new provider (e.g. `internal/provider/rest/`).

**Verification**: `go test ./internal/provider/grpc/...` with mock gRPC server

### Phase B3: Manager Wiring + Integration Tests

**Goal**: Wire the gRPC provider into the operator startup.

**Files to modify:**

- `cmd/main.go`:
  - Add flag `--daemon-address` (e.g. `vrouter-daemon.vrouter-system.svc:50052`)
  - Create gRPC connection + gRPC client at startup
  - Pass client to provider factory

- `internal/provider/provider.go`: `New()` accepts optional gRPC client parameter

**Verification**: envtest integration test with mock gRPC server; `make test` passes.

---

## Phase Dependency Graph

```
Phase A1 (proto + server core)
    │
    ├──→ Phase A2 (Go agent + E2E)
    │        │
    │        └──→ Phase A3 (Python agent)
    │
    ├──→ Phase B1 (API types + webhook)
    │
    └──→ Phase B2 (gRPC provider adapter)
              │
              └──→ Phase B3 (manager wiring)
```

- A1 is the foundation
- A2, B1, B2 can proceed in parallel after A1
- A3 depends on A2
- B3 depends on B1 + B2

---

## Verification Summary

| Phase | Verification |
|-------|-------------|
| A1 | `go test ./internal/...` |
| A2 | E2E integration tests (vrouter-server + vrouter-agent) |
| A3 | Unit tests + integration tests against Go server |
| B1 | `make test` (webhook tests) |
| B2 | `go test ./internal/provider/grpc/...` |
| B3 | `make test` (full envtest suite) |

---

## Repository Naming and Structure

**New repo**: `github.com/tjjh89017/vrouter-daemon`

**Binaries** (three binaries from the same repo):
- `vrouter-server` — server only, runs in Kubernetes
- `vrouter-agent` — agent only, runs on bare metal VyOS routers
- `vrouter-daemon` — mixed mode (server + agent in one process)

Local development directory layout (side-by-side):
```
~/git/
├── vrouter-operator/    # this repo
└── vrouter-daemon/      # new repo
```

No Go module dependency between repos. The only shared artifact is `control.proto`.

---

## Deployment

```
Namespace: vrouter-system

Deployment: vrouter-operator        (1 replica, leader-elected)
  --daemon-address=vrouter-daemon.vrouter-system.svc:50052

Deployment: vrouter-daemon          (1+ replicas)
  Port 50051: agent-facing (AgentService) → LoadBalancer/NodePort
  Port 50052: operator-facing (ControlService) → ClusterIP

Service: vrouter-daemon             (ClusterIP, port 50052)
Service: vrouter-daemon-agents      (LoadBalancer, port 50051)
```

---

## Next Steps

1. Start Phase A1 in the `vrouter-daemon` repo
2. After A1, begin B1 + B2 in parallel in the operator repo
