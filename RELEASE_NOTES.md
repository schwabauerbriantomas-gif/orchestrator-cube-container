# Cube Container v0.10.0-beta — Release Notes

**Release date:** July 11, 2026
**License:** Apache 2.0
**Tag:** `v0.10.0-beta`

---

## ⚠️ Beta Notice

This is a **public beta**. It is feature-complete and security-audited (11 rounds, 131 findings,
111 resolved, 20 deferred with documented justification). Not yet deployed in production at scale.

- Breaking changes possible before v1.0
- API surface may change (tool names, argument schemas)
- No backward compatibility guarantee until v1.0

---

## What's New in v0.10.0-beta

### Security Hardening (R10–R11)

- **Response body limits**: All `io.ReadAll` calls on backend HTTP responses (Docker, CubeAPI, Proxmox) now use `limitedReadAll` with a 100MB cap — prevents memory exhaustion from compromised/malicious backend services
- **Logstream XSS prevention**: `validateContainerID` now enforced on SSE log streaming endpoint (`logstream.go`)
- **SSRF via Docker API paths**: New `dockerAPIPathRe` regex validator rejects malformed Docker API paths
- **TOCTOU race conditions**: `filepath.EvalSymlinks` added before file reads in `images.go` and `backup.go`
- **Path traversal in deploy**: `filepath.Clean` + error checking on `MkdirAll` in `deploy.go` and `certificates.go`
- **SSH hostname injection**: `validateHostname` added before SSH/SCP operations in `volumes.go`
- **gosec configuration**: `.gosec.toml` with documented exclusions for known false positives (gosec v2 dev bug workaround)

### Architecture Cleanup

- **Removed MicroVM guest agent** (`agent/`, -35,504 lines Rust): Not used in container-mode, eliminated 2 Dependabot low-severity alerts (`tracing-subscriber`, `atty`)
- **Removed upstream SDKs** (`sdk/go`, `sdk/python`, -12,304 lines): REST-based, not MCP-compatible
- **Removed Tencent Chinese docs** (`docs/zh/`, 17 `*_zh.md`, -44,014 lines): Inherited from upstream, not applicable
- **Removed network-agent**: Not started in container-mode runtime
- **Removed mkcert ELF binaries** (4.6MB): Replaced with CI binary guard
- **Enhanced `.gitignore`**: Added patterns for compiled binaries to prevent accidental commits

### Divergence from Upstream

Formal architectural divergence declared from `github.com/TencentCloud/CubeSandbox` (fork point `d5ac863`, v0.5.1-rc3):

- **61+ commits** of container-mode-specific work
- **178 MCP tools** replacing the REST API surface
- Container-mode (runc/overlayfs) instead of MicroVM (KVM/rustvmm)
- MCP JSON-RPC over stdio/HTTP as the sole operations interface

---

## What's Included

### Core MCP Server (178 tools)

| Category | Tools | Description |
|---|---|---|
| Container orchestration | 42 | CRUD, exec, logs (SSE), templates, scaling, health probes |
| Deployment | 18 | Git deploy, rolling/blue-green updates, rollback, webhooks |
| Networking | 13 | Port maps, DNS, policies, routing (Caddy), TLS certs |
| Storage & backup | 12 | Volumes, migration, backup/restore, ZFS management |
| Security | 12 | AES-256-GCM secrets, audit chain, certificates |
| Multi-node & HA | 7 | Node management, active-passive failover, scheduler |
| Observability | 11 | Metrics, log search, event stream, alerting |
| Environments & jobs | 8 | Namespace isolation, scheduled tasks |
| Infrastructure | 8 | Databases, notifications, configmaps |
| **Hypervisor layer** | **32** | **VM lifecycle (libvirt), ZFS, GPU passthrough, cloud-init** |
| **TOTP 2FA** | **4** | **RFC 6238, Steam Guard style** |
| **Proxmox VE backend** | **13** | **VMs, snapshots, storage, nodes, migration via REST API** |

### Security (11 audit rounds, 131 findings, 111 fixed, 20 deferred)

