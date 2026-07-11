# Cube Container — Quick Start Guide

## What is Cube Container?

A container orchestration platform controlled by AI through the Model Context Protocol
(MCP). Instead of YAML files and CLI tools, you describe what you want in natural language
and the MCP server executes it.

**178 tools** covering: containers, images, deploy, scaling, health, networking,
secrets, backup, HA, environments, VMs (libvirt), ZFS storage, GPU passthrough,
and cloud-init provisioning.

---

## Prerequisites

- **OS**: Linux x86_64 (Ubuntu 22.04+ recommended)
- **Go**: 1.25.12+
- **Docker**: 24+ (or containerd + runc)
- **For hypervisor tools**: libvirt-daemon, qemu-system, ZFS utils, nvidia drivers

## Build from Source

```bash
git clone https://github.com/schwabauerbriantomas-gif/cube-container.git
cd cube-container

# Build MCP server
cd mcp-server-go
go build -o cube-mcp .
```

## First Run (stdio mode — for local AI agents)

```bash
# Set required environment variables
export CUBE_AUTH_KEYS_FILE=/etc/cube-container/auth-keys.json
export CUBE_SECRETS_KEY=$(openssl rand -hex 32)
export CUBE_AUDIT_LOG=/var/log/cube-container/audit.log
export CUBE_SECRETS_PASSPHRASE="your-secret-passphrase"

# Generate your first API key
./cube-mcp --mode keygen --role admin

# Output:
#   API Key:    cc_live_xxxxxxxxxxxxxxxx
#   API Secret: sec_xxxxxxxxxxxxxxxxxxxxxxxx
#   SAVE THESE — the secret is never shown again

# Start in stdio mode (for Claude, GPT, or any MCP-compatible agent)
./cube-mcp --mode stdio
```

## HTTP Mode (for remote agents + web UI)

```bash
# Start HTTP server on port 8080
./cube-mcp --mode http --port 8080

# Health check
curl http://localhost:8080/health
```

## Docker (container mode — all-in-one)

```bash
docker build -f deploy/container-mode/Dockerfile -t cube-container:beta .

docker run -d --name cube-node \
  --privileged \
  -v /var/lib/containerd:/var/lib/containerd \
  -v /var/lib/cube-container:/var/lib/cube-container \
  -v /var/run/docker.sock:/var/run/docker.sock \
  -p 80:80 -p 443:443 \
  -e ACME_EMAIL=you@example.com \
  -e MCP_DOMAIN=mcp.yourdomain.com \
  cube-container:beta
```

Services started: containerd → CubeMaster → Cubelet → CubeAPI → MCP HTTP → Caddy (TLS)

---

## Connecting an AI Agent

### Claude Desktop / Code

Add to your MCP config:

```json
{
  "mcpServers": {
    "cube-container": {
      "command": "/path/to/cube-mcp",
      "args": ["--mode", "stdio"],
      "env": {
        "CUBE_API_KEY": "cc_live_xxx",
        "CUBE_API_SECRET": "sec_xxx",
        "CUBE_API_URL": "http://localhost:8080"
      }
    }
  }
}
```

### Any MCP-compatible client

Headers required:

```
X-API-Key: cc_live_xxx
X-API-Secret: sec_xxx
```

Or Bearer token:

```
Authorization: Bearer cc_live_xxx:sec_xxx
```

---

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `CUBE_BACKEND` | `auto` | Backend: `docker`, `cube`, or `auto` |
| `CUBE_AUTH_KEYS_FILE` | `./auth-keys.json` | Path to API key store |
| `CUBE_SECRETS_KEY` | *(required)* | HMAC key for audit chain (32 hex bytes) |
| `CUBE_SECRETS_PASSPHRASE` | *(required)* | Argon2id passphrase for AES-256 secrets |
| `CUBE_AUDIT_LOG` | `./audit.log` | Tamper-evident audit log path |
| `CUBE_AUDIT_DIR` | *(same as log)* | Audit log directory |
| `CUBE_ALERT_ROOT` | `./alerts` | Alert rules storage |
| `CUBE_BACKUP_ROOT` | `./backups` | Backup storage |
| `CUBE_CADDY_RELOAD` | `caddy reload` | Command to reload Caddy config |
| `CUBE_TLS_CERT` | `` | Custom TLS cert path |

