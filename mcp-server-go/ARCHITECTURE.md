# Architecture — Cube Container MCP Server

> **For AI agents**: This file is your map. Read it before modifying any code.
> It tells you WHERE things are, WHY they're structured this way, and WHAT
> patterns to follow when adding new features.

## Overview

Cube Container is a container orchestration platform controlled entirely through
MCP (Model Context Protocol). An AI agent communicates with it using 161 tools.
The primary operations interface IS natural language — a React web dashboard
exists for status visualization, but all control flows through MCP.

```
┌──────────┐     ┌──────────────────────────────────────────┐     ┌──────────┐
│ AI Agent │ ──► │ MCP Server (Go)                          │ ──► │ Backend  │
│ (Claude, │     │                                          │     │ Docker / │
│  GPT...) │     │  ┌─────────┐  ┌──────────┐  ┌────────┐  │     │ Cube     │
│          │ ◄── │  │ 161     │  │ Auth +   │  │ 11     │  │     │ Engine   │
│          │     │  │ Tools   │  │ RBAC +   │  │ Watch  │  │     │          │
│          │     │  │         │  │ Rate     │  │ Loops  │  │     └──────────┘
│          │     │  └─────────┘  │ Limit +  │  └────────┘  │
│          │     │               │ Audit    │              │     ┌──────────┐
│          │     │               └──────────┘              │ ──► │ Caddy    │
│          │     │                 stdio / HTTP             │     │ TLS+WAF  │
│          │     └──────────────────────────────────────────┘     └──────────┘
```

## Code Organization

### File Categories

Every `.go` file falls into exactly one of these categories:

#### 1. Entry Point & Routing

| File | Responsibility |
|------|---------------|
| `server.go` | `main()`, manager initialization, HTTP middleware, stdio/HTTP mode |
| `tools_registration.go` | Tool registration (`registerAllTools`), all 161 `registerTool` calls |
| `tools_helpers.go` | Tool builders, arg extraction, handler registry (for scheduled jobs) |
| `handlers_basic.go` | Handlers for cluster, containers, templates, deploy, volumes, backup |
| `handlers_phase2.go` | Handlers for images, deploy rollout, logs, envs, jobs, DBs, certs, events |
| `handlers_secure.go` | Handlers for secure sandbox operations |

**Pattern**: To add a new tool, you touch these files:
1. `tools_registration.go` — register the tool with `registerTool(s, tool(...), handleX)`
2. `handlers_basic.go` or `handlers_phase2.go` — implement `func handleX(ctx, req) (*mcp.CallToolResult, error)`

#### 2. Backend Abstraction

| File | Responsibility |
|------|---------------|
| `backend.go` | `ContainerBackend` interface + auto-detection (`newBackend()`) |
| `docker_client.go` | Docker Engine API implementation |
| `client.go` | CubeAPI implementation |

**Pattern**: The interface is the contract. Both implementations must satisfy it.
When adding backend-specific operations, extend the interface, not the callers.

#### 3. Feature Modules (one file per feature domain)

Each feature module follows this structure:

```
feature_name.go
├── Types (structs returned to the AI)
├── Validator functions (if handling untrusted input)
├── Manager struct (stateful, with sync.Mutex)
├── newFeatureManager() constructor
├── Disk persistence (loadFromDisk / saveToDisk)
├── Business logic methods
└── No handlers (those go in handlers_basic.go or handlers_phase2.go)
```

> **Note on tool counts**: Handlers are centralized in `handlers_basic.go`,
> `handlers_phase2.go`, and `handlers_secure.go` (3 files), but each tool is
> attributed to the feature module that owns its business logic. The sum of
> the "Tools" column below is 114; the remaining 15 tools (cluster health,
> container CRUD, templates, exec, backend_info) have no dedicated feature
> file — they call the backend directly from the handler. Additionally there
> are 32 hypervisor tools (VM, ZFS, GPU, cloud-init). Total: **161**.

