# Agent Guide — How to Work on Cube Container

> **For AI agents**: Read this before writing or modifying any code.
> It contains the conventions, patterns, and rules that keep this project
> maintainable. Violating these will produce code that doesn't fit.

## Golden Rules

1. **One file per feature domain** — `images.go`, `health.go`, `jobs.go`, etc. Don't mix features.
2. **Handlers in `handlers_basic.go` or `handlers_phase2.go`**. Feature files contain logic only.
3. **ALL user input goes through `security.go` validators** — no exceptions.
4. **RBAC for every tool** — if a tool exists, it MUST have an entry in `toolPermissions`.
5. **Register the tool in `tools_registration.go`** using `registerTool(s, tool(...), handleX)`.
6. **Mutex-protect all shared state** — use `sync.Mutex` or `sync.RWMutex`, never bare maps.
7. **Persist state to disk** — managers that hold state must survive restarts.
8. **No external dependencies** unless absolutely necessary — Go stdlib + mcp-go only.
9. **Comments in English** — but the code speaks for itself. Document WHY, not WHAT.

## File Conventions

### Naming
- Feature files: `featurename.go` (e.g., `images.go`, `health.go`)
- Test files: `feature_test.go` (e.g., `security_test.go`)
- Handler files: `handlers_phaseN.go` (e.g., `handlers_phase2.go`)
- Infrastructure: lowercase, no underscores (`docker_client.go`, `backend.go`)

### Structure within a feature file

```go
// Package main: <one-line description of what this file does>
//
// <paragraph explaining the feature, why it exists, and how it fits>
package main

import (...)

// ---- Types ----
type MyResult struct { ... }

// ---- Validation ----
func validateMyInput(input string) error { ... }

// ---- Manager ----
var myMgr *MyManager

type MyManager struct {
    mu      sync.Mutex
    rootDir string
    // state fields
}

func newMyManager() *MyManager { ... }

// ---- Disk persistence ----
func (m *MyManager) loadFromDisk() { ... }
func (m *MyManager) saveToDisk() error { ... }

// ---- Operations ----
func (m *MyManager) DoThing(args ...) (*MyResult, error) { ... }
```

### Function ordering within a file
1. Package comment + imports
2. Types
3. Validators
4. Manager struct + constructor
5. Disk persistence
6. Business logic (operations)
7. Helpers (at the bottom)

## Adding a New Tool — Checklist

```
[ ] 1. Created feature file (or added to existing one)
[ ] 2. Defined types for the response
[ ] 3. Added validators in security.go (if untrusted input)
[ ] 4. Implemented manager method
[ ] 5. Registered tool in registerAllTools() (tools_registration.go)
[ ] 6. Written handler in handlers_phase2.go
[ ] 7. Added RBAC entry in toolPermissions (auth.go)
[ ] 8. go build ./... passes
[ ] 9. go test -race -count=1 ./... passes
[ ] 10. Updated README.md tool count if needed
```

## Common Mistakes to Avoid

### ❌ Accessing manager internals directly
```go
// BAD
keyStore.mu.RLock()
for _, k := range keyStore.keys { ... }

// GOOD
keys := keyStore.List()
for _, k := range keys { ... }
```

### ❌ Using user input without validation
```go
// BAD
exec("docker update --memory " + userMemory + " " + containerID)

// GOOD
if err := validateContainerID(containerID); err != nil {
    return nil, err
}
```

### ❌ Bare map without mutex
```go
// BAD — race condition
var cache = make(map[string]string)
cache["key"] = "value" // from goroutine A
v := cache["key"]      // from goroutine B

// GOOD
type SafeCache struct {
    mu sync.RWMutex
    m  map[string]string
}
```

### ❌ Ignoring disk persistence
```go
// BAD — state lost on restart
func newManager() *Manager {
    return &Manager{rules: make(map[string]*Rule)}
}

// GOOD — loads from disk
func newManager() *Manager {
    m := &Manager{rules: make(map[string]*Rule), rootDir: envOr(...)}
    m.loadFromDisk()
    return m
}
```

### ❌ External dependencies for simple things
```go
// BAD — adds a dependency for something stdlib does
import "github.com/some-lib/strings"
result := someLib.ReplaceAll(s, old, new)

// GOOD — use stdlib
result := strings.ReplaceAll(s, old, new)
```

## Security Checklist

Before adding any tool that accepts user input:

- [ ] Does it accept a URL? → `validateWebhookURL()` or `validateGitURL()`
- [ ] Does it accept a path? → `validatePathSafe()` or `validateMountPath()`
- [ ] Does it accept a hostname/IP? → `isPrivateHost()` (blocks SSRF)
- [ ] Does it accept a container ID? → `validateContainerID()`
- [ ] Does it accept an image ref? → `validateImageRef()`
- [ ] Does it accept a domain? → `validateDomain()`
- [ ] Does it accept a Telegram token? → `validateTelegramToken()`
- [ ] Does it accept a domain for egress? → `validateDomainRule()`
- [ ] Does it accept an egress rule ID? → `validateRuleID()`
- [ ] Does it send API keys to a remote host? → Verify HTTPS first (C8 pattern)
- [ ] Does it concat user input into a URL path? → Validate format first (H12 pattern)
- [ ] Does it pass args to a shell command? → `validateCommand()` (allowlist)
- [ ] Does it accept a timeout? → Cap it (300s max, 1s min) — see AS-2

### Security Design Decisions (from Round 5 Attack Surface Audit)

These are intentional design choices, not oversights. Understanding them prevents regressions:

1. **Secure sandbox exec (AS-1)**: `secure_sandbox_exec` deliberately skips `validateCommand()`. The security boundary is KVM hardware isolation, not command filtering. Adding an allowlist here would break the sandbox's purpose (running arbitrary code). The denylist in `security.go` is defense-in-depth only.

2. **`sh -c` in Docker exec (AS-3)**: `exec_in_container` wraps commands in `sh -c` for compatibility (pipes, redirects, env vars). This allows bypassing the allowlist via chaining. The expanded denylist catches common exfiltration patterns but is NOT a security boundary. For untrusted code, always use `secure_sandbox_exec`.

3. **Inter-node TLS opt-in (AS-4)**: Remote Docker connections default to plaintext. This is a pragmatic choice for homelab/dev environments. The code prints a stderr warning when plaintext is used. Set `CUBE_DOCKER_TLS=true` in production.

4. **Webhook header-only auth (AS-5)**: Webhook secrets are accepted ONLY via the `X-Git-Token` header, never via query params. Query params leak into access logs, Referer headers, and browser history. Do NOT re-add a `?token=` fallback.

5. **HMAC audit chain (AS-7)**: `computeAuditHash` uses HMAC-SHA256 keyed with `CUBE_SECRETS_KEY`. Without the key, an attacker who modifies the audit log cannot recompute valid hashes. The fallback to plain SHA-256 (when no key is configured) exists for backward compatibility only — always set `CUBE_SECRETS_KEY` in production.

## Commit Message Format

```
<type>: <description>

<optional body explaining what and why>

Autonomously-by: Hermes:<model>
```

Types: `feat`, `fix`, `security`, `docs`, `refactor`, `test`, `chore`

## AI Agent Policy

When working on this project as an AI agent:
- Read `ARCHITECTURE.md` for the code map
- Read this file (`AGENT_GUIDE.md`) for conventions
- Read `skills/` for workflow patterns
- Follow the checklist when adding tools
- Run `go build` and `go test -race` before committing
- Include `Autonomously-by:` tag in commits per `AGENTS.md`