---

## Security Controls

### Authentication

- API keys: `cc_live_<32 hex>` + secret `sec_<48 hex>`
- Secrets hashed with SHA-256 (never stored in plaintext)
- Constant-time comparison (anti-timing attack)
- Dummy HMAC on auth failure (equalizes response timing)

### RBAC — 3 roles

| Role | Permissions |
|---|---|
| `viewer` | Read-only: list, inspect, logs, metrics. No mutations. |
| `operator` | Deploy, scale, restart, backup. No key management or security config. |
| `admin` | Full access including key management, secrets, hypervisor, GPU. |

### Rate Limiting

| Limit | Value |
|---|---|
| Per API key | 60 requests/minute |
| Per IP address | 600 requests/minute |
| HA heartbeat | 1/second per peer |
| Connection limit | 100 concurrent per IP |

### Audit Trail

- Every tool call logged with: timestamp, key ID, tool name, arguments, result
- Tamper-evident HMAC-SHA256 hash chain (each entry links to previous)
- Verify integrity: `./cube-mcp --mode verify-audit`

### Secrets Management

- AES-256-GCM encryption at rest
- Argon2id key derivation (memory-hard: 64MB, 3 iterations, 4 threads)
- GPU/ASIC resistant per OWASP 2023 recommendations

### Network Security (HTTP mode)

- Caddy reverse proxy: automatic TLS via Let's Encrypt
- WAF rules: SQL injection, XSS, path traversal, command injection patterns
- Security headers: HSTS, CSP, X-Frame-Options, X-Content-Type-Options, Referrer-Policy
- MCP HTTP port (8080) is internal-only — not exposed publicly

---

## Hypervisor Tools (optional)

The 32 hypervisor tools require additional host setup:

```bash
# Install dependencies
apt install -y libvirt-daemon-system qemu-system-x86 zfsutils-linux cloud-image-utils

# For GPU passthrough
apt install -y nvidia-driver-595
# Enable IOMMU in kernel: intel_iommu=on or amd_iommu=on
```

| Category | Tools | Count |
|---|---|---|
| VM lifecycle | create, start, stop, pause, resume, delete, snapshot, migrate | 13 |
| ZFS storage | pool create/destroy, dataset, snapshot, clone, rollback | 12 |
| GPU | detect, stats, assign (VFIO), release | 4 |
| Cloud-init | generate user-data, create ISO, list templates | 3 |

---

## Verification

```bash
# Check tool count
./cube-mcp --mode http --port 8080 &
curl -H "X-API-Key: $KEY" -H "X-API-Secret: $SECRET" \
  http://localhost:8080/mcp -d '{"tool":"backend_info"}'

# Run tests
cd mcp-server-go && go test -v -race ./...

# Verify audit chain integrity
./cube-mcp --mode verify-audit
```

---

## Troubleshooting

| Problem | Solution |
|---|---|
| `connection refused :8080` | MCP server not running — check `./cube-mcp --mode http` |
| `401 Unauthorized` | Wrong API key/secret — regenerate with `--mode keygen` |
| `403 Forbidden` | RBAC — key role too low. Use `admin` for full access |
| `429 Too Many Requests` | Rate limit hit — wait 60s or create additional keys |
| Docker socket error | Ensure `/var/run/docker.sock` is mounted/accessible |
| GPU not detected | Check `nvidia-smi` works on host, driver version ≥ 535 |
| ZFS errors | Ensure `zfsutils-linux` installed and kernel module loaded |

---

*Cube Container v0.10.0-beta — see RELEASE_NOTES.md for changelog*
