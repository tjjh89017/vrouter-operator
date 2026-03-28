# gRPC Agent Implementation Plan

## Context

The vrouter-operator currently pushes configuration to VyOS routers inside VMs via QGA (KubeVirt SPDY / Proxmox REST). To support bare metal VyOS routers with no hypervisor-level management channel, a gRPC agent mechanism is needed: the operator pushes config to a gRPC server, and agents connect to pull and apply configurations.

Design reference: `docs/proposals/grpc-agent-architecture.md`

**Key decisions:**
- **Adapter pattern**: The gRPC provider implements the existing `Provider` interface so the controller requires no changes.
- **Separate repository**: gRPC server and client live in the `vrouter-daemon` repo. No git submodules — both repos sit side-by-side in the same parent directory.
- **Go module dependency**: The operator imports `vrouter-daemon/pkg/...` via `go.mod`.
- **Three binaries**: `vrouter-server` (server only), `vrouter-agent` (agent only), `vrouter-daemon` (mixed mode — both server and agent in one process).

---

## Part A: vrouter-daemon Repository (New Repo)

### Phase A1: Proto Definitions + gRPC Server Core

**Goal**: Scaffold the repository, define the protobuf service, and implement the core gRPC server (connection management, agent registration, message routing).

**Deliverables:**

1. Repository scaffold: `github.com/tjjh89017/vrouter-daemon`
   ```
   vrouter-daemon/
   ├── proto/agent.proto
   ├── gen/go/                    # protoc-generated Go code
   ├── pkg/
   │   ├── server/               # server library (imported by vrouter-operator)
   │   │   ├── server.go         # gRPC server, Connect handler
   │   │   ├── registry.go       # Agent registry (sync.Map)
   │   │   └── pool.go           # AgentPool interface + in-memory implementation
   │   └── agent/                # agent library
   │       └── client.go         # gRPC client, message handling
   ├── cmd/
   │   ├── vrouter-server/main.go  # server only binary
   │   ├── vrouter-agent/main.go     # agent only binary
   │   └── vrouter-daemon/main.go   # mixed mode binary (future)
   ├── Makefile                    # proto generate, build
   └── go.mod
   ```

2. `proto/agent.proto`:
   ```protobuf
   service AgentService {
     rpc Connect(stream AgentMessage) returns (stream ServerMessage) {}
   }
   message AgentMessage { string type = 1; bytes payload = 2; }
   message ServerMessage { string type = 1; string id = 2; bytes payload = 3; }
   ```

3. `pkg/server/pool.go` — AgentPool interface:
   ```go
   type AgentPool interface {
       ApplyConfig(ctx context.Context, agentID string, payload []byte) (*ConfigResult, error)
       GetStatus(agentID string) (*AgentStatus, error)
       IsConnected(agentID string) bool
   }
   ```

4. Server core functionality:
   - Agent registry (concurrent map: agentID → stream)
   - Connect handler: read first `register` message, store in registry, pump messages
   - Request correlation: `apply_config` carries a UUID `id`, blocks until matching `config_ack`
   - Heartbeat / keepalive configuration
   - Clean removal from registry on stream close

5. Binaries:
   - `vrouter-server`: standalone gRPC server binary (`cmd/vrouter-server/main.go`)
   - `vrouter-agent`: standalone agent binary (`cmd/vrouter-agent/main.go`)
   - `vrouter-daemon`: mixed mode binary — runs both server and agent in one process (`cmd/vrouter-daemon/main.go`)

**Verification**: Unit tests for mock streams, registration, and ApplyConfig correlation.

### Phase A2: Go Test Agent + E2E Validation

**Goal**: Build a minimal Go-based test agent to validate the full protocol end-to-end.

**Deliverables:**

1. `cmd/vrouter-agent/main.go` (extend with test/debug capabilities):
   - Connect to server, send `register`
   - Receive `apply_config` → respond with `config_ack`
   - Send periodic `status` messages

2. E2E tests:
   - Start `vrouter-server` + `vrouter-agent`
   - Call `AgentPool.ApplyConfig()` and assert success
   - Test agent disconnect / reconnect
   - Test `ApplyConfig` timeout (agent does not respond)
   - Concurrency test (multiple agents connected simultaneously)

3. Error handling hardening:
   - Context cancellation propagation
   - Duplicate agentID handling
   - Graceful shutdown

**Verification**: All E2E integration tests pass.

### Phase A3: Python VyOS Agent

**Goal**: Production agent that runs on bare metal VyOS routers.

**Deliverables:**

1. `agent/` (Python, separate from Go cmd/):
   - gRPC client using `grpcio`
   - `apply_config` handler:
     - Load init config from `/config/vrouter-agent/init-config.txt`
     - Merge: init config takes precedence on conflicts
     - Apply via `commit-confirm` (60s timeout)
     - Connectivity check → confirm or let auto-rollback expire
   - `get_status` handler: read interface/route/BGP state
   - Automatic reconnect with exponential backoff
   - Periodic status reporting

2. Configuration: `--server`, `--agent-id`, `--init-config`, `--backend vyos`
3. Systemd service file
4. Setup documentation