| Round | Scope | Findings | Status |
|---|---|---|---|
| R1–R4 | Core MCP server (auth, path traversal, injection, RBAC) | 40 | ✅ All resolved |
| R5 | Attack surface (exec, sandbox, transport, webhook, HA) | 7 | ✅ All resolved |
| R7 | Hypervisor layer — 2 CRITICAL shell injections | 9 | ✅ All resolved |
| R8 | Pre-beta (CI/CD, deploy, web, deps) | 14 | ✅ All resolved |
| R9-Deploy | Deployment hardening (USER, digests, privileged, gosec SARIF) | 14 | ✅ All resolved |
| R9-Auth | Auth/crypto (heartbeat replay, webhook, HMAC, dead code) | 14 | 5 fixed, 9 deferred |
| R9-Hyp | Hypervisor (temp files, ZFS validation, VNC, cloud-init) | 10 | 5 fixed, 5 deferred |
| R9-MCP | MCP protocol (SSRF, metrics injection, secret leaks) | 12 | 6 fixed, 6 deferred |
| R10-Sec | Security audit (logstream XSS, SSRF, TOCTOU, path traversal) | 9 | ✅ All resolved |
| R11-Sec | Response body limits, cleanup | 2 | ✅ All resolved |

**Key security features:**

- TOTP 2FA (RFC 6238, HMAC-SHA1) — 12 destructive ops require TOTP
- Argon2id key derivation (OWASP 2023)
- AES-256-GCM secrets encryption
- HMAC-SHA256 tamper-evident audit chain
- RBAC with 3 roles (viewer / operator / admin), fail-closed
- Dual rate limiting (per-key 60/min + per-IP 600/min)
- SSRF prevention on health probes and all URL-accepting tools
- Response body limits (100MB) on all backend HTTP calls
- gosec SARIF enforcement in CI (0 HIGH/CRITICAL)
- govulncheck: 0 vulnerabilities

### TOTP 2FA

RFC 6238 TOTP with HMAC-SHA1 — compatible with Google/Microsoft Authenticator.

- `totp_enroll` — generate secret + `otpauth://` URL for QR enrollment
- `totp_confirm` — verify first code to activate 2FA
- `totp_disable` — disable TOTP (requires valid code)
- `totp_status` — check enrollment status

### Proxmox VE Backend

REST API client for Proxmox VE. API token auth, TLS on by default, RBAC viewer/operator/admin.
Auto-initializes when `CUBE_PROXMOX_HOST` env var is set.

13 tools: `pve_list_vms`, `pve_get_vm`, `pve_create_vm`, `pve_start_vm`, `pve_stop_vm`,
`pve_delete_vm`, `pve_migrate_vm`, `pve_list_snapshots`, `pve_create_snapshot`,
`pve_delete_snapshot`, `pve_list_storage`, `pve_list_nodes`, `pve_list_lxc`.

### Deployment

- **Container mode**: Single Docker image (all components)
- **Caddy**: TLS (Let's Encrypt auto), WAF, rate limiting, security headers, /metrics IP allowlist
- **Config**: TOML-based, env-var overrides
- **Docker image**: Multi-stage build (Go + Caddy)

---

## Tested Configurations

| Environment | Status |
|---|---|
| WSL2 (QEMU emulation, no KVM) | ✅ VM lifecycle, cloud-init validated |
| NVIDIA RTX 3090 (driver 595.79) | ✅ GPU detect + stats validated |
| Docker Engine 24+ | ✅ Full tool suite validated |
| ZFS (without kernel module) | ✅ Graceful degradation validated |
| Go 1.25.12 + mcp-go v0.56.0 | ✅ CI 6/6 green |

---

## Known Limitations

1. **Single-node only**: HA manager exists but clustering is not yet implemented
2. **No GPU assignment in Docker mode**: VFIO passthrough requires bare-metal
3. **Web UI**: Read-only visualization; all control via MCP
4. **20 deferred findings**: All architectural or LOW risk, documented in audit reports

---

## Contributors

- Brian Tomas Schwabauer — project lead, architecture, all code
- Hermes Agent (glm-5.2) — AI-assisted development, security audits, test coverage

---

## Links

- **Repo**: [github.com/schwabauerbriantomas-gif/orchestrator-cube-container](https://github.com/schwabauerbriantomas-gif/orchestrator-cube-container)
- **Release**: [v0.10.0-beta](https://github.com/schwabauerbriantomas-gif/orchestrator-cube-container/releases/tag/v0.10.0-beta)
- **Docs**: See `QUICKSTART.md`, `mcp-server-go/ARCHITECTURE.md`, `CONTRIBUTING.md`
- **Audit reports**: `docs/audits/`
- **Issues**: Use GitHub Issues for bug reports and feature requests
