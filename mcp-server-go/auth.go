// Package main: authentication, RBAC, rate limiting, and audit logging.
// Replaces auth_gateway.py — built directly into the MCP server.
package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---- Role definitions ----

type Role string

const (
	RoleViewer  Role = "viewer"
	RoleOperator Role = "operator"
	RoleAdmin   Role = "admin"
)

// Permission levels for each tool.
// viewer: read-only (list, get, health, logs)
// operator: deploy, create, kill, pause, resume, exec, volumes
// admin: everything (delete_volume, etc.)
var toolPermissions = map[string]Role{
	// Read-only — viewer
	"cluster_health":    RoleViewer,
	"cluster_overview":  RoleViewer,
	"cluster_versions":  RoleViewer,
	"list_nodes":        RoleViewer,
	"get_node":          RoleViewer,
	"list_containers":   RoleViewer,
	"get_container":     RoleViewer,
	"get_container_logs": RoleViewer,
	"list_templates":    RoleViewer,
	"get_template":      RoleViewer,
	"list_volumes":      RoleViewer,
	// Mutating — operator
	"create_container":  RoleOperator,
	"create_template":   RoleOperator,
	"pause_container":   RoleOperator,
	"resume_container":  RoleOperator,
	"kill_container":    RoleOperator,
	"exec_in_container": RoleOperator,
	"create_volume":     RoleOperator,
	"deploy_from_git":   RoleOperator,
	"deploy_from_code":  RoleOperator,
	"update_code":       RoleOperator,
	// Destructive — admin
	"delete_volume":     RoleAdmin,
}

// roleLevel returns a numeric level for comparison (higher = more permissions).
func roleLevel(r Role) int {
	switch r {
	case RoleAdmin:
		return 3
	case RoleOperator:
		return 2
	case RoleViewer:
		return 1
	default:
		return 0
	}
}

// canExecute checks if the given role can execute the tool.
func canExecute(role Role, toolName string) bool {
	required, ok := toolPermissions[toolName]
	if !ok {
		return false // unknown tools blocked by default
	}
	return roleLevel(role) >= roleLevel(required)
}

// ---- API Key store ----

// APIKey represents a stored credential.
type APIKey struct {
	Key       string    `json:"key"`
	Secret    string    `json:"secret"`
	Role      Role      `json:"role"`
	Label     string    `json:"label"`
	CreatedAt time.Time `json:"created_at"`
	LastUsed  time.Time `json:"last_used"`
	Disabled  bool      `json:"disabled"`
}

// KeyStore manages API keys with file persistence.
type KeyStore struct {
	mu       sync.RWMutex
	keys     map[string]*APIKey
	filePath string
}

func newKeyStore() *KeyStore {
	ks := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: envOr("CUBE_AUTH_KEYS_FILE", "/var/lib/cube-container/auth-keys.json"),
	}
	ks.load()
	return ks
}

func (ks *KeyStore) load() {
	data, err := os.ReadFile(ks.filePath)
	if err != nil {
		return
	}
	var keys []*APIKey
	if err := json.Unmarshal(data, &keys); err != nil {
		return
	}
	ks.mu.Lock()
	defer ks.mu.Unlock()
	for _, k := range keys {
		ks.keys[k.Key] = k
	}
}

func (ks *KeyStore) save() {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	ks.saveLocked()
}

// saveLocked writes keys to disk. Caller must hold ks.mu (RLock or Lock).
func (ks *KeyStore) saveLocked() {
	var keys []*APIKey
	for _, k := range ks.keys {
		keys = append(keys, k)
	}
	data, _ := json.MarshalIndent(keys, "", "  ")
	os.MkdirAll(filepath.Dir(ks.filePath), 0700)
	os.WriteFile(ks.filePath, data, 0600)
}

