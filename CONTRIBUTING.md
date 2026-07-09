# Contributing to Cube Container

Cube Container is a container-mode fork of [CubeSandbox](https://github.com/TencentCloud/CubeSandbox), focused on MCP-native container orchestration for edge nodes.

## Quick Start

```bash
# Clone
git clone https://github.com/schwabauerbriantomas-gif/cube-container.git
cd cube-container/mcp-server-go

# Build (no build tags needed — Docker backend is always compiled)
go build -o cube-mcp .

# Run tests
go test -v ./...

# Run e2e + benchmarks (these require the e2e build tag)
go test -tags=e2e -v ./...

# Run with race detector
go test -race -v ./...
```

## Architecture

The MCP server is a single Go binary with 161 tools. It auto-detects the backend
(Docker or Cube) at runtime — no build tags needed.

```
mcp-server-go/ (single binary, ~8.5MB)
├── server.go              — main(), manager init, HTTP middleware, stdio/HTTP mode
├── tools_registration.go  — all 161 tool registrations via registerTool()
├── tools_helpers.go       — tool builders, arg extraction, handler registry (jobs)
├── handlers_basic.go      — cluster, containers, templates, deploy, volumes, backup
├── handlers_phase2.go     — images, deploy rollout, logs, envs, jobs, DBs, certs, events
├── handlers_secure.go     — secure sandbox operations
├── backend.go             — ContainerBackend interface + auto-detection
├── docker_client.go       — Docker Engine API backend (always compiled, auto-detected)
├── client.go              — CubeAPI backend (edge nodes)
├── auth.go                — API keys (hashed), RBAC, rate limiting, HMAC-SHA256 audit chain
├── auth_tokens.go         — Programmatic token management
├── security.go            — Input validation (command allowlist + denylist, SSRF, path traversal)
├── secrets.go             — AES-256-GCM secrets (argon2id key derivation)
├── deploy.go              — Git deploy + persistent volumes + version tracking
├── deploy_rollout.go      — Rolling + blue-green deployment
├── scaling.go             — Replica management + load balancing
├── health.go              — Health probes + auto-restart watcher
├── nodes.go               — Multi-node cluster (TLS-aware remote clients)
├── volumes.go             — Volume lifecycle + SSH migration
├── backup.go              — Backup + restore with SHA256 integrity
├── scheduler.go           — Bin-packing node scheduling assistant
├── routing.go             — Caddy TLS automation
├── networking.go          — Port mappings, DNS aliases, network policies
├── ha.go                  — Active-passive high availability (heartbeat rate-limited)
├── jobs.go                — Scheduled jobs with real tool execution
├── logstream.go           — SSE log streaming endpoint
├── webhook.go             — Git webhook endpoint (X-Git-Token header auth)
├── secure_sandbox.go      — KVM sandbox for untrusted code (egress, vault, snapshots)
├── metrics.go             — Prometheus endpoint
├── rollback.go            — Deploy versioning
├── hypervisor.go          — VM lifecycle via libvirt/virsh (13 tools)
├── hypervisor_zfs.go      — ZFS storage management (12 tools)
├── hypervisor_gpu.go      — GPU detection, monitoring, VFIO passthrough (4 tools)
├── hypervisor_cloudinit.go— Cloud-init ISO generation, VM templates (3 tools)
├── hypervisor_validate.go — Input validators for hypervisor tools (7 validators)
└── *_test.go              — Tests (security, auth, backup, e2e, bench, concurrency, hypervisor)
```

**One file per feature domain.** Handlers go in `handlers_*.go`, logic in `feature.go`.
See [ARCHITECTURE.md](mcp-server-go/ARCHITECTURE.md) for the full code map.

## Adding a New MCP Tool

1. **Create the feature module** (`myfeature.go`) following the one-file-per-domain pattern:

```go
// Package main: <one-line description>
package main

type MyResult struct {
    ID   string `json:"id"`
    Name string `json:"name"`
}

var myMgr *MyManager

func newMyManager() *MyManager { ... }

func (m *MyManager) DoThing(name string) (*MyResult, error) { ... }
```

2. **Write the handler** in `handlers_basic.go` or `handlers_phase2.go`:

```go
func handleMyTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    args := parseArgs(req)
    name := argString(args, "name")
    if name == "" {
        return errResult("name is required"), nil
    }
    result, err := myMgr.DoThing(name)
    if err != nil {
        return unwrapError(err), nil
    }
    return okResult(result), nil
}
```

3. **Register it** in `tools_registration.go` → `registerAllTools()`:

```go
registerTool(s, toolWithArgs("my_tool", "Description here.",
    mcp.WithString("name", mcp.Required()),
), handleMyTool)
```

Use `tool()` for tools with no args, `toolWithArgs()` for tools with typed parameters.

4. **Add RBAC permission** in `auth.go` → `toolPermissions`:

```go
"my_tool": RoleOperator, // viewer, operator, or admin
```

5. **Add validators** in `security.go` if the tool accepts untrusted input (URLs, paths,
   container IDs, domains, commands). See the security checklist in
   [AGENT_GUIDE.md](mcp-server-go/AGENT_GUIDE.md).

6. **Write tests** in a `_test.go` file.

7. **Verify**:

```bash
go build ./... && go vet ./... && staticcheck ./... && go test -race -count=1 ./...
```

8. **Update the tool count** in `README.md` and the CI check in `.github/workflows/ci.yml`
   (the CI step `Verify tool count matches README` enforces this).

## RBAC Roles

| Role | Level | Can do |
|------|-------|--------|
| viewer | 1 | Read-only: list, get, health, logs, metrics |
| operator | 2 | Deploy, create, kill, pause, resume, exec, volumes, backups |
| admin | 3 | Everything: delete, rollback, routing, restore, node management, tokens |

## Testing

| Test type | Command | What it covers |
|-----------|---------|----------------|
| Unit | `go test -v ./...` | Security validators, auth, RBAC, backup, rate limit |
| E2E | `go test -tags=e2e -v ./...` | Full HTTP stack against mock CubeAPI |
| Concurrency | `go test -race -v ./...` | Race detector on all tests |
| Benchmarks | `go test -tags=e2e -bench=. -benchmem` | Performance metrics |
| Staticcheck | `staticcheck ./...` | Dead code, style, correctness (must be 0 findings) |

**CI gates**: build, `go vet`, `gosec`, `govulncheck`, staticcheck, and a tool-count
check (`grep -c 'registerTool(s,' tools_registration.go` must match README).

## Backend Auto-Detection

The binary auto-detects the backend at startup — no build tags needed:

- **Docker** (default for production): if `/var/run/docker.sock` exists, the Docker
  Engine API backend is used. Override with `CUBE_BACKEND=docker`.
- **Cube** (default for edge): if the Cube engine is detected, the CubeAPI backend is
  used. Override with `CUBE_BACKEND=cube`.

Both backends implement the same `ContainerBackend` interface, so all 161 tools work
on either. Some tools (secure sandbox, CubeCoW snapshots) require the Cube backend.
Hypervisor tools (VM, ZFS, GPU) require libvirt and/or ZFS installed on the host.

## Commit Messages

Follow conventional commits:

```
feat: add new MCP tool for X
fix: resolve race condition in audit logger
docs: update README with performance numbers
refactor: extract backend interface
test: add concurrency stress tests
security: patch SSRF in webhook validator
```

AI-assisted commits must include the attribution tag per [AGENTS.md](AGENTS.md):

```
# Human-assisted by AI
Assisted-by: AgentName:ModelVersion

# Fully autonomous AI work
Autonomously-by: AgentName:ModelVersion
```

AI agents MUST NOT add `Signed-off-by` tags (DCO certification is human-only).

## Code Style

- Match existing patterns — look at neighboring functions
- `gofmt` is mandatory: `gofmt -w *.go`
- `staticcheck` must be clean: `staticcheck ./...` (0 findings)
- No external dependencies unless absolutely necessary (stdlib + mcp-go only)
- Errors are returned, not panicked
- All public-facing strings should be descriptive (they reach the AI agent)
- Mutex-protect all shared state — use `sync.Mutex` or `sync.RWMutex`, never bare maps
- Persist stateful manager data to disk (survive restarts)

## Security Conventions

Before adding any tool that accepts user input, consult the security checklist in
[AGENT_GUIDE.md](mcp-server-go/AGENT_GUIDE.md). Key rules:

- **All user input** goes through a `validate*` function in `security.go`
- **All commands** pass through `validateCommand()` (allowlist + denylist)
- **URLs** are validated for SSRF (`isPrivateHost()` blocks internal IPs)
- **Timeouts** on `exec_in_container` are hard-capped at 300s (AS-2)
- **Inter-node Docker** connections should use `CUBE_DOCKER_TLS=true` in production (AS-4)
- **Audit trail** uses HMAC-SHA256 keyed with `CUBE_SECRETS_KEY` (AS-7)

56 security issues have been identified and fixed across 7 audit rounds. See
[README.md](README.md#security) for the full audit history.

## License

Apache 2.0 (inherited from upstream CubeSandbox). See [LICENSE](LICENSE).
