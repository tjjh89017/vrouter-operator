# gRPC Agent Architecture for Bare Metal VyOS

**Status**: Proposal (not yet implemented)
**Date**: 2026-03-15

## Goal

Support bare metal VyOS routers that have no hypervisor-level management channel (QGA, Proxmox API). A lightweight agent on VyOS initiates a gRPC connection back to the controller cluster, enabling config push and status pull over a single bidirectional stream.

## Motivation

| Scenario | Communication | Available Today |
|----------|--------------|-----------------|
| KubeVirt VM | QGA (hypervisor channel, no network needed) | Yes |
| Proxmox VM | Proxmox REST API + QGA | Yes |
| Bare metal VyOS | **None** — no hypervisor, no QGA | No |

QGA is deliberately retained for KubeVirt — it works without network and is the right choice when SSH/network login must be disabled. The gRPC agent is a new, parallel provider for environments where a hypervisor channel does not exist.

## Architecture Overview

```
┌─────────────────────────────────────────────────────┐
│  vrouter-operator process                           │
│                                                     │
│  ┌──────────────┐       ┌───────────────────┐       │
│  │  Controller   │──────→│  AgentPool (API)   │       │
│  │  (reconciler) │       │                   │       │
│  │  knows CRDs   │       │  ApplyConfig(id)  │       │
│  │  no gRPC      │       │  GetStatus(id)    │       │
│  └──────────────┘       └────────┬──────────┘       │
│                                  │                  │
│                          ┌───────┴──────────┐       │
│                          │  gRPC Server      │       │
│                          │                  │       │
│                          │  manages streams  │       │
│                          │  no CRD knowledge │       │
│                          └──────────────────┘       │
│                                  ▲                  │
└──────────────────────────────────│──────────────────┘
                                   │
                     VyOS agents connect here
```

### Separation of Concerns

| Layer | Responsibility | Does NOT touch |
|-------|---------------|----------------|
| **gRPC Server** | Stream lifecycle, agent registry, reconnect, heartbeat, message routing | CRD, reconcile, k8s API |
| **AgentPool API** | Go interface for controller; abstracts gRPC details | Stream internals, proto types |
| **Controller** | Reconcile CRDs, decide when to send config, write back status | gRPC connections, registry |

The gRPC server is completely k8s-agnostic. It is a connection manager with a registry — nothing more.

## Provider Integration

New provider alongside existing ones:

```
internal/provider/
├── kubevirt/     # QGA (existing)
├── proxmox/      # Proxmox REST API + QGA (existing)
└── grpc/         # bare metal VyOS agent (new)
```

New API type (similar to `KubeVirtConfig`):

```go
// api/v1/grpc_types.go
type GRPCAgentConfig struct {
    // AgentID is the identity the agent uses when registering.
    // Must match the agent_id configured on the VyOS side.
    AgentID string `json:"agentID"`
}
```

`ProviderConfig` gains a `grpcAgent` field; webhook validation extends the existing mutual-exclusion pattern.

## AgentPool Interface

```go
// internal/agentpool/pool.go

type AgentPool interface {
    // ApplyConfig sends a config payload to the agent and blocks until ack.
    // Returns error if agent is not connected or ack reports failure.
    ApplyConfig(ctx context.Context, agentID string, payload []byte) (*ConfigResult, error)

    // GetStatus returns the last cached status from the agent.
    // Returns nil if agent has never reported status.
    GetStatus(agentID string) (*AgentStatus, error)
}
```

No `Watch()`, no `Connected()`. The controller uses standard k8s requeue-on-error:

```
Reconcile VRouterConfig
  └── AgentPool.ApplyConfig(agentID, payload)
      ├── agent online  → send → wait ack → success
      └── agent offline → return error
          └── controller: RequeueAfter: 30s
```

Agent comes online → next requeue picks it up. Maximum latency is one requeue interval, acceptable for config management.

## gRPC Protocol

### Proto Definition

```protobuf
syntax = "proto3";
package vrouter.agent.v1;

service AgentService {
  // Single bidirectional stream per agent connection.
  rpc Connect(stream AgentMessage) returns (stream ServerMessage) {}
}

message AgentMessage {
  string type    = 1;   // message type discriminator
  bytes  payload = 2;   // JSON-encoded body
}

message ServerMessage {
  string type    = 1;   // message type discriminator
  string id      = 2;   // request ID for correlation
  bytes  payload = 3;   // JSON-encoded body
}
```

The proto is intentionally minimal — it never needs to change. All message evolution happens in the JSON payload.

### Message Types

**Agent → Server:**

| type | payload | description |
|------|---------|-------------|
| `register` | `{"agent_id": "vyos-tokyo-1", "version": "1.4.1"}` | First message, required |
| `status` | `{"interfaces": [...], "bgp": {...}, "uptime": 86400}` | Periodic or on-change |
| `config_ack` | `{"id": "req-123", "success": true, "error": ""}` | Response to apply_config |

**Server → Agent:**

| type | payload | description |
|------|---------|-------------|
| `apply_config` | `{"id": "req-123", "config": "set interfaces ..."}` | Push config to agent |
| `get_status` | `{"id": "req-124"}` | Request immediate status report |

### Connection Lifecycle

