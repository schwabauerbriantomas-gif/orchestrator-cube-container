# Cube Container

[![Fork of TencentCloud/CubeSandbox](https://img.shields.io/badge/fork%20of-CubeSandbox-blue)](https://github.com/TencentCloud/CubeSandbox)
[![License: Apache 2.0](https://img.shields.io/badge/License-Apache_2.0-green)](LICENSE)
[![MCP Server](https://img.shields.io/badge/MCP-89%20tools-orange)](https://modelcontextprotocol.io)
[![Min RAM: 4GB](https://img.shields.io/badge/Min%20RAM-4GB-success)]()
[![Go Version](https://img.shields.io/badge/Go-1.24%2B-00ADD8)]()
[![CI](https://img.shields.io/badge/CI-7%20jobs-blue)]()

**Container-mode fork of [CubeSandbox](https://github.com/TencentCloud/CubeSandbox)** — the same control plane, E2B-compatible API, and MCP orchestration, but running on **native Linux containers (containerd + runc + overlayfs)** instead of KVM MicroVMs.

Designed for **low-resource edge nodes** (4 GB RAM, 4 cores, no KVM) where hardware virtualization isn't available or is overkill.

---

## Why This Fork?

CubeSandbox is an excellent AI-agent sandbox runtime (8.7K+ stars on GitHub), but it requires:

- **KVM support** (`/dev/kvm`)
- **8+ GB RAM** per node
- **XFS with reflink** for Copy-on-Write storage
- A full hypervisor stack (rustvmm / Cloud Hypervisor)

**Cube Container** strips out the entire VM layer — `hypervisor/`, `cubecow/`, `CubeNet/`, `CubeShim/` — and runs everything as native containers. Same control plane, same API surface, same lifecycle management, but **10× lighter and 6× faster cold starts**.

### Target hardware

| Spec | CubeSandbox (upstream) | **Cube Container** |
|------|----------------------|---------------------|
| Min RAM per node | 8 GB | **4 GB** |
| Virtualization required | KVM (`/dev/kvm`) | **None** |
| CPU overhead per workload | ~50 MB (VM kernel + VMM) | **~2–5 MB** (process) |
| Cold start | ~60 ms (boot VM kernel) | **~5–10 ms** (clone + namespaces) |
| Auto-resume (pause/thaw) | ~100 ms (VM restore) | **~15 ms** (cgroup freezer) |

Runs on COTS mini-PCs, ARM SBCs, recycled office machines, Proxmox VMs, or any Linux host with containerd.

---

## What Changed

| Component | CubeSandbox (original) | Cube Container (this fork) |
|-----------|----------------------|---------------------------|
| **Isolation** | KVM MicroVM (hardware-level) | runc containers (namespace-level) |
| **Hypervisor** | `hypervisor/` (rustvmm, 30+ modules) | ❌ Removed — not needed |
| **Storage** | `cubecow/` (XFS reflink CoW) | `overlayfs` (containerd native) |
| **Networking** | `CubeNet/` (eBPF + vsock) | CNI bridge (standard) |
| **Runtime Shim** | `CubeShim/` (containerd → VM bridge) | ❌ Removed — runc is direct |
| **Storage plugin** | CubeCow + XFS only | Accepts `overlayfs` backend |

### What Stayed the Same

- ✅ **CubeAPI** (Rust) — E2B-compatible REST API on `:3000`
- ✅ **CubeMaster** (Go) — multi-node scheduler
- ✅ **Cubelet** (Go) — per-node lifecycle management
- ✅ **cube-lifecycle-manager** — auto-pause/resume logic
- ✅ **CubeProxy** (nginx) — reverse proxy + TLS
- ✅ **CubeEgress** (OpenResty) — egress control
- ✅ **network-agent** (Go) — network management
- ✅ **Web Console** (`:12088`)

### What Was Added

- ✅ **MCP Server** (Go) — **89 tools** for AI-agent-driven orchestration, single static binary
- ✅ **Dual Backend** — auto-detects Docker (production) or Cube (edge 4GB) at runtime
- ✅ **Auth + RBAC** (Go) — API-key + secret auth, RBAC (viewer/operator/admin), rate limiting, JSONL audit trail with SHA256 hash chain
- ✅ **HA Active-Passive** — heartbeat-based failover with HMAC auth + priority-based split-brain resolution
- ✅ **Encrypted Secrets** — AES-256-GCM at rest, 3 key sources (hex key, passphrase, auto-generated)
- ✅ **Zero-Config TLS** — Caddy route generation with automatic Let's Encrypt certificates
- ✅ **Networking** — port mappings, DNS aliases, network policies (iptables-backed)
- ✅ **Backup & Restore** — tar.gz with SHA256 integrity, container manifests, point-in-time staging
- ✅ **Rollback** — deployment version history with git-based rollback
- ✅ **GitOps Webhook** — auto-deploy on push (GitHub, Gitea, GitLab, Gogs)
- ✅ **Metrics** — Prometheus `/metrics` endpoint with live cluster state
- ✅ **Log Streaming** — SSE endpoint for real-time container log tailing
- ✅ **4D Scheduling** — bin-packing node suggestions based on CPU, RAM, disk, network
- ✅ **Health Checks + Auto-Restart** — HTTP/TCP/exec probes, automatic container restart on consecutive failures
- ✅ **Multi-Node Cluster** — node registry, remote deploy, SSRF-protected inter-node communication
- ✅ **Horizontal Scaling** — replica groups with automatic Caddy load balancing (round-robin + health checks)
- ✅ **Volume Lifecycle** — attach/detach/migrate volumes, cross-node migration via tar+scp
- ✅ **Service Discovery** — logical name → endpoint registry with auto-sync from container labels
- ✅ **Resource Limits** — hard cgroup limits (memory/CPU) via `docker update`, real-time usage stats
- ✅ **Garbage Collection** — automatic image pruning, disk usage monitoring, auto-prune at threshold
- ✅ **Alerting** — rule-based monitoring (container_down, CPU/disk/mem) with webhook notifications
- ✅ **ConfigMaps** — non-sensitive configuration data (env vars, feature flags)
- ✅ **Security Hardened** — 26 attack surfaces audited and closed (allowlist exec, SSRF prevention, argument injection prevention, timing-attack resistant auth, per-IP connection limits, path traversal prevention, mount path validation)

---

## Architecture

### Dual-mode operation

```
 ┌─────────────────────────────────────────────────────────────┐
 │                      LOCAL (trusted)                         │
 │                                                              │
 │  AI Agent ──stdio──▶ MCP Server ──▶ ContainerBackend         │
 │  (Hermes,           (Go, 89 tools)  ├── Docker (unix sock)  │
 │   Claude,                            └── Cube (HTTP :3000)   │
 │   Cursor)                                   │                │
 │                                              ▼                │
 │                              CubeMaster (Go)                  │
 │                              ├── Node 1 (Cubelet + runc)     │
 │                              ├── Node 2 (Cubelet + runc)     │
 │                              └── Node N (Cubelet + runc)     │
 └─────────────────────────────────────────────────────────────┘

 ┌─────────────────────────────────────────────────────────────┐
 │                   REMOTE (untrusted / production)            │
 │                                                              │
 │  External            Caddy :443 (or native TLS)              │
 │  Client ──HTTPS──▶  ├── TLS 1.3 / Let's Encrypt auto         │
 │  (API key)          ├── WAF (SQLi, XSS, path traversal)     │
 │                     ├── Rate limiting                        │
 │                     └── Dynamic route import                 │
 │                              │                               │
 │                              ▼                               │
 │                       MCP HTTP :8080                          │
 │                       ├── API-key + secret (HMAC compare)    │
 │                       ├── RBAC (viewer/operator/admin)       │
 │                       ├── Rate limiting (120 req/min/key)    │
 │                       ├── Audit trail (JSONL SHA256 chain)   │
 │                       ├── Body size limit (10 MB)            │
 │                       ├── Per-IP conn limit (64)             │
 │                       └── Secrets redaction in audit         │
 │                              │                               │
 │                              ▼                               │
 │                       ContainerBackend                        │
 └─────────────────────────────────────────────────────────────┘
```

### Background watchers

The MCP server runs several goroutine watchers in HTTP mode:

| Watcher | Interval | Purpose |
|---------|----------|---------|
| Health watcher | 5s | Runs probes, restarts failed containers |
| Alert watcher | 30s | Evaluates monitoring rules, fires webhooks |
| GC watcher | 1h | Checks disk usage, auto-prunes images if > 85% |
| HA heartbeat | 2s | Active-passive failover coordination |

### Backend auto-detection

```
1. CUBE_BACKEND=docker  → force Docker
2. CUBE_BACKEND=cube    → force Cube
3. /var/run/docker.sock responds → Docker (lower latency)
4. fallback             → Cube (lighter, for edge 4GB)
```

### Security layer separation

| Layer | Where | Applies to |
|-------|-------|------------|
| Input validation (path traversal, git sanitization, **command allowlist**, SSRF prevention, **argument injection prevention**, **mount path validation**) | `security.go` | **Both** modes |
| TLS 1.3 + WAF + rate limiting | `Caddyfile` — Caddy proxy | HTTP mode only |
| API-key auth + RBAC + audit | `auth.go` | HTTP mode only |
| Encrypted secrets (AES-256-GCM) | `secrets.go` | Both modes |
| HA heartbeat auth (HMAC-SHA256) | `ha.go` | HTTP mode only |

---

## MCP Tools (89 total)

Any MCP-compatible AI agent (Claude, Cursor, Hermes, OpenAI agents, local LLMs) can manage the entire cluster through natural language.

### Cluster Management (6)

| Tool | Description |
|------|-------------|
| `cluster_health` | Check CubeAPI reachability |
| `cluster_overview` | Node count, running containers, resource capacity |
| `cluster_versions` | Component version matrix |
| `list_nodes` | All nodes with CPU/RAM/disk info |
| `get_node` | Detailed node info by ID |
| `suggest_node` | 4D bin-packing: best node for new container |

### Container Lifecycle (8)

| Tool | Description |
|------|-------------|
| `list_containers` | Running / paused / stopped containers |
| `get_container` | Container details by ID |
| `create_container` | Deploy from template with CPU/RAM/env config |
| `kill_container` | Stop and remove |
| `pause_container` | Freeze (cgroup freezer, ~0 CPU) |
| `resume_container` | Thaw a paused container |
| `get_container_logs` | Fetch stdout/stderr logs |
| `tail_container_logs` | Last N log lines |

### Templates (3)

| Tool | Description |
|------|-------------|
| `list_templates` | Available container templates |
| `create_template` | Create from any OCI image |
| `get_template` | Template details |

### Persistent Deployment (4)

| Tool | Description |
|------|-------------|
| `deploy_from_git` | Clone repo, build, deploy with volumes |
| `deploy_from_code` | Deploy from inline files |
| `update_code` | Pull latest from git and redeploy |
| `exec_in_container` | Run command inside container (allowlist-enforced) |

### Volumes (7)

| Tool | Description |
|------|-------------|
| `list_volumes` | Persistent volumes across the cluster |
| `create_volume` | Create a named volume |
| `delete_volume` | Remove a volume |
| `volume_attach` | Attach volume to a running container at a mount path |
| `volume_detach` | Detach volume from a container (data preserved) |
| `volume_migrate` | Migrate volume to a remote node via tar+scp |
| `volume_info` | Detailed info: size, file count, attached containers |

### Backup & Restore (5)

| Tool | Description |
|------|-------------|
| `backup_volume` | tar.gz with SHA256 integrity, point-in-time staging |
| `backup_container` | Full snapshot: config manifest + all volumes |
| `list_backups` | All backups with size, checksum, status |
| `restore_backup` | Restore with SHA256 verification |
| `delete_backup` | Remove a backup permanently |

### Deployment Versioning (2)

| Tool | Description |
|------|-------------|
| `rollback_deploy` | Rollback to previous deployment version |
| `list_deploy_versions` | List all deployment versions for an app |

### Routing & TLS (4)

| Tool | Description |
|------|-------------|
| `create_route` | Domain → container reverse proxy with auto TLS |
| `delete_route` | Remove a domain route |
| `list_routes` | All configured domain routes |
| `reload_routes` | Force regenerate Caddy config and reload |

### Networking (9)

| Tool | Description |
|------|-------------|
| `add_port_mapping` | Map host port to container port |
| `remove_port_mapping` | Remove a port mapping |
| `list_port_mappings` | All port mappings |
| `add_dns_alias` | Add DNS alias to /etc/hosts (IP-validated) |
| `remove_dns_alias` | Remove a DNS alias |
| `list_dns_aliases` | All DNS aliases |
| `add_network_policy` | Allow/deny firewall rule between containers |
| `list_network_policies` | All network policies |
| `remove_network_policy` | Remove a network policy |

### Multi-Node Cluster (6)

| Tool | RBAC | Description |
|------|------|-------------|
| `node_add` | admin | Register a new cluster node |
| `node_update` | admin | Update node properties (address, state, resources) |
| `node_remove` | admin | Remove a node from the registry |
| `node_list` | viewer | List all cluster nodes |
| `node_get` | viewer | Detailed node information |
| `deploy_to_node` | operator | Deploy a container to a specific remote node |

### Horizontal Scaling (5)

| Tool | RBAC | Description |
|------|------|-------------|
| `service_create` | admin | Define a scalable service (replica group) |
| `scale_set` | operator | Set exact replica count (creates/removes containers) |
| `service_list` | viewer | List all scalable services |
| `service_get` | viewer | Service details with container IDs |
| `service_remove` | admin | Remove a service definition |

### Service Discovery (4)

| Tool | RBAC | Description |
|------|------|-------------|
| `service_register` | operator | Register logical name → endpoint |
| `service_deregister` | operator | Remove a service entry |
| `service_resolve` | viewer | Look up a service by name → host:port |
| `service_entries` | viewer | List all discovery entries (sync from container labels) |

### Health Checks (4)

| Tool | RBAC | Description |
|------|------|-------------|
| `health_check_set` | operator | Configure HTTP/TCP/exec probe for a container |
| `health_check_remove` | operator | Remove a health check |
| `health_check_list` | viewer | All checks with status and restart counts |
| `health_check_status` | viewer | Detailed health status for one container |

### Resource Limits (4)

| Tool | RBAC | Description |
|------|------|-------------|
| `resource_set_limits` | operator | Apply hard memory/CPU limits via docker update |
| `resource_get_usage` | viewer | Real-time CPU/memory/IO for one container |
| `resource_list_usage` | viewer | Usage for all running containers |
| `resource_quota_summary` | viewer | Allocated vs node capacity |

### Garbage Collection (3)

| Tool | RBAC | Description |
|------|------|-------------|
| `gc_prune_images` | operator | Remove unused images older than 7 days |
| `gc_prune_volumes` | operator | Remove orphaned volumes (manual, not auto) |
| `gc_disk_usage` | viewer | Disk breakdown: images, containers, volumes |

### Alerting (4)

| Tool | RBAC | Description |
|------|------|-------------|
| `alert_rule_add` | admin | Create monitoring rule (container_down, CPU/disk/mem) |
| `alert_rule_remove` | admin | Remove an alert rule |
| `alert_list` | viewer | All rules with fire count and last fired |
| `alert_test` | operator | Fire a test alert to verify webhook |

### ConfigMaps (5)

| Tool | RBAC | Description |
|------|------|-------------|
| `configmap_create` | admin | Create non-sensitive config data (env vars, flags) |
| `configmap_update` | admin | Update/merge config data |
| `configmap_get` | viewer | Get config data values |
| `configmap_list` | viewer | List all ConfigMaps |
| `configmap_remove` | admin | Remove a ConfigMap |

### Secrets Management (4)

| Tool | RBAC | Description |
|------|------|-------------|
| `secret_set` | admin | Store encrypted secret (AES-256-GCM) |
| `secret_get` | operator | Decrypt and retrieve a secret |
| `secret_list` | viewer | List secret names + metadata (no values) |
| `secret_delete` | admin | Permanently delete a secret |

### HA + Backend (2)

| Tool | Description |
|------|-------------|
| `ha_state` | Current HA state: role, active node, peer health |
| `backend_info` | Active backend, endpoint, capabilities |

---

## Quick Start

### Build the MCP server

```bash
cd mcp-server-go
CGO_ENABLED=0 go build -o cube-mcp .
# Single ~8MB static binary
```

### Run in stdio mode (local)

```bash
./cube-mcp
```

### Configure your MCP client

```json
{
  "mcpServers": {
    "cube-container": {
      "command": "cube-mcp",
      "env": {
        "CUBE_API_URL": "http://localhost:3000",
        "CUBE_API_KEY": "e2b_000000"
      }
    }
  }
}
```

### Run in HTTP mode (production)

```bash
# Generate an admin API key
./cube-mcp --gen-key admin --label "production-admin"

# Start the server
./cube-mcp --mode http --port 8080

# With native TLS
CUBE_TLS_CERT=/path/to/cert.pem \
CUBE_TLS_KEY=/path/to/key.pem \
./cube-mcp --mode http --port 8443
```

---

## Configuration

All configuration is via environment variables. No config files needed.

### Core

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_BACKEND` | auto | `docker`, `cube`, or `auto` |
| `CUBE_API_URL` | `http://localhost:3000` | CubeAPI URL |
| `CUBE_API_KEY` | `e2b_000000` | CubeAPI key |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker socket path |

### Security

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_AUTH_KEYS_FILE` | `/var/lib/cube-container/auth-keys.json` | API key store |
| `CUBE_AUDIT_LOG` | `/var/lib/cube-container/audit.logl` | Audit log |
| `CUBE_EXEC_ALLOWLIST` | *(built-in)* | Extra allowed exec commands |
| `CUBE_ALLOW_INSECURE_GIT` | `false` | Allow http:// and git:// |
| `CUBE_TLS_CERT` | *(empty)* | TLS cert for native HTTPS |
| `CUBE_TLS_KEY` | *(empty)* | TLS key for native HTTPS |

### Secrets

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_SECRETS_KEY` | *(empty)* | Hex-encoded 32-byte AES key |
| `CUBE_SECRETS_PASSPHRASE` | *(empty)* | Derive key via PBKDF2 |
| `CUBE_SECRETS_FILE` | `/var/lib/cube-container/secrets.json` | Encrypted store |
| `CUBE_SECRETS_KEY_FILE` | `/var/lib/cube-container/keys/secrets.key` | Auto-gen key |
| `CUBE_SECRETS_SALT_FILE` | `/var/lib/cube-container/keys/secrets.salt` | Auto-gen salt |

### High Availability

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_HA_PEERS` | *(empty)* | Comma-separated peer addresses |
| `CUBE_HA_SELF_ID` | hostname | This node's unique ID |
| `CUBE_HA_PRIORITY` | `100` | Lower wins split-brain |
| `CUBE_HA_SECRET` | *(empty)* | HMAC-SHA256 heartbeat secret |

### Routing & TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_CADDY_CONFIG_PATH` | `/etc/caddy/cube-routes.caddy` | Route fragment |
| `CUBE_CADDY_RELOAD` | `false` | Auto-reload Caddy after route changes |
| `CUBE_ROUTES_ROOT` | `/var/lib/cube-container/routes` | Route store |

### Multi-Node + Inter-Node TLS

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_CUBE_TLS` | `false` | Enable HTTPS for remote CubeAPI nodes |
| `CUBE_DOCKER_TLS` | `false` | Enable TLS for remote Docker TCP connections |

### Garbage Collection

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_GC_ENABLED` | `true` | Enable background GC watcher |
| `CUBE_GC_THRESHOLD` | `85` | Disk usage % that triggers auto-prune |
| `CUBE_GC_MIN_AGE_HOURS` | `168` | Min image age before pruning (7 days) |

### Alerting

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_ALERT_WEBHOOK` | *(empty)* | Global webhook URL for alert notifications |

### GitOps

| Variable | Default | Description |
|----------|---------|-------------|
| `CUBE_WEBHOOK_ENABLED` | `false` | Enable git webhook listener |
| `CUBE_WEBHOOK_SECRET` | *(empty)* | Webhook auth secret |

---

## Security Model

### Security Trade-off

| | CubeSandbox (KVM) | Cube Container (runc) |
|---|---|---|
| Isolation strength | Hardware (dedicated kernel) | Namespace (shared kernel) |
| Container escape risk | Near zero | Low but nonzero |
| Best for | Untrusted code execution | **Trusted workloads and services** |

**Cube Container is designed for hosting your own services** — not for running untrusted user-submitted code. For untrusted workloads, use upstream CubeSandbox with KVM.

### Security Audit

The MCP server underwent two full attack-surface audits. **26 issues identified, all 26 resolved:**

| Severity | Count | Status |
|----------|-------|--------|
| 🔴 Critical | 6 | ✅ All fixed |
| 🟠 High | 8 | ✅ All fixed |
| 🟡 Medium | 7 | ✅ All fixed |
| 🟢 Low | 5 | ✅ All fixed |

Key hardening measures:

| Control | Implementation |
|---------|----------------|
| Transport encryption | TLS 1.3 via Caddy or native TLS |
| Authentication | API key + secret, HMAC constant-time compare |
| Authorization | RBAC: viewer / operator / admin |
| Rate limiting | 120 req/min per API key |
| **Exec allowlist** | Commands validated against allowlist (not bypassable blacklist) |
| **SSRF prevention** | Git URLs, node addresses, webhook URLs, health probes — all block private IPs and cloud metadata |
| **Argument injection prevention** | Container IDs validated before passing to Docker CLI |
| **Path traversal prevention** | Mount paths validated against sensitive system directories |
| **Git injection** | Branch names validated, `--` separator |
| **Config injection** | Domain/path inputs sanitized |
| Body limit | 10 MB max request body |
| Conn limit | 64 simultaneous connections per IP |
| Secrets encryption | AES-256-GCM at rest |
| Audit logging | JSONL SHA256 hash chain |
| HA heartbeat auth | HMAC-SHA256, priority-based split-brain |
| **Volume prune safety** | Auto-prune never deletes volumes (manual only) |
| **SSH host key checking** | known_hosts file prevents MITM after first connection |

---

## Performance

| Metric | Value | Notes |
|--------|-------|-------|
| **Binary size** | ~8 MB | Static binary, stripped |
| **RSS memory (idle)** | 8.3 MB | HTTP mode, after startup |
| **Startup + init** | < 1 ms | Process spawn → MCP initialize |
| **HTTP latency (avg)** | 482 µs | Full stack: auth → RBAC → backend |
| **Throughput** | 2,076 RPS | Single core |

```bash
cd mcp-server-go
go test -tags=e2e -bench=. -benchmem ./...
```

---

## Project Structure

```
cube-container/
├── CubeAPI/                   # Rust — E2B-compatible REST API (:3000)
├── CubeMaster/                # Go — multi-node scheduler
├── Cubelet/                   # Go — per-node lifecycle
├── mcp-server-go/             # Go — MCP server (89 tools, single binary)
│   ├── server.go              # MCP server — stdio + HTTP, 89 tool handlers
│   ├── client.go              # CubeAPI HTTP client (Cube backend)
│   ├── docker_client.go       # Docker Engine API client (Docker backend)
│   ├── backend.go             # ContainerBackend interface + auto-detection
│   ├── deploy.go              # Persistent deploy from git/code
│   ├── security.go            # Input validation (10+ validators)
│   ├── auth.go                # Auth, RBAC, rate limiting, audit, conn limits
│   ├── secrets.go             # AES-256-GCM encrypted secrets
│   ├── ha.go                  # Active-passive HA with HMAC heartbeats
│   ├── backup.go              # Backup & restore with SHA256
│   ├── rollback.go            # Deployment version history & rollback
│   ├── routing.go             # Caddy route management + auto TLS
│   ├── networking.go          # Port mappings, DNS aliases, policies
│   ├── health.go              # Health probes + auto-restart watcher
│   ├── nodes.go               # Multi-node registry + remote deploy
│   ├── scaling.go             # Horizontal scaling + load balancing
│   ├── volumes.go             # Volume attach/detach/migrate
│   ├── discovery.go           # Service discovery registry
│   ├── resources.go           # Resource limits + usage monitoring
│   ├── gc.go                  # Image/volume garbage collection
│   ├── alerting.go            # Alert rules + webhook notifications
│   ├── configmaps.go          # Non-sensitive config management
│   ├── webhook.go             # GitOps webhook listener
│   ├── metrics.go             # Prometheus /metrics
│   ├── scheduler.go           # 4D bin-packing
│   ├── logstream.go           # SSE log streaming
│   └── *_test.go              # 43 tests (security, auth, concurrency, e2e)
├── deploy/
│   └── container-mode/        # Dockerfile, Caddyfile, config
└── sdk/                       # Python + Go SDKs
```

---

## CI

7 jobs on GitHub Actions:

| Job | Purpose |
|-----|---------|
| MCP Server (Go 1.24 + 1.25) | Build, vet, tests with race detector |
| Go Components | Cubelet + CubeMaster builds |
| Docker Image Build | Full multi-stage Dockerfile |
| Security Scan | Gosec SAST + Govulncheck |
| Binary Guard | Detects committed ELF/PE binaries |

---

## Roadmap

- [x] ~~Multi-node scheduling~~ → `suggest_node` 4D bin-packing + `deploy_to_node`
- [x] ~~Health checks~~ → HTTP/TCP/exec probes with auto-restart
- [x] ~~Horizontal scaling~~ → `scale_set` with Caddy load balancing
- [x] ~~Volume lifecycle~~ → attach/detach/migrate
- [x] ~~Service discovery~~ → name → endpoint registry
- [x] ~~Resource limits~~ → docker update + usage monitoring
- [x] ~~Garbage collection~~ → auto image prune, disk monitoring
- [x] ~~Alerting~~ → rule-based monitoring with webhooks
- [x] ~~ConfigMaps~~ → non-sensitive config management
- [x] ~~Security hardening~~ → 26/26 attack surfaces closed
- [ ] Billing / metering
- [ ] OIDC / OAuth2 authentication
- [ ] Multi-region federation

---

## License

Apache 2.0 (inherited from upstream [CubeSandbox](https://github.com/TencentCloud/CubeSandbox) / Tencent Cloud).

---

## Credits

- **[TencentCloud/CubeSandbox](https://github.com/TencentCloud/CubeSandbox)** — original project, 8.7K+ stars.
- Built on [containerd](https://containerd.io/), [runc](https://github.com/opencontainers/runc), [Caddy](https://caddyserver.com/), [Docker Engine API](https://docs.docker.com/engine/api/), and the [MCP protocol](https://modelcontextprotocol.io/).
