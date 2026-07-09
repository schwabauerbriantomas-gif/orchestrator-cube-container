# Contributing to Cube Container

Cube Container is a container-mode fork of [CubeSandbox](https://github.com/TencentCloud/CubeSandbox), focused on MCP-native container orchestration for edge nodes.

## Quick Start

```bash
# Clone
git clone https://github.com/schwabauerbriantomas-gif/cube-container.git
cd cube-container/mcp-server-go

# Build
go build -o cube-mcp .

# Run tests
go test -v ./...

# Run e2e + benchmarks
go test -tags=e2e -v ./...

# Run with race detector
go test -race -v ./...
```

## Architecture

```
MCP Server (Go, single binary)
├── server.go          — tool registration + core handlers (129 tools)
├── handlers_phase2.go — Phase 2+ tool handlers (deploy, scaling, etc.)
├── handlers_secure.go — Secure sandbox tool handlers
├── client.go          — CubeAPI HTTP client (Cube backend)
├── docker_client.go   — Docker Engine backend (auto-detected, no build tag needed)
├── auth.go            — API keys (hashed at rest), RBAC, rate limiting, audit hash chain
├── secrets.go         — AES-256-GCM encrypted secrets (argon2id key derivation)
├── security.go        — Input validation (command allowlist, SSRF, path traversal)
├── deploy.go          — GitOps deploy, volume management
├── backup.go          — Backup/restore with SHA256 integrity
├── scheduler.go       — Bin-packing node scheduling assistant
├── routing.go         — Caddy TLS automation
├── logstream.go       — SSE log streaming
├── metrics.go         — Prometheus endpoint
├── rollback.go        — Deploy versioning
├── webhook.go         — GitOps push webhooks (URL validated pre-deploy)
├── secure_sandbox.go  — KVM sandbox for untrusted code
└── ha.go              — Active-passive high availability
```

## Adding a New MCP Tool

1. **Define the handler** in the appropriate `.go` file:

```go
func handleMyTool(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
    args := parseArgs(req)
    name := argString(args, "name")
    if name == "" {
        return errResult("name is required"), nil
    }
    // ... do work ...
    return okResult(data), nil
}
```

2. **Register it** in `server.go` → `registerAllTools`:

```go
s.AddTool(toolWithArgs("my_tool", "Description here.",
    mcp.WithString("name", mcp.Required()),
), handleMyTool)
```

3. **Add RBAC permission** in `auth.go` → `toolPermissions`:

```go
"my_tool": RoleOperator, // viewer, operator, or admin
```

4. **Write tests** in a `_test.go` file.

5. **Verify**: `go build . && go test -v ./...`

## RBAC Roles

| Role | Level | Can do |
|------|-------|--------|
| viewer | 1 | Read-only: list, get, health, logs, metrics |
| operator | 2 | Deploy, create, kill, pause, resume, exec, volumes, backups |
| admin | 3 | Everything: delete, rollback, routing, restore |

## Testing

| Test type | Command | What it covers |
|-----------|---------|----------------|
| Unit | `go test -v ./...` | Security, auth, RBAC, backup, rate limit |
| E2E | `go test -tags=e2e -v ./...` | Full HTTP stack against mock CubeAPI |
| Concurrency | `go test -race -v ./...` | Race detector on all tests |
| Benchmarks | `go test -tags=e2e -bench=. -benchmem` | Performance metrics |

## Commit Messages

Follow conventional commits:

```
feat: add new MCP tool for X
fix: resolve race condition in audit logger
docs: update README with performance numbers
refactor: extract backend interface
test: add concurrency stress tests
```

AI-assisted commits must include:
```
Assisted-by: AgentName:ModelVersion
```

## Code Style

- Match existing patterns — look at neighboring functions
- `gofmt` is mandatory: `gofmt -w *.go`
- No external dependencies unless absolutely necessary (we're stdlib-first)
- Errors are returned, not panicked
- All public-facing strings should be descriptive (they reach the AI agent)

## Docker Backend (Optional)

The Docker backend is behind a build tag:

```bash
# Build with Docker support
go build -tags docker -o cube-mcp .

# The binary auto-detects: if /var/run/docker.sock exists, use Docker;
# otherwise fall back to CubeAPI
```

## License

Apache 2.0 (inherited from upstream CubeSandbox).