**Verification**: Unit tests for init config merge logic; integration test against the Go server.

---

## Part B: vrouter-operator Integration (This Repo)

### Phase B1: API Types + Webhook (Can start after Phase A1)

**Goal**: Add the `grpc` provider type, extend the CRD schema and validation.

**Files to modify:**

- `api/v1/grpc_types.go` (new file):
  ```go
  const ProviderGRPCAgent ProviderType = "grpc"
  type GRPCAgentConfig struct {
      AgentID string `json:"agentID"`
  }
  ```

- `api/v1/shared_types.go`:
  - Add `grpc` to the `ProviderType` enum
  - Add `GRPCAgent *GRPCAgentConfig` field to `ProviderConfig`

- `internal/webhook/v1/validation.go`:
  - Add `case vrouterv1.ProviderGRPCAgent:` validating `GRPCAgent != nil` and `AgentID != ""`

- Run `make generate && make manifests`

**Verification**: Webhook unit tests pass; `make test` passes.

### Phase B2: gRPC Provider Adapter (Can start after Phase A1)

**Goal**: Implement a gRPC provider that satisfies the existing `Provider` interface.

**New files:**

- `internal/provider/grpc/provider.go`:
  ```go
  type Provider struct {
      agentID    string
      pool       AgentPool     // imported from vrouter-daemon
      configBuf  []byte        // buffered by WriteFile
      lastResult *ConfigResult // stored by ExecScript
  }
  ```
  - `IsVMRunning` → `pool.IsConnected(agentID)`
  - `CheckReady` → `pool.IsConnected(agentID)` + `pool.GetStatus(agentID)` to confirm agent readiness
  - `WriteFile` → buffer content into `configBuf`
  - `ExecScript` → call `pool.ApplyConfig(agentID, configBuf)`, store result in `lastResult`, return synthetic PID
  - `GetExecStatus` → convert `lastResult` to `ExecStatus{Exited: true, ExitCode: ...}`

- `internal/provider/provider.go`:
  - Add `case vrouterv1.ProviderGRPCAgent:` to the factory to construct the gRPC provider

**Verification**: Unit tests with a mock AgentPool; verify adapter behavior.

### Phase B3: Manager Wiring + Integration Tests

**Goal**: Start the gRPC server within the operator process and wire the AgentPool into the provider factory.

**Files to modify:**

- `go.mod`: Add `github.com/tjjh89017/vrouter-daemon` dependency
- `cmd/main.go`:
  - Create gRPC server + AgentPool (in-memory, same process)
  - Register as `manager.Runnable` via `mgr.Add()` (participates in leader election + graceful shutdown)
  - Add flag `--grpc-listen-address` (default `:50051`)
  - Pass AgentPool to the provider factory (or inject globally)

- `internal/provider/provider.go`: `New()` accepts an AgentPool parameter

**Verification**: envtest integration tests (VRouterTarget type=grpc → create VRouterConfig → mock AgentPool → verify reconcile).

---

## Phase Dependency Graph

```
Phase A1 (proto + server)
    │
    ├──→ Phase A2 (test agent + E2E)
    │        │
    │        └──→ Phase A3 (Python agent)
    │
    ├──→ Phase B1 (API types + webhook)
    │
    └──→ Phase B2 (gRPC provider adapter)
              │
              └──→ Phase B3 (manager wiring)
```

- A1 is the foundation for everything
- A2, B1, and B2 can proceed in parallel after A1 is complete
- A3 depends on A2 (requires a validated protocol)
- B3 depends on B1 + B2 (requires both API types and the provider adapter)

---

## Verification Summary

| Phase | Verification |
|-------|-------------|
| A1 | `go test ./pkg/server/...` |
| A2 | E2E integration tests (start vrouter-server + vrouter-agent) |
| A3 | Unit tests + integration tests against the Go server |
| B1 | `make test` (webhook tests) |
| B2 | `go test ./internal/provider/grpc/...` |
| B3 | `make test` (full envtest suite), manual run with `ENABLE_WEBHOOKS=false go run ./cmd/main.go --grpc-listen-address=:50051` |

---

## Repository Naming and Structure

**New repo**: `github.com/tjjh89017/vrouter-daemon`

**Binaries** (three binaries from the same repo):
- `vrouter-server` — server only, runs in Kubernetes alongside the operator
- `vrouter-agent` — agent only, runs on bare metal VyOS routers
- `vrouter-daemon` — mixed mode (server + agent in one process), useful for dev/testing or single-node setups

Local development directory layout (side-by-side):
```
~/git/
├── vrouter-operator/    # this repo
└── vrouter-daemon/       # new repo
```

Go module reference (in vrouter-operator's go.mod):
```
require github.com/tjjh89017/vrouter-daemon v0.1.0
```

During development, use a `replace` directive to point to the local path:
```
replace github.com/tjjh89017/vrouter-daemon => ../vrouter-daemon
```

---

## Next Steps

1. Create the `vrouter-daemon` git repository
2. Start with Phase A1 (proto + gRPC server)
3. After A1 is complete, begin Phase B1 + B2 in the operator repo