// GenerateKey creates a new API key + secret pair.
func (ks *KeyStore) GenerateKey(role Role, label string) (*APIKey, error) {
	key := "cc_live_" + randomHex(16)
	secret := "sec_" + randomHex(24)

	apiKey := &APIKey{
		Key:       key,
		Secret:    secret,
		Role:      role,
		Label:     label,
		CreatedAt: time.Now(),
	}

	ks.mu.Lock()
	ks.keys[key] = apiKey
	ks.mu.Unlock()
	ks.save()

	return apiKey, nil
}

// Validate checks an API key + secret pair and returns the key if valid.
func (ks *KeyStore) Validate(key, secret string) (*APIKey, error) {
	ks.mu.RLock()
	k, exists := ks.keys[key]
	ks.mu.RUnlock()

	if !exists || k.Disabled {
		return nil, fmt.Errorf("invalid API key")
	}
	if !hmac.Equal([]byte(k.Secret), []byte(secret)) {
		return nil, fmt.Errorf("invalid secret")
	}

	// Update last used
	ks.mu.Lock()
	k.LastUsed = time.Now()
	ks.mu.Unlock()

	return k, nil
}

// Revoke disables an API key.
func (ks *KeyStore) Revoke(key string) error {
	ks.mu.Lock()
	defer ks.mu.Unlock()
	k, exists := ks.keys[key]
	if !exists {
		return fmt.Errorf("key not found")
	}
	k.Disabled = true
	ks.saveLocked()
	return nil
}

// List returns all keys (secrets redacted in output).
func (ks *KeyStore) List() []*APIKey {
	ks.mu.RLock()
	defer ks.mu.RUnlock()
	var result []*APIKey
	for _, k := range ks.keys {
		redacted := *k
		redacted.Secret = "[redacted]"
		result = append(result, &redacted)
	}
	return result
}

// ---- Rate limiter (sliding window) ----

type rateLimiter struct {
	mu       sync.Mutex
	requests map[string][]time.Time
	limit    int
	window   time.Duration
}

func newRateLimiter(limit int, window time.Duration) *rateLimiter {
	return &rateLimiter{
		requests: make(map[string][]time.Time),
		limit:    limit,
		window:   window,
	}
}

func (rl *rateLimiter) Allow(key string) bool {
	rl.mu.Lock()
	defer rl.mu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rl.window)

	// Filter to requests within the window
	var recent []time.Time
	for _, t := range rl.requests[key] {
		if t.After(cutoff) {
			recent = append(recent, t)
		}
	}

	if len(recent) >= rl.limit {
		rl.requests[key] = recent
		return false
	}

	recent = append(recent, now)
	rl.requests[key] = recent
	return true
}

// ---- Audit logger (JSONL with tamper-evident hashing) ----

type AuditEntry struct {
	Timestamp  string                 `json:"timestamp"`
	Key        string                 `json:"key"`
	Role       string                 `json:"role"`
	Method     string                 `json:"method"`
	Path       string                 `json:"path"`
	Tool       string                 `json:"tool,omitempty"`
	StatusCode int                    `json:"status_code"`
	Duration   string                 `json:"duration"`
	Allowed    bool                   `json:"allowed"`
	Reason     string                 `json:"reason,omitempty"`
	PrevHash   string                 `json:"prev_hash"`
	Hash       string                 `json:"hash"`
}

type AuditLogger struct {
	mu       sync.Mutex
	file     *os.File
	prevHash string
}

func newAuditLogger() *AuditLogger {
	logPath := envOr("CUBE_AUDIT_LOG", "/var/lib/cube-container/audit.logl")
	os.MkdirAll(filepath.Dir(logPath), 0700)
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] warning: could not open audit log: %v\n", err)
		return &AuditLogger{}
	}
	return &AuditLogger{file: f}
}

func (al *AuditLogger) Log(entry AuditEntry) {
	if al.file == nil {
		return
	}
	al.mu.Lock()
	defer al.mu.Unlock()

	entry.PrevHash = al.prevHash
	entry.Hash = computeAuditHash(entry)

	line, _ := json.Marshal(entry)
	al.file.Write(line)
	al.file.Write([]byte("\n"))

	al.prevHash = entry.Hash
}