| File | Feature | Tools |
|------|---------|-------|
| `images.go` | Docker image lifecycle | 5 |
| `deploy.go` | Git/code deployment | 3 |
| `deploy_rollout.go` | Rolling/blue-green updates | 1 |
| `scaling.go` | Replica management + LB | 5 |
| `health.go` | Probes + auto-restart watcher | 4 |
| `nodes.go` | Multi-node cluster (TLS-aware remote clients, AS-4) | 6 |
| `volumes.go` | Volume lifecycle + SSH migrate | 7 |
| `discovery.go` | Service discovery | 4 |
| `resources.go` | CPU/memory limits | 4 |
| `gc.go` | Image/volume garbage collector | 3 |
| `alerting.go` | Alert rules + webhooks | 4 |
| `configmaps.go` | Non-sensitive config | 5 |
| `secrets.go` | AES-256-GCM secrets | 4 |
| `backup.go` | Backup + restore | 5 |
| `routing.go` | Caddy routes + TLS | 4 |
| `networking.go` | Port maps, DNS, policies | 9 |
| `ha.go` | Active-passive failover (heartbeat rate-limited, AS-6) | 1 |
| `log_aggregation.go` | Multi-container log search | 2 |
| `audit_query.go` | Audit trail search | 1 |
| `environments.go` | Namespace isolation | 4 |
| `notifications.go` | Slack/Discord/Telegram/Email | 4 |
| `auth_tokens.go` | API token management | 3 |
| `jobs.go` | Scheduled tasks with real tool execution | 4 |
| `metrics.go` + `metrics_query.go` | Metrics export + query | 1 |
| `databases.go` | Managed DB provisioning | 3 |
| `certificates.go` | TLS cert inspection | 2 |
| `events.go` | Cluster event stream | 2 |
| `secure_sandbox.go` | KVM sandbox for untrusted code (egress, vault, snapshots). Security boundary = VM isolation, NOT command filtering (AS-1) | 8 |
| `scheduler.go` | Bin-packing node placement | 1 |
| `rollback.go` | Deployment versioning + rollback | 2 |
| `logstream.go` | SSE log streaming endpoint + `tail_container_logs` | 2 |
| `webhook.go` | Git webhook endpoint (`X-Git-Token` header auth, AS-5) | 1 |
| `hypervisor.go` | VM lifecycle via libvirt (create, start, stop, snapshot, migrate) | 13 |
| `hypervisor_zfs.go` | ZFS pool/dataset/snapshot/clone/rollback management | 12 |
| `hypervisor_gpu.go` | GPU detection, stats, VFIO passthrough (NVIDIA/AMD/Intel) | 4 |
| `hypervisor_cloudinit.go` | cloud-init user-data gen, ISO creation, template list | 3 |
| `hypervisor_validate.go` | Input validators for all hypervisor tools (names, paths, PCI addresses) | — |

#### 4. Security Layer

| File | Responsibility |
|------|---------------|
| `auth.go` | API keys, RBAC, rate limiting, **HMAC-SHA256** audit hash chain (keyed with `CUBE_SECRETS_KEY`, AS-7), HTTP middleware |
| `security.go` | Input validators (command allowlist + expanded exfiltration denylist, path traversal, SSRF, etc.) |
| `secrets.go` | AES-256-GCM secrets; `getSecretsKeyForAudit()` exposes the key for the HMAC audit chain |
| `security_test.go` | Validator unit tests |

#### 5. Tests

| File | Coverage |
|------|---------|
| `security_test.go` | Command allowlist, path validation, URL validation |
| `auth_test.go` | Key generation, RBAC, timing-safe comparison |
| `secrets_test.go` | AES-256-GCM encryption, argon2id key derivation |
| `backup_test.go` | Backup, restore, integrity verification |
| `e2e_test.go` | End-to-end container lifecycle (`-tags=e2e`) |
| `bench_test.go` | Performance benchmarks (`-tags=e2e`) |
| `concurrency_test.go` | Race condition stress tests |

#### 6. Infrastructure

| File | Responsibility |
|------|---------------|
| `Dockerfile` | Multi-stage build (Go + Caddy) |
| `Caddyfile` | TLS 1.3 + WAF + rate limiting config |
| `.github/workflows/ci.yml` | CI: build, test, vet, gosec, govulncheck, binary guard |
| `.gitignore` | Binary exclusion |

## Key Patterns

### Pattern: Adding a New Tool

1. **Create the feature module** (`myfeature.go`) following the module structure above
2. **Register the tool** in `tools_registration.go` → `registerAllTools()`
3. **Write the handler** in `handlers_basic.go` or `handlers_phase2.go`
4. **Add RBAC** in `auth.go` → `toolPermissions` map
5. **Add validators** in `security.go` if handling untrusted input
6. **Compile**: `go build ./...`
7. **Test**: `go test -race -count=1 ./...`

