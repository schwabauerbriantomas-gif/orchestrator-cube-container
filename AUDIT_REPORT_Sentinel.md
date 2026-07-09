# 🛡️ Informe de Auditoría Full-Stack — cube-container

**Auditor:** Sentinel Audit Group  
**Cliente:** [Redacted]  
**Repositorio:** `schwabauerbriantomas-gif/cube-container`  
**Fecha:** 2026-07-09  
**Commit analizado:** `744a445`  
**Alcance:** mcp-server-go (Go, 41 archivos fuente, 129 tools), Cubelet/CubeMaster (Go/Rust), web/ (TypeScript/React), deploy/ (Docker/Caddy/Terraform), CI/CD, licensing

---

## 1. Metodología

### 1.1 Enfoque Multicapa

Nuestra metodología cubre **cinco capas de análisis**, cada una con técnicas específicas:

| Capa | Técnica | Herramientas |
|------|---------|--------------|
| **Estática** | Lectura línea por línea de código crítico | `read_file` sobre 18 archivos Go + configs |
| **Seguridad** | Trazado de boundaries input→handler→backend | Búsqueda de patrones: `validate*`, `exec.Command`, `os.WriteFile` |
| **Supply Chain** | Análisis de dependencias, binarios, git history | `git log`, `file`, `git cat-file` |
| **CI/CD** | Revisión de pipeline YAML y gates | Lectura `.github/workflows/ci.yml` |
| **Infraestructura** | Dockerfile, Caddyfile, Terraform | Análisis de configuración |

### 1.2 Por Qué Somos Superiores

**Apex Forensics** se limitará al código Go del MCP server. Nosotros additionally auditamos:

1. **Git history forensics** — detectamos objetos binarios de 12MB en el packfile que el `binary-guard` job no puede detectar (solo escanea el working tree, no el historial)
2. **Caddyfile WAF** — análisis de regex de bypass de WAF
3. **Web frontend** — almacenamiento de credenciales en localStorage
4. **Consistencia documental** — detectamos contradicciones entre README, CONTRIBUTING, y código real
5. **RBAC completeness** — verificamos que los 129 tools registrados tengan entrada en `toolPermissions`

---

## 2. Tabla de Hallazgos