// computeAuditHash creates a SHA256 chain: hash(prev_hash + entry fields).
func computeAuditHash(entry AuditEntry) string {
	payload := fmt.Sprintf("%s|%s|%s|%s|%s|%d|%s|%v|%s",
		entry.Timestamp, entry.Key, entry.Role,
		entry.Method, entry.Path, entry.StatusCode,
		entry.Duration, entry.Allowed, entry.PrevHash,
	)
	h := sha256.Sum256([]byte(payload))
	return hex.EncodeToString(h[:])
}

// VerifyAuditChain reads an audit log file and verifies hash integrity.
func VerifyAuditChain(path string) (int, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, err
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	prevHash := ""
	count := 0
	for i, line := range lines {
		if line == "" {
			continue
		}
		var entry AuditEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			return i, fmt.Errorf("line %d: malformed JSON: %w", i+1, err)
		}
		if entry.PrevHash != prevHash {
			return i, fmt.Errorf("line %d: hash chain broken (expected prev %q, got %q)", i+1, prevHash, entry.PrevHash)
		}
		expected := computeAuditHash(entry)
		if entry.Hash != expected {
			return i, fmt.Errorf("line %d: hash mismatch (expected %s, got %s)", i+1, expected, entry.Hash)
		}
		prevHash = entry.Hash
		count++
	}
	return count, nil
}

// ---- Auth middleware ----

// AuthMiddleware wraps an http.Handler with API key auth, RBAC, rate limiting, audit.
type AuthMiddleware struct {
	keys    *KeyStore
	limiter *rateLimiter
	audit   *AuditLogger
}

func newAuthMiddleware(keys *KeyStore, limiter *rateLimiter, audit *AuditLogger) *AuthMiddleware {
	return &AuthMiddleware{
		keys:    keys,
		limiter: limiter,
		audit:   audit,
	}
}

// extractAuth pulls API key and secret from request headers.
// Supports two formats:
//   X-API-Key: cc_live_xxx  +  X-API-Secret: sec_yyy
//   Authorization: Bearer cc_live_xxx:sec_yyy
func extractAuth(r *http.Request) (key, secret string) {
	// Try custom headers first
	key = r.Header.Get("X-API-Key")
	secret = r.Header.Get("X-API-Secret")
	if key != "" && secret != "" {
		return key, secret
	}
	// Fall back to Bearer token: "key:secret"
	auth := r.Header.Get("Authorization")
	if strings.HasPrefix(auth, "Bearer ") {
		token := strings.TrimPrefix(auth, "Bearer ")
		parts := strings.SplitN(token, ":", 2)
		if len(parts) == 2 {
			return parts[0], parts[1]
		}
	}
	return "", ""
}

// Wrap returns an http.Handler that enforces auth + RBAC + rate limit + audit.
// toolExtractor is a function that reads the tool name from the request body
// (needed because RBAC checks happen before the tool executes).
func (am *AuthMiddleware) Wrap(next http.Handler, toolExtractor func(*http.Request) string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()

		key, secret := extractAuth(r)
		statusCode := 200
		allowed := true
		reason := ""

		// Authenticate
		apiKey, err := am.keys.Validate(key, secret)
		if err != nil {
			statusCode = 401
			allowed = false
			reason = err.Error()
			writeJSONError(w, statusCode, reason)
			am.logAudit(start, key, "", r, statusCode, allowed, reason, "")
			return
		}

		// Rate limit
		if !am.limiter.Allow(key) {
			statusCode = 429
			allowed = false
			reason = "rate limit exceeded"
			writeJSONError(w, statusCode, reason)
			am.logAudit(start, key, string(apiKey.Role), r, statusCode, allowed, reason, "")
			return
		}

		// RBAC: extract tool name and check permission
		toolName := ""
		if toolExtractor != nil {
			toolName = toolExtractor(r)
		}
		if toolName != "" && !canExecute(apiKey.Role, toolName) {
			statusCode = 403
			allowed = false
			reason = fmt.Sprintf("role '%s' cannot execute '%s' (requires %s)", apiKey.Role, toolName, toolPermissions[toolName])
			writeJSONError(w, statusCode, reason)
			am.logAudit(start, key, string(apiKey.Role), r, statusCode, allowed, reason, toolName)
			return
		}

		// Wrap the ResponseWriter to capture status code
		rw := &statusCaptureWriter{ResponseWriter: w, status: 200}
		next.ServeHTTP(rw, r)
		statusCode = rw.status

		am.logAudit(start, key, string(apiKey.Role), r, statusCode, allowed, reason, toolName)
	})
}

