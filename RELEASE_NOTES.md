# Cube Container v0.9.0-beta — Release Notes

**Release date:** July 9, 2026
**Codename:** "Sentinel"
**License:** Apache 2.0

---

## ⚠️ Beta Notice

This is a **public beta**. It is feature-complete and security-audited (8 rounds, 68 findings
resolved), but has not yet been deployed in production at scale. Expect:

- Breaking changes before v1.0
- API surface may change (tool names, argument schemas)
- No backward compatibility guarantee until v1.0

**Do NOT use for production workloads without your own testing.**

---

## What's Included

### Core MCP Server (161 tools)

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

### Security (8 audit rounds, 68 findings resolved)

| Round | Scope | Findings | Status |
|---|---|---|---|
| 1-2 | Core MCP server | 15 | ✅ All resolved |
| 3-4 | Attack surfaces (SSRF, path traversal, command injection) | 14 | ✅ All resolved |
| 5 | Sentinel consensus review | 11 | ✅ All resolved |
| 6 | Apex forensics deep dive | 7 | ✅ All resolved |
| 7 | Hypervisor layer (29 tools) | 9 (2 CRITICAL shell injections) | ✅ All resolved |
| 8 | Pre-beta (CI/CD, deploy, web, deps) | 12 | ✅ All resolved |

**Key security features:**

- Argon2id key derivation (OWASP 2023 recommended)
- AES-256-GCM secrets encryption
- HMAC-SHA256 tamper-evident audit chain
- RBAC with 3 roles (viewer / operator / admin)
- Dual rate limiting (per-key 60/min + per-IP 600/min)
- IPv6-safe IP extraction (fixed in R8)
- Content-Security-Policy headers via Caddy
- Input validation on every tool (command allowlist, path traversal, SSRF, XML/YAML injection)
- Resource limits on VM creation (max 64 vCPU, 256GB RAM, 8TB disk)
- Supply-chain hardening: SHA-pinned CI actions, checksum-verified Caddy binary, Dependabot

### Web Dashboard

React 18 + Vite + Tailwind CSS. 17 pages:

Overview, Sandboxes (list/detail/new), Nodes (list/detail), Templates,
Template Store, Agent Hub, Keys, Network, Observability, Settings, Login, Versions.

### Deployment

- **Container mode**: Single Docker image (all components)
- **Caddy**: TLS (Let's Encrypt auto), WAF, rate limiting, security headers
- **Config**: TOML-based, env-var overrides
- **Docker image**: ~400MB (Ubuntu 22.04 base + Go + Rust binaries)

---

## What's NOT in Beta

| Feature | Status |
|---|---|
| TOTP / 2FA (Steam Guard style) | Planned for v0.10 |
| Clustering (Raft consensus) | Planned for v1.0 |
| Proxmox backend integration | Planned for v1.0 |
| OpenAPI spec for hypervisor tools | Planned for v0.10 |
| Web UI pages for VM/GPU/ZFS | Planned for v0.10 |
| Custom appliance ISO | Planned for v1.0 |

---

## Tested Configurations

| Environment | Status |
|---|---|
| WSL2 (QEMU emulation, no KVM) | ✅ VM lifecycle, cloud-init validated |
| NVIDIA RTX 3090 (driver 595.79) | ✅ GPU detect + stats validated |
| Docker Engine 24+ | ✅ Full tool suite validated |
| ZFS (without kernel module) | ✅ Graceful degradation validated |

---

## Known Limitations

1. **Single-node only**: HA manager exists but clustering is not yet implemented
2. **No GPU assignment in Docker mode**: VFIO passthrough requires bare-metal
3. **Legacy Tencent pVM**: Present in repo but not used by container-mode
4. **Web UI**: Read-only visualization; all control via MCP
5. **i18n**: EN + ZH available; other locales need community contributions

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
- **Docs**: See `QUICKSTART.md`, `mcp-server-go/ARCHITECTURE.md`, `CONTRIBUTING.md`
- **Audit reports**: `docs/audits/`
- **Issues**: Use GitHub Issues for bug reports and feature requests