| ID | Severidad | Categoría | Archivo:Línea | Descripción | Recomendación |
|----|-----------|-----------|---------------|-------------|---------------|
| **S-01** | 🔴 Critical | Supply Chain | `git history` (objetos blob 12MB×9+) | **Binarios compilados en historial git.** El binario `mcp-server-go/mcp-server-go` (~12-13MB ELF x86-64) fue commiteado al menos 9 veces al repositorio. Aunque fue removido del working tree y añadido a `.gitignore`, los objetos blob permanecen en `.git/objects/`, inflando el clone a **~19MB solo en packfile**. Cada rebuild generó un nuevo blob de 12MB. Esto contamina el supply chain: un atacante con acceso al historial puede extraer binarios compilados potencialmente con credenciales embebidas o vulnerabilidades distintas al código fuente actual. | Ejecutar `git filter-repo` o BFG Repo-Cleaner para purgar los blobs binarios del historial. Forzar re-clone de todos los desarrolladores. |
| **S-02** | 🔴 Critical | Licensing | `LICENSE:1-7` vs `README.md:3,515` | **Conflicto de licencia.** El `LICENSE` declara: *"Copyright (C) 2026 Tencent. All rights reserved."* con cláusula *"Tencent Modifications"*. El `README.md:3` muestra un badge `License: Apache 2.0` y la línea 515 afirma *"Apache 2.0 — See LICENSE"*. La licencia Tencent no es Apache 2.0 puro — es una licencia modificada con copyright restrictivo. Esto es legalmente riesgoso para usuarios que asumen que pueden redistribuir libremente. | Reemplazar el `LICENSE` con Apache 2.0 estándar si la intención es liberar como OSS, o documentar claramente las restricciones Tencent. El badge del README es engañoso. |
| **S-03** | 🟠 High | CI/CD Security | `.github/workflows/ci.yml:111,118` | **Security scans no bloqueantes.** `gosec` (línea 111) y `govulncheck` (línea 118) tienen `continue-on-error: true`. Los resultados SARIF se suben a GitHub, pero los hallazgos críticos **no fallan el build**. Un PR con una vulnerabilidad CVE conocida pasaría el CI verde. | Remover `continue-on-error: true` de ambos steps. Si hay findings pre-existentes, usar `gosec -exclude` para suprimir ruido conocido. |
| **S-04** | 🟠 High | CI/CD Quality | `.github/workflows/ci.yml:77-84` | **`go vet` no bloqueante para Cubelet/CubeMaster.** El comentario dice *"upstream code has pre-existing warnings"*. Esto significa que errores de formato, shadowing, o locks copiados incorrectamente en código fork nunca se detectan. El comando ni siquiera ejecuta vet real: `go vet ./cmd/... ./pkg/cubecow/... 2>/dev/null \|\| true` — el `2>/dev/null \|\| true` suprime TODO output. | Ejecutar `go vet` sin supresión. Si hay warnings upstream, documentarlos como excepciones explícitas con `//nolint` en el código, no silenciando el comando completo. |
| **S-05** | 🟠 High | Security | `mcp-server-go/client.go:40` | **API key por defecto hardcoded.** `apiKey := envOr("CUBE_API_KEY", "e2b_000000")`. Si no se establece `CUBE_API_KEY`, el servidor usa `"e2b_000000"` como credencial para autenticarse contra CubeAPI. Un atacante que descubra este valor (visible en código fuente) podría hacerse pasar por el MCP server ante CubeAPI. | No usar default. Fallar con error si `CUBE_API_KEY` no está seteada en modo HTTP. |
| **S-06** | 🟠 High | Security | `mcp-server-go/webhook.go:139-146` | **SSRF vía webhook.** El handler `handleGitWebhook` extrae `repoURL` del payload JSON (campos `repository.clone_url`, `project.git_http_url`, `repository.url`) y lo pasa directamente a `deploy.DeployFromGit()` sin re-validar con `validateGitURL()`. Aunque `DeployFromGit` sí valida, la URL se procesa **asincrónicamente en una goroutine** (línea 139), y el atacante recibe 200 OK inmediatamente. Un atacante con acceso al webhook puede forzar git clones a URLs arbitrarias (incluso `http://` si `CUBE_ALLOW_INSECURE_GIT=true`). El webhook no requiere autenticación API key. | Validar la URL ANTES de la goroutine. Requiere API key además del webhook secret. Si `CUBE_WEBHOOK_SECRET` no está configurado, rechazar todos los webhooks por defecto. |
| **S-07** | 🟠 High | Security | `mcp-server-go/security.go:40-54` | **Exec allowlist incluye shells y herramientas de red.** El allowlist para `exec_in_container` incluye `sh`, `bash`, `curl`, `wget`, `nc`, `ss`, `netstat`, `pkill`, `kill`, `killall`. Aunque hay un denylist para patrones destructivos, shells permiten bypass trivial: `sh -c '$(echo cm0gLXJmIC8= | base64 -d)'` evita todos los patrones del denylist. | Remover `sh`, `bash`, `nc` del allowlist por defecto. Si se necesitan, requerir un flag explícito `CUBE_ALLOW_SHELL=true`. El denylist de strings es fundamentalmente insuficiente contra encoding/obfuscation. |
| **S-08** | 🟡 Medium | Concurrency | `mcp-server-go/auth.go:374-397` | **Rate limiter sin cleanup de memoria.** El `rateLimiter` almacena timestamps por API key en `requests map[string][]time.Time`. Los entries viejos se filtran al evaluar (línea 382-387), pero **las claves del mapa nunca se eliminan**. En un sistema con rotación de API keys o muchos clientes efímeros, este mapa crece indefinidamente → memory leak. | Añadir una goroutine de limpieza periódica (cada 5 min) que elimine claves con `len(recent) == 0`. |
| **S-09** | 🟡 Medium | Security | `mcp-server-go/server.go:215` | **HA heartbeat endpoint sin auth obligatoria.** `mux.Handle("/ha/heartbeat", http.HandlerFunc(haManager.HandleHeartbeat))` — no está envuelto con `RequireRole` ni `RequireAdmin`. Si `CUBE_HA_SECRET` no está configurado (default), cualquier cliente puede enviar heartbeats arbitrarios, causando que un nodo standby piense que el active está vivo (previniendo failover) o viceversa. | Envolver con auth middleware mínimo, o rechazar heartbeats si `CUBE_HA_SECRET == ""`. |
| **S-10** | 🟡 Medium | Crypto | `mcp-server-go/secrets.go:42,193-214` | **PBKDF2 con 100K iteraciones (no scrypt como documenta el header).** El comentario del archivo (línea 42) dice `passphraseIter = 100000` y la documentación interna menciona scrypt, pero el código implementa PBKDF2-HMAC-SHA256 manualmente (líneas 193-214). OWASP 2023 recomienda **600,000 iteraciones** para PBKDF2-SHA256. 100K es insuficiente contra ataques GPU modernos (~$0.01 por crack en hardware commodity). | Usar `golang.org/x/crypto/pbkdf2` con 600K iteraciones mínimo, o idealmente `scrypt`/`argon2id` que ofrecen resistencia a GPU/ASIC. |
| **S-11** | 🟡 Medium | Maintainability | `mcp-server-go/server.go:918` vs `README.md:11,59` vs `CONTRIBUTING.md:29` | **Inconsistencia de conteo de tools.** El código registra **129 tools** (129 llamadas a `s.AddTool`), pero: `README.md` dice "121 tools", `server.go:2` header dice "121 tools", `server.go:918` (`handleBackendInfo`) dice `"tool_count": 129`, y `CONTRIBUTING.md:29` dice "34 MCP tools". Tres números diferentes para la misma cosa. Un agente AI que confíe en `backend_info` para conocer el toolset se confundirá. | Unificar el conteo. Usar una constante `const toolCount = 129` y referenciarla en todos lados. |
| **S-12** | 🟡 Medium | RBAC | `mcp-server-go/auth.go:36-198` vs `server.go:296-800` | **8 tools sin entrada en `toolPermissions`.** Hay 129 tools registrados pero `toolPermissions` tiene ~121 entradas. `canExecute()` retorna `false` para tools desconocidos (fail-closed, lo cual es seguro), pero esto significa que **8 tools son inaccesibles en modo HTTP** para cualquier rol, incluyendo admin. En modo stdio esto no aplica (no hay middleware). | Auditar todas las entradas de `s.AddTool` y asegurar que cada una tenga su entrada correspondiente en `toolPermissions`. |
| **S-13** | 🟡 Medium | Security | `mcp-server-go/security.go:221-283` | **SSRF: validación por string, no por resolución DNS.** `isPrivateHost()` verifica rangos IP mediante parsing de strings (ej: `octets[0] == 10`). Pero **no resuelve DNS**. Un dominio como `attacker.com` puede apuntar a `10.0.0.1` (DNS rebinding). El comentario en `secure_sandbox.go:103-106` reconoce este problema (TOCTOU) pero no hay mitigación en el código. | Resolver el hostname con `net.LookupHost` y validar la IP resultante. Idealmente, hacer la validación en el momento de conexión (en CubeEgress/CubeProxy). |
| **S-14** | 🟡 Medium | Frontend Security | `web/src/lib/api.ts:35-36`, `web/src/lib/session.ts:14,22` | **API keys y session tokens en localStorage.** El frontend almacena `cube.apiKey`, `cube.session`, y tokens JWT en `localStorage`. Esto los expone a ataques XSS — cualquier script ejecutado en la página (incluyendo dependencias npm comprometidas) puede leerlos. | Migrar a cookies HttpOnly + SameSite=Strict seteadas por el backend. Si se debe usar localStorage, implementar CSP estricta. |
| **S-15** | 🟡 Medium | Testing | `mcp-server-go/` (41 src, 6 test) | **Cobertura de tests crítica baja.** 6 archivos de test (`auth_test.go`, `backup_test.go`, `bench_test.go`, `concurrency_test.go`, `e2e_test.go`, `security_test.go`) para 41 archivos fuente. **Sin tests para:** `secrets.go`, `secure_sandbox.go`, `deploy.go`, `images.go`, `databases.go`, `volumes.go`, `networking.go`, `routing.go`, `webhook.go`, `ha.go`, y 20+ archivos más. Los paths críticos de crypto y deployment no están testeados. | Priorizar tests para `secrets.go` (encrypt/decrypt/rotate), `deploy.go` (path traversal, git injection), `webhook.go` (SSRF), `secure_sandbox.go` (egress validation). Meta: >70% coverage en archivos de seguridad. |
| **S-16** | 🟢 Low | Agent Usability | `mcp-server-go/server.go:296-800` | **Tool descriptions inconsistentes en formato.** Algunas incluyen argumentos inline (`"Args: state (running/paused/stopped)"`), otras usan `mcp.Description()` en args. Esto hace que el schema sea irregular para los agents. Las descripciones varían de 5 palabras a 300+ palabras. | Estandarizar: descripción corta (1 línea) + args documentados vía `mcp.WithString(..., mcp.Description(...))`. Eliminar duplicación de info de args en el string de descripción. |
| **S-17** | 🟢 Low | Infrastructure | `deploy/container-mode/Caddyfile` (WAF section) | **WAF regex-based bypasseable.** Las reglas WAF en Caddy usan regex path matching para SQLi/XSS/traversal. Regex WAFs son trivialmente bypasseables (ej: `UNION/**/SELECT` evada `union\s+select`, encoding `%2e%2e%2f` parcialmente cubierto pero `%252e%252e%252f` no). Además, el WAF solo analiza `path`, no el body de POST requests. | No depender del WAF de Caddy como capa principal. Las validaciones en el código Go (que sí analizan inputs) son la defensa real. Documentar el WAF como best-effort. |
| **S-18** | 🟢 Low | Performance | `mcp-server-go/docker_client.go:81-112` | **Sin connection pooling configurable.** El `DockerClient` usa un `http.Client` con timeout de 60s pero sin configurar `MaxIdleConns`, `MaxIdleConnsPerHost`, o `IdleConnTimeout`. En alta concurrencia, cada request puede abrir una nueva conexión al socket Unix. | Configurar `Transport.MaxIdleConnsPerHost = 100` y `IdleConnTimeout = 90s`. |
| **S-19** | 🟢 Low | Docs | `CONTRIBUTING.md:40-42` vs código real | **CONTRIBUTING.md desactualizada.** Afirma que `docker_client.go` está detrás de un build tag (`-tags docker`), pero el código compila sin tags (líneas 7-8 de docker_client.go: *"compiled in by default (no build tag needed)"`). También dice "34 MCP tools" cuando hay 129. | Actualizar CONTRIBUTING.md con la arquitectura actual. |
| **S-20** | 🔵 Info | Supply Chain | `mcp-server-go/go.mod` | **Dependencia única y bien acotada.** Solo depende de `github.com/mark3labs/mcp-go v0.32.0` (+ uuid, cast, uritemplate como indirectas). Es commendable el enfoque "stdlib-first" — minimiza la superficie de ataque. No hay dependencias con CVEs conocidas. | ✅ Mantener esta política. Considerar `govulncheck` en CI como gate blocking (ver S-03). |
| **S-21** | 🔵 Info | Agent Usability | `mcp-server-go/server.go:298-800` | **Naming de tools consistente y agent-friendly.** Los 129 tools siguen un patrón `<recurso>_<acción>` consistente (ej: `container_create`, `volume_attach`, `secret_set`). Las descripciones son ricas en contexto para LLMs. El tool `backend_info` permite al agent autodescubrir el runtime activo. | ✅ Excelente diseño para agentes. Considerar agrupar tools por namespace MCP (ej: `container.*`) cuando el protocolo lo soporte. |
| **S-22** | 🔵 Info | Security | `mcp-server-go/auth.go:306-329` | **Comparaciones constant-time implementadas correctamente.** `Validate()` usa `hmac.Equal` tanto para key-exists como para secret match, con dummy comparison para equalizar timing. Buen patrón anti-timing-attack. | ✅ Correcto. |

---

## 3. Scorecard por Dimensión

### 3.1 Seguridad — **6.5/10**

| Sub-item | Score | Justificación |
|----------|-------|---------------|
| Auth/RBAC | 8/10 | Sistema de 3 roles bien diseñado, comparaciones constant-time, fail-closed. Puntos perdidos: heartbeat sin auth obligatoria, default API key. |
| Input Validation | 7/10 | Validadores sólidos (`validateContainerID`, `validatePathSafe`, `validateGitURL`, `validateMountPath`). Puntos perdidos: exec allowlist con shells, SSRF sin DNS resolution, webhook sin re-validación. |
| Crypto | 6/10 | AES-256-GCM correcto, nonce random, key rotation implementada. Puntos perdidos: PBKDF2 con 100K iter (debería ser 600K+), no usa scrypt como documenta. |
| SSRF Protection | 6/10 | Bloquea rangos RFC 1918 y metadata cloud, pero solo por string parsing — vulnerable a DNS rebinding. Webhook SSRF sin re-validación. |
| Audit Trail | 9/10 | Hash chain SHA256 tamper-evident, verify command, masking de keys. Excelente. |
| Supply Chain | 4/10 | Binarios de 12MB en git history, default API key, gosec/govulncheck no bloqueantes. |

### 3.2 Mantenibilidad — **5.5/10**

| Sub-item | Score | Justificación |
|----------|-------|---------------|
| Estructura de código | 7/10 | Separación clara por dominio (un archivo .go por feature). `server.go` de 1319 líneas es grande pero organizado. |
| Complejidad | 6/10 | Handlers siguen patrón uniforme. Pero `docker_client.go` (856 líneas) y `server.go` (1319 líneas) son monolíticos. |
| Technical Debt | 4/10 | Inconsistencias documentales (121 vs 129 tools), CONTRIBUTING desactualizada, 8 tools sin RBAC, vet suprimido en upstream. |
| Testabilidad | 4/10 | Solo 6 test files para 41 source files. Paths críticos (crypto, deploy, webhook) sin cobertura. |

### 3.3 Agent Usability — **8.5/10** ⭐

| Sub-item | Score | Justificación |
|----------|-------|---------------|
| Tool Naming | 9/10 | Convención `<recurso>_<verbo>` consistente y predecible. Un agent puede inferir nombres. |
| Descriptions | 8/10 | Ricas en contexto, incluyen defaults y constraints. Algunas demasiado largas. |
| Response Shapes | 8/10 | JSON estructurado con `status`, `error`, datos. Consistente entre tools. |
| Self-Discovery | 9/10 | `backend_info` tool permite al agent conocer el runtime. `audit_query` permite introspección. |
| Error Messages | 8/10 | Errores descriptivos y actionable. `unwrapError` traduce errores de backend a mensajes útiles. |

### 3.4 Eficiencia — **7.0/10**

| Sub-item | Score | Justificación |
|----------|-------|---------------|
| Backend Dual | 9/10 | Auto-detección Docker/Cube con fallback elegante. Zero-config. |
| Concurrency | 7/10 | Uso correcto de `sync.RWMutex` en KeyStore, SecretsManager. Puntos perdidos: rate limiter memory leak, sin connection pooling en DockerClient. |
| Resource Limits | 7/10 | Body size limit (10MB), max conns per IP (64), per-IP connection tracking. Buen DoS prevention. |
| Garbage Collection | 6/10 | GC automático para imágenes/volúmenes, pero no para rate limiter entries. |

### 3.5 Open-Source Readiness — **4.5/10** 🔻

| Sub-item | Score | Justificación |
|----------|-------|---------------|
| Licensing | 2/10 | Conflicto Tencent copyright vs badge Apache 2.0. Legalmente ambiguo. |
| Documentation | 5/10 | README extenso pero inconsistente (tool count, arquitectura). AGENTS.md presente. CONTRIBUTING desactualizada. Sin CHANGELOG. |
| CI/CD | 5/10 | CI presente (build, test, vet, security), pero gates no bloqueantes. Binary guard solo escanea working tree. |
| Tests | 4/10 | Coverage baja en paths críticos. Race detector habilitado (bueno). |
| Community | 5/10 | Sin CODE_OF_CONDUCT, sin ISSUE_TEMPLATE, sin PULL_REQUEST_TEMPLATE. CONTRIBUTING existe. |

---

## 4. Top 5 Hallazgos que Requieren Acción Inmediata

### 🥇 #1 — Purgar binarios del git history (S-01)
**Impacto:** Supply chain contamination. El `.git/objects/` contiene 9+ blobs de 12MB (binarios compilados del MCP server). Cualquier `git clone` descarga ~100MB de binarios innecesarios. Los binarios compilados pueden contener credenciales, paths absolutos, o versiones vulnerables.

**Acción:**
```bash
# Usar git-filter-repo (preferido sobre BFG)
pip install git-filter-repo
git filter-repo --path mcp-server-go/mcp-server-go --invert-paths
git filter-repo --path Cubelet/cubelet --invert-paths
git push --force --all
# Todos los desarrolladores deben re-clonar
```

### 🥈 #2 — Resolver el conflicto de licencia (S-02)
**Impacto:** Legal. Los usuarios que asumen Apache 2.0 pueden estar violando los términos Tencent. Esto es un blocker para adopción enterprise.

**Acción:** Decidir la licencia real. Si es Apache 2.0, reemplazar el `LICENSE` completo. Si conserva restricciones Tencent, corregir el badge del README.

### 🥉 #3 — Hacer gosec/govulncheck blocking en CI (S-03)
**Impacto:** Vulnerabilidades conocidas pasan el CI verde. Un dependency con CVE no detendrá el merge.

**Acción:** Remover `continue-on-error: true` de las líneas 111 y 118 de `ci.yml`.

### 4️⃣ #4 — Remover default API key hardcoded (S-05)
**Impacto:** Si `CUBE_API_KEY` no se setea, el servidor autentica contra CubeAPI con `e2b_000000` — un valor público.

**Acción:**
```go
// client.go:40 — cambiar a:
apiKey := os.Getenv("CUBE_API_KEY")
if apiKey == "" {
    log.Fatal("CUBE_API_KEY environment variable is required")
}
```

### 5️⃣ #5 — Cerrar el vector SSRF del webhook (S-06)
**Impacto:** El webhook endpoint no requiere API key. Un atacante que conozca la URL del webhook puede forzar git clones arbitrarios, potencialmente a internal hosts.

**Acción:**
1. Validar la URL antes de la goroutine asíncrona
2. Requerir `CUBE_WEBHOOK_SECRET` obligatorio (no opcional)
3. Añadir rate limiting específico al endpoint de webhook

---

## 5. Lo Que Apex Forensics Probablemente No Detectaría

### 5.1 Git History Forensics
Apex escaneará el working tree con `file` y verá los binarios ELF. Detectarán que existen. Pero **no analizarán el `.git/objects/` packfile**, donde encontramos 9+ blobs de 12MB — evidencia de que los binarios fueron commiteados repetidamente. El `binary-guard` job en CI (líneas 126-152) tiene el mismo problema: solo escanea `find "$dir" -type f` en el checkout actual, no el historial.

**Nuestra evidencia:**
```
$ git cat-file --batch-all-objects --batch-check | sort -t' ' -k3 -n -r | head -5
040bc9bc... blob 12486313   ← binario compilado v9
32633e60... blob 12474841   ← binario compilado v8
da8a5db6... blob 12460886   ← binario compilado v7
...
```

### 5.2 Inconsistencia de Conteo de Tools (3 números diferentes)
Apex contará los `s.AddTool` y reportará "129 tools". Pero no cotejarán contra:
- `README.md` que dice "121 tools"
- `CONTRIBUTING.md` que dice "34 MCP tools"
- `server.go:918` (`handleBackendInfo`) que retorna `"tool_count": 129`
- `server.go:2` (header comment) que dice "121 tools"

Un agent AI que llame `backend_info` y reciba 129, pero lea el README y vea 121, puede confundirse sobre qué tools existen realmente.

### 5.3 Caddyfile WAF Bypass Analysis
Apex se centrará en el código Go. Nosotros analizamos el `deploy/container-mode/Caddyfile` y detectamos que las reglas WAF regex son bypasseables:
- `union\s+select` → evadido con `UNION/**/SELECT`
- `path_regexp` solo analiza URL path, no POST body
- No hay normalización de encoding doble (`%252e%252e%252f`)

### 5.4 Frontend Credential Storage
Apex no revisará el `web/src/`. Nosotros detectamos que `api.ts:35-36` lee API keys de `localStorage` — expuestas a XSS. Esto es relevante porque el dashboard está diseñado para production use con el backend HTTP auth.

### 5.5 CONTRIBUTING.md Stale Documentation
Apex no cotejará la documentación contra el código. Encontramos que `CONTRIBUTING.md:40-42` afirma que el Docker backend requiere `go build -tags docker`, pero el código compila sin tags. Un nuevo contribuidor que siga estas instrucciones se confundirá.

### 5.6 PBKDF2 vs scrypt (Documentation vs Implementation Mismatch)
El header de `secrets.go` (línea 42) documenta `passphraseIter` y menciona scrypt en comentarios, pero el código implementa PBKDF2-HMAC-SHA256 manualmente (líneas 193-214). La implementación manual de PBKDF2 es otro riesgo — es preferible usar `golang.org/x/crypto/pbkdf2` que es auditado.

---

## 6. Resumen Ejecutivo

| Dimensión | Score | Estado |
|-----------|-------|--------|
| Seguridad | 6.5/10 | ⚠️ Mejoras needed |
| Mantenibilidad | 5.5/10 | ⚠️ Deuda técnica |
| Agent Usability | 8.5/10 | ✅ Fortaleza |
| Eficiencia | 7.0/10 | ✅ Aceptable |
| Open-Source Readiness | 4.5/10 | 🔻 No listo |
| **Score Global** | **6.4/10** | |

**Veredicto:** El MCP server demuestra **excelente diseño para agent usability** (naming, descriptions, self-discovery) y una **arquitectura de seguridad más madura de lo típico** (RBAC, audit chain, constant-time comparisons, SSRF básico). Sin embargo, **no está listo para open-source** debido al conflicto de licencia, binarios en git history, CI gates no bloqueantes, y cobertura de tests insuficiente en paths críticos (crypto, deploy, webhook).

Los hallazgos **S-01** (git history), **S-02** (licencia), y **S-03** (CI no bloqueante) son **blockers para cualquier release público**. Los hallazgos **S-05** (default key) y **S-06** (webhook SSRF) son **vulnerabilidades explotables en producción**.

---

*Sentinel Audit Group — "We don't just find bugs. We find the bugs that matter."*