func (am *AuthMiddleware) logAudit(start time.Time, key, role string, r *http.Request, status int, allowed bool, reason, tool string) {
	if am.audit == nil {
		return
	}
	am.audit.Log(AuditEntry{
		Timestamp:  start.UTC().Format(time.RFC3339Nano),
		Key:        maskKey(key),
		Role:       role,
		Method:     r.Method,
		Path:       r.URL.Path,
		Tool:       tool,
		StatusCode: status,
		Duration:   time.Since(start).String(),
		Allowed:    allowed,
		Reason:     reason,
	})
}

// maskKey shows first 12 chars + *** for audit logs.
func maskKey(key string) string {
	if len(key) <= 12 {
		return key
	}
	return key[:12] + "***"
}

// ---- Helpers ----

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func writeJSONError(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]string{"error": msg})
	w.Write(body)
}

// statusCaptureWriter wraps http.ResponseWriter to capture the status code.
type statusCaptureWriter struct {
	http.ResponseWriter
	status    int
	wroteHead bool
}

func (w *statusCaptureWriter) WriteHeader(code int) {
	if !w.wroteHead {
		w.status = code
		w.wroteHead = true
	}
	w.ResponseWriter.WriteHeader(code)
}

func (w *statusCaptureWriter) Write(b []byte) (int, error) {
	if !w.wroteHead {
		w.status = 200
		w.wroteHead = true
	}
	return w.ResponseWriter.Write(b)
}

// AuthAdminAPI provides endpoints for key management (/auth/keys).
type AuthAdminAPI struct {
	keys *KeyStore
}

func (a *AuthAdminAPI) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	switch {
	case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/auth/keys"):
		a.handleCreateKey(w, r)
	case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/auth/keys"):
		a.handleListKeys(w, r)
	case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/auth/keys/"):
		a.handleRevokeKey(w, r)
	default:
		writeJSONError(w, 404, "not found")
	}
}

func (a *AuthAdminAPI) handleCreateKey(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role  string `json:"role"`
		Label string `json:"label"`
	}
	body, _ := io.ReadAll(r.Body)
	json.Unmarshal(body, &req)

	role := Role(req.Role)
	if role != RoleViewer && role != RoleOperator && role != RoleAdmin {
		writeJSONError(w, 400, "invalid role (must be viewer, operator, or admin)")
		return
	}

	apiKey, err := a.keys.GenerateKey(role, req.Label)
	if err != nil {
		writeJSONError(w, 500, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(apiKey)
}

func (a *AuthAdminAPI) handleListKeys(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(a.keys.List())
}

func (a *AuthAdminAPI) handleRevokeKey(w http.ResponseWriter, r *http.Request) {
	keyID := strings.TrimPrefix(r.URL.Path, "/auth/keys/")
	if keyID == "" {
		writeJSONError(w, 400, "key id required")
		return
	}
	if err := a.keys.Revoke(keyID); err != nil {
		writeJSONError(w, 404, err.Error())
		return
	}
	w.WriteHeader(204)
}
