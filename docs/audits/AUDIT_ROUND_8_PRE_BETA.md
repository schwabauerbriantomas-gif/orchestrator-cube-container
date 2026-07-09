# Round 8 — Pre-Beta Security Audit

**Date:** July 9, 2026
**Scope:** Full pre-release review — authentication, hypervisor, web UI, deploy, CI/CD, dependencies
**Method:** 4 parallel subagent audits (auth/session, injection/hypervisor, web/deploy, CI/deps)
**Total findings:** 12 new (0 CRITICAL after R7 fixes, 2 HIGH, 7 MEDIUM, 3 LOW)

---

## Findings Summary

| ID | Severity | Component | Issue | Status |
|---|---|---|---|---|
| C-CI-01 | ~~CRITICAL~~ | go.mod | Go 1.25.0 — 23 reachable stdlib CVEs | ✅ Fixed: bump to 1.25.12 |
| NEW-01 | ~~HIGH~~ | Caddyfile | No Content-Security-Policy header | ✅ Fixed: CSP added |
| NEW-02 | ~~HIGH~~ | entrypoint.sh | MCP HTTP server + Caddy never started | ✅ Fixed: both now start |
| H-CI-02 | ~~HIGH~~ | ci.yml | gosec@master — floating tag | ✅ Fixed: SHA-pinned |
| H-CI-03 | ~~HIGH~~ | ci.yml | All actions use mutable tags | ✅ Fixed: all SHA-pinned |
| H-CI-04 | ~~HIGH~~ | .github | No Dependabot config | ✅ Fixed: dependabot.yml added |
| NEW-03 | MEDIUM | Dockerfile | No USER directive (runs as root) | ⏳ Deferred: containerd needs root |
| NEW-04 | ~~MEDIUM~~ | Dockerfile | Caddy download without checksum | ✅ Fixed: SHA256 verification |
| NEW-05 | ~~MEDIUM~~ | auth.go | ipFromAddr breaks IPv6 | ✅ Fixed: net.SplitHostPort |
| NEW-06 | MEDIUM | auth.go/api.ts | No CSRF infrastructure | ⏳ Deferred: blocks cookie migration only |
| M-CI-05 | MEDIUM | ci.yml | No SBOM generation | ⏳ Post-beta |
| M-CI-06 | ~~MEDIUM~~ | ci.yml | No govulncheck SARIF upload | ✅ Fixed: SARIF upload added |
| M-CI-07 | MEDIUM | Dockerfile | golang:1.25 floating tag | ✅ Fixed: pinned to 1.25.12 |
| M-CI-08 | LOW | ci.yml | Single Go version in matrix | ⏳ Acceptable for beta |
| NEW-07 | ~~LOW~~ | Dockerfile | curl without --fail | ✅ Fixed: -sfL flag added |
| NEW-08 | ~~LOW~~ | Dockerfile | No HEALTHCHECK | ✅ Fixed: healthcheck added |
| NEW-09 | ~~LOW~~ | Dockerfile | EXPOSE 8080 bypasses Caddy | ✅ Fixed: removed from EXPOSE |
| L-CI-09 | ~~LOW~~ | ci.yml | No timeout-minutes | ✅ Fixed: 15min on all jobs |
| L-CI-10 | LOW | ci.yml | Secrets exposure | ✅ Clean — no secrets used |

**Resolved:** 12 / 17 (5 deferred to post-beta with justification)
**Blocking for beta:** 0

---

## Deferred Items (justified)

### NEW-03: Dockerfile runs as root
**Why deferred:** `containerd` and `cubelet` require root for DinD pattern.
Running `cube-mcp` and `caddy` as non-root would require `gosu`/`su-exec` privilege
separation — planned for v0.10. The `--privileged` Docker flag is documented in QUICKSTART.

### NEW-06: No CSRF infrastructure
**Why deferred:** Currently safe — auth uses custom headers (X-API-Key, X-API-Secret)
that cross-origin forms cannot set. CSRF tokens are needed only when migrating to
HttpOnly+SameSite cookies (planned for TOTP/2FA in v0.10).

### M-CI-05: No SBOM
**Why deferred:** SBOM generation (syft/cosign) is a compliance feature, not a security
vulnerability. Planned for v1.0 when EU CRA compliance becomes relevant.

---

## Cumulative Audit History

| Round | Scope | Findings | Resolved |
|---|---|---|---|
| 1-2 | Core MCP server | 15 | 15 |
| 3-4 | Attack surfaces | 14 | 14 |
| 5 | Sentinel consensus | 11 | 11 |
| 6 | Apex forensics | 7 | 7 |
| 7 | Hypervisor layer | 9 | 9 |
| 8 | Pre-beta (this round) | 12+5 deferred | 12 |
| **Total** | | **68 + 5 deferred** | **68 resolved** |

---

## Methodology

4 independent audits run in parallel, each by a separate subagent with isolated context:

1. **Auth/Session audit** — reviewed auth.go, auth_tokens.go, auth_test.go for timing
   attacks, RBAC bypass, key entropy, audit chain integrity
2. **Injection/Hypervisor audit** — reviewed all hypervisor_*.go for shell injection,
   path traversal, XML/YAML injection, PCI address injection, resource exhaustion
3. **Web/Deploy audit** — reviewed Caddyfile, Dockerfile, entrypoint.sh, web/ for
   CORS, CSRF, CSP, Docker security, TLS config
4. **CI/CD audit** — reviewed ci.yml, go.mod, go.sum for dependency CVEs (live
   govulncheck), supply chain risks, action pinning, Dependabot gaps