```
VyOS Agent                         gRPC Server
  │                                    │
  │── register ───────────────────────→│  store in agent registry
  │                                    │
  │── status ─────────────────────────→│  cache latest status
  │                                    │
  │←── apply_config(id="r1") ─────────│  triggered by controller
  │── config_ack(id="r1", ok) ───────→│  unblock ApplyConfig() caller
  │                                    │
  │── status ─────────────────────────→│  periodic / on-change
```

### Request-Response Correlation

Server attaches a UUID `id` to each `apply_config`. Agent echoes the same `id` in `config_ack`. The gRPC server uses a pending-request map to correlate:

```go
func (s *Server) ApplyConfig(agentID string, payload []byte) (*ConfigResult, error) {
    id := uuid.New().String()
    ch := make(chan *ConfigResult, 1)
    s.pending[id] = ch

    s.send(agentID, &ServerMessage{
        Type:    "apply_config",
        Id:      id,
        Payload: payload,
    })

    select {
    case result := <-ch:
        return result, nil
    case <-ctx.Done():
        return nil, ctx.Err()
    }
}
```

### Why JSON over Protobuf for Payloads

- **Agent is Python** — JSON is native, no protobuf compiler needed beyond the envelope
- **Debuggability** — human-readable on the wire
- **Extensibility** — new fields without proto regeneration
- **Config is text** — VyOS config is `set` commands, naturally string/JSON

## Scaling: Multi-Instance gRPC Server

### Single Instance (initial)

gRPC server runs in the same process as the controller. AgentPool uses an in-memory map. No external dependencies.

### Multiple Instances (scale-out)

gRPC server deployed as a separate Deployment (supports HPA). Agent connections are distributed across pods via a Service.

```
Service: grpc-server.svc (ClusterIP)
  ├── pod-0: holds stream for vyos-1, vyos-3
  ├── pod-1: holds stream for vyos-2
  └── pod-2: holds stream for vyos-4, vyos-5
```

#### Registry

Each pod writes its agent mappings to Redis with TTL:

```
key:   agent:vyos-tokyo-1
value: 10.0.1.5:50051      (pod IP)
TTL:   60s                  (refreshed every 30s)
```

- Agent connects → pod writes to Redis
- Pod dies → TTL expires → entry removed automatically
- Agent detects disconnect → reconnects through Service LB → lands on new pod → new Redis entry

#### Request Routing via Forward

Controller sends requests to the Service (any pod). The receiving pod checks locally first, then forwards if needed:

```go
func (s *Server) ApplyConfig(ctx context.Context, req *pb.ApplyRequest) (*pb.ApplyResponse, error) {
    // Do I have this agent?
    if stream, ok := s.agents[req.AgentId]; ok {
        return s.applyLocal(stream, req)
    }

    // Look up Redis for which instance has it
    addr, err := s.registry.Lookup(req.AgentId)
    if err != nil {
        return nil, status.Error(codes.NotFound, "agent not connected")
    }

    // Forward to the correct pod
    return s.forwardTo(ctx, addr, req)
}
```

Controller only knows one endpoint (the Service). All routing is encapsulated inside the gRPC server layer.

#### Pod Identity

Pods only need their own IP (via Downward API), not stable hostnames. No StatefulSet required:

```yaml
env:
  - name: POD_IP
    valueFrom:
      fieldRef:
        fieldPath: status.podIP
```

### Evolution Path

| Stage | AgentPool impl | gRPC Server | External deps |
|-------|---------------|-------------|---------------|
| Dev / small | In-memory map | Same process | None |
| Single replica standalone | gRPC client to Service | Separate Deployment | None |
| Multi-replica | gRPC client to Service | Deployment + HPA | Redis |

The controller code and AgentPool interface remain unchanged across all stages. Only the AgentPool implementation and gRPC server deployment topology change.

## VyOS Agent (Separate Repo)

The agent runs on VyOS as a daemon. Separate repository: `vrouter-agent`.

- **Language**: Python (VyOS has `vyos.configsession` API)
- **Role**: gRPC client, connects to controller cluster
- **Bootstrap**: Server address injected via cloud-init or static config
- **Reconnect**: Automatic with backoff on disconnect
- **Config apply**: Receives `set` commands, applies via VyOS API
- **Status report**: Reads interfaces, routes, BGP state; sends periodically or on-change

## Directory Structure (New Files)

```
api/
├── v1/
│   └── grpc_types.go              # GRPCAgentConfig type
├── grpc/
│   └── v1/
│       └── agent.proto            # proto definition
internal/
├── agentpool/
│   ├── pool.go                    # AgentPool interface + types
│   ├── grpc_pool.go               # in-memory impl (initial)
│   └── grpc_pool_test.go
├── grpcserver/
│   ├── server.go                  # gRPC server, pure stream management
│   └── server_test.go
├── provider/
│   └── grpc/
│       └── provider.go            # Provider impl, calls AgentPool
```

## Files to Modify

- `api/v1/shared_types.go` — add `GRPCAgent *GRPCAgentConfig` to `ProviderConfig`
- `internal/webhook/v1/validation.go` — extend provider mutual-exclusion validation
- `internal/provider/provider.go` — register gRPC provider in factory
- `cmd/main.go` — start gRPC server, wire AgentPool into provider