### Pattern: Manager + Watcher Loop

Background processes (health checks, alerts, GC, jobs) follow this pattern:

```go
type MyManager struct {
    mu      sync.Mutex
    stopCh  chan struct{}
    running bool
    // ... state fields
}

func (m *MyManager) Start() {
    if m.running { return }
    m.running = true
    go m.watcherLoop()
}

func (m *MyManager) watcherLoop() {
    ticker := time.NewTicker(30 * time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-m.stopCh:
            return
        case <-ticker.C:
            m.tick()
        }
    }
}
```

Started from `main()`, stopped on process exit.

### Pattern: Disk Persistence

Stateful managers persist to JSON files under `/var/lib/cube-container/`:

```go
func (m *MyManager) saveToDisk() error {
    os.MkdirAll(m.rootDir, 0700)
    data, _ := json.MarshalIndent(m.state, "", "  ")
    return os.WriteFile(m.filePath(), data, 0600)
}
```

Root dirs are configurable via `CUBE_*_ROOT` environment variables.

### Pattern: Input Validation

ALL user-supplied input must pass through a validator in `security.go`:

```go
// BAD — raw user input used directly
exec("docker update --memory " + userInput + " " + containerID)

// GOOD — validated through security.go
if err := validateContainerID(containerID); err != nil {
    return nil, err
}
exec("docker update --memory " + memoryLimit + " " + containerID)
```

### Pattern: Handler Structure

Every handler follows the same shape:

```go
func handleMyTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    args := parseArgs(req)
    requiredArg := argString(args, "required_arg")
    if requiredArg == "" {
        return errResult("required_arg is required"), nil
    }
    optionalArg := argInt(args, "optional_arg", 42) // default 42

    data, err := myMgr.DoThing(requiredArg, optionalArg)
    if err != nil {
        return unwrapError(err), nil
    }
    return okResult(data), nil
}
```

Key helpers: `parseArgs`, `argString`, `argInt`, `argFloat`, `argMap`, `argStringSlice`, `okResult`, `errResult`, `unwrapError`.

## Data Flow

### Tool Invocation Flow

```
AI Agent
  → JSON-RPC over stdio/HTTP
  → MCP framework (mcp-go)
  → registerAllTools dispatch table
  → Auth middleware (if HTTP mode): extract API key, check RBAC
  → Handler function (handleX)
  → Feature manager (myMgr.DoThing)
  → ContainerBackend interface
  → Docker/Cube engine
  → JSON response
  → okResult(data) or errResult(msg)
  → MCP framework wraps in CallToolResult
  → Back to AI Agent
```

### Background Watcher Flow

```
main()
  → manager.Start()
  → goroutine: watcherLoop()
  → every N seconds: tick()
  → check state (containers, disks, thresholds)
  → if condition: take action (restart, prune, alert)
  → persist changes to disk
```

## Dependencies

| Dependency | Purpose |
|-----------|---------|
| `github.com/mark3labs/mcp-go` | MCP protocol framework (tool registration, stdio/HTTP transport) |
| Go stdlib | Everything else (no web framework, no ORM, no external HTTP router) |

**Philosophy**: Minimal dependencies. The server uses only the MCP framework and Go stdlib. This keeps the binary small (~8.5MB) and the attack surface narrow.

## Build & Deploy

```bash
# Development
go build ./...
go test -race -count=1 ./...

# Production binary
CGO_ENABLED=0 go build -ldflags "-s -w" -o mcp-server-go .

# Docker
docker build -t cube-container .
```

## Known Limitations

- **Secure sandbox command filtering** (AS-1): `secure_sandbox_exec` does NOT apply the `validateCommand()` allowlist. This is by design — the security boundary for untrusted code is KVM hardware isolation, not command filtering. The denylist in `security.go` provides defense-in-depth only.
- **`sh -c` allowlist bypass** (AS-3): `exec_in_container` uses `sh -c` which allows command chaining and pipes. The expanded denylist catches common exfiltration patterns but cannot prevent all bypasses. For truly untrusted code, use `secure_sandbox_exec` instead.
- **Inter-node TLS opt-in** (AS-4): Remote Docker connections default to plaintext TCP. Set `CUBE_DOCKER_TLS=true` in production. A warning is printed to stderr when plaintext is used.

See [Roadmap](README.md#roadmap) in the main README for planned features.
