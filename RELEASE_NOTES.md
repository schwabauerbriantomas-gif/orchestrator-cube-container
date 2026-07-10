# Cube Container v0.9.0-beta — Release Notes

**Release date:** July 10, 2026
**Codename:** "Sentinel"
**License:** Apache 2.0
**Tag:** `v0.9.0-beta` (commit `23e9ee9`)

---

## ⚠️ Beta Notice

This is a **public beta**. It is feature-complete and security-audited (9 rounds, 120 findings,
100 resolved, 20 deferred with documented justification), but has not yet been deployed in
production at scale. Expect:

- Breaking changes before v1.0
- API surface may change (tool names, argument schemas)
- No backward compatibility guarantee until v1.0

**Do NOT use for production workloads without your own testing.**

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
| **TOTP 2FA** | **4** | **RFC 6238, Steam Guard style, Google/Microsoft Authenticator** |
| **Proxmox VE backend** | **13** | **VMs, snapshots, storage, nodes, migration via REST API** |

### Security (9 audit rounds, 120 findings, 100 fixed, 20 deferred)

| Round | Scope | Findings | Status |
|---|---|---|---|
| R1-R4 | Core MCP server (auth, path traversal, injection, RBAC) | 40 | ✅ All resolved |
| R5 | Attack surface (exec, sandbox, transport, webhook, HA) | 7 | ✅ All resolved |
| R7 | Hypervisor layer (29 tools) — 2 CRITICAL shell injections | 9 | ✅ All resolved |
| R8 | Pre-beta (CI/CD, deploy, web, deps) | 14 | ✅ All resolved |
| R9-Deploy | Deployment hardening (USER, digests, privileged, gosec SARIF) | 14 | ✅ All resolved |
| R9-Auth | Auth/crypto (heartbeat replay, webhook, HMAC, dead code) | 14 | 5 fixed, 9 deferred |
| R9-Hyp | Hypervisor (temp files, ZFS validation, VNC, cloud-init) | 10 | 5 fixed, 5 deferred |
| R9-MCP | MCP protocol (SSRF, metrics injection, secret leaks) | 12 | 6 fixed, 6 deferred |

**Key security features:**

- TOTP 2FA (RFC 6238, HMAC-SHA1) — Steam Guard style, 12 destructive ops require TOTP
- Argon2id key derivation (OWASP 2023 recommended)
- AES-256-GCM secrets encryption
- HMAC-SHA256 tamper-evident audit chain
- RBAC with 3 roles (viewer / operator / admin), fail-closed
- Dual rate limiting (per-key 60/min + per-IP 600/min)
- IPv6-safe IP extraction (fixed in R8)
- Content-Security-Policy headers via Caddy
- Input validation on every tool (command allowlist, path traversal, SSRF, XML/YAML injection)
- SSRF prevention on health probes (`isPrivateHost()` blocks RFC 1918, loopback, link-local, cloud metadata)
- HA heartbeat replay protection (timestamp validation + monotonic counter)
- Resource limits on VM creation (max 64 vCPU, 256GB RAM, 8TB disk)
- Supply-chain hardening: SHA-pinned CI actions, checksum-verified Caddy binary, Dependabot
- gosec SARIF enforcement (CI fails on new HIGH/CRITICAL, 14 exclusiones documentadas)
- govulncheck: 0 vulnerabilities

### TOTP 2FA (NEW in v0.9.0-beta)

RFC 6238 TOTP with HMAC-SHA1 — compatible with Google/Microsoft Authenticator.

- `totp_enroll` — generate secret + `otpauth://` URL for QR enrollment
- `totp_confirm` — verify first code to activate 2FA
- `totp_disable` — disable TOTP (requires valid code)
- `totp_status` — check enrollment status

**Security model:** Human admins use password + TOTP. Automated agents/CI use API keys
without TOTP but with restricted scope. 12 destructive operations require `X-TOTP` header
if the key has TOTP enrolled.

### Proxmox VE Backend (NEW in v0.9.0-beta)

REST API client for Proxmox VE. API token auth, TLS on by default, RBAC viewer/operator/admin.
Auto-initializes when `CUBE_PROXMOX_HOST` env var is set.

13 tools: `pve_list_vms`, `pve_get_vm`, `pve_create_vm`, `pve_start_vm`, `pve_stop_vm`,
`pve_delete_vm`, `pve_migrate_vm`, `pve_list_snapshots`, `pve_create_snapshot`,
`pve_delete_snapshot`, `pve_list_storage`, `pve_list_nodes`, `pve_list_lxc`.

### Web Dashboard

React 18 + Vite + Tailwind CSS. 17 pages:

Overview, Sandboxes (list/detail/new), Nodes (list/detail), Templates,
Template Store, Agent Hub, Keys, Network, Observability, Settings, Login, Versions.

### Deployment

- **Container mode**: Single Docker image (all components)
- **Caddy**: TLS (Let's Encrypt auto), WAF, rate limiting, security headers, /metrics IP allowlist
- **Config**: TOML-based, env-var overrides
- **Docker image**: ~400MB (Ubuntu 22.04 base + Go + Rust binaries)

---

## What's NOT in Beta

| Feature | Status |
|---|---|
| Clustering (Raft consensus) | Planned for v1.0 |
| OpenAPI spec for hypervisor/TOTP/Proxmox tools | Planned for v0.10 |
| Web UI pages for VM/GPU/ZFS/TOTP/Proxmox | Planned for v0.10 |
| Custom appliance ISO | Planned for v1.0 |
| Error message sanitization (R9-MCP-01) | Post-beta |
| Migrate gosec global exclusions to `#nosec` (R9-AUTH-08) | Post-beta |

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
3. **Legacy Tencent pVM**: Present in repo but not used by container-mode
4. **Web UI**: Read-only visualization; all control via MCP
5. **i18n**: EN + ZH available; other locales need community contributions
6. **20 deferred findings**: All architectural or LOW risk, documented in commit history

---

## Upgrade Path

This is the first public release. No upgrade path needed.

---

## Contributors

- Brian Tomas Schwabauer — project lead, architecture, all code
- Hermes Agent (glm-5.2) — AI-assisted development, security audits, test coverage

---

## Links

- **Repo**: [github.com/schwabauerbriantomas-gif/cube-container](https://github.com/schwabauerbriantomas-gif/cube-container)
- **Release**: [v0.9.0-beta](https://github.com/schwabauerbriantomas-gif/cube-container/releases/tag/v0.9.0-beta)
- **Docs**: See `QUICKSTART.md`, `mcp-server-go/ARCHITECTURE.md`, `CONTRIBUTING.md`
- **Audit reports**: `docs/audits/`
- **Issues**: Use GitHub Issues for bug reports and feature requests
