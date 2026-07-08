package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestKeyGenerationAndValidation(t *testing.T) {
	dir := t.TempDir()
	ks := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: filepath.Join(dir, "keys.json"),
	}

	// Generate key
	k, err := ks.GenerateKey(RoleOperator, "test-operator")
	if err != nil {
		t.Fatal(err)
	}
	if k.Key == "" || k.Secret == "" {
		t.Fatal("key or secret empty")
	}
	if k.Role != RoleOperator {
		t.Fatalf("expected operator, got %s", k.Role)
	}
	if k.Disabled {
		t.Fatal("new key should not be disabled")
	}

	// Validate correct
	validated, err := ks.Validate(k.Key, k.Secret)
	if err != nil {
		t.Fatalf("validate failed: %v", err)
	}
	if validated.Key != k.Key {
		t.Fatal("validated key mismatch")
	}

	// Wrong secret
	_, err = ks.Validate(k.Key, "wrong")
	if err == nil {
		t.Fatal("expected error for wrong secret")
	}

	// Unknown key
	_, err = ks.Validate("cc_live_nonexistent", k.Secret)
	if err == nil {
		t.Fatal("expected error for unknown key")
	}
}

func TestKeyPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "keys.json")

	// Create and save
	ks1 := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: path,
	}
	k, _ := ks1.GenerateKey(RoleAdmin, "persisted")
	ks1.save()

	// Load into new store
	ks2 := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: path,
	}
	ks2.load()

	validated, err := ks2.Validate(k.Key, k.Secret)
	if err != nil {
		t.Fatalf("persisted key not valid after reload: %v", err)
	}
	if validated.Label != "persisted" {
		t.Fatalf("expected label 'persisted', got '%s'", validated.Label)
	}
}

func TestKeyRevocation(t *testing.T) {
	dir := t.TempDir()
	ks := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: filepath.Join(dir, "keys.json"),
	}
	k, _ := ks.GenerateKey(RoleViewer, "temp")

	// Should work
	_, err := ks.Validate(k.Key, k.Secret)
	if err != nil {
		t.Fatal(err)
	}

	// Revoke
	ks.Revoke(k.Key)

	// Should fail now
	_, err = ks.Validate(k.Key, k.Secret)
	if err == nil {
		t.Fatal("revoked key should not validate")
	}
}

func TestRBAC(t *testing.T) {
	cases := []struct {
		role    Role
		tool    string
		allowed bool
	}{
		{RoleViewer, "list_containers", true},
		{RoleViewer, "cluster_health", true},
		{RoleViewer, "create_container", false},
		{RoleViewer, "delete_volume", false},
		{RoleOperator, "create_container", true},
		{RoleOperator, "deploy_from_git", true},
		{RoleOperator, "delete_volume", false},
		{RoleAdmin, "delete_volume", true},
		{RoleAdmin, "create_container", true},
		{RoleAdmin, "list_containers", true},
	}
	for _, tc := range cases {
		result := canExecute(tc.role, tc.tool)
		if result != tc.allowed {
			t.Errorf("role '%s' + tool '%s': expected %v, got %v", tc.role, tc.tool, tc.allowed, result)
		}
	}
}

func TestRBACUnknownTool(t *testing.T) {
	if canExecute(RoleAdmin, "nonexistent_tool") {
		t.Error("unknown tools should be blocked by default")
	}
}

func TestRateLimiter(t *testing.T) {
	rl := newRateLimiter(3, 1000000000) // 3 per ~1s for testing

	// First 3 should pass
	for i := 0; i < 3; i++ {
		if !rl.Allow("key1") {
			t.Fatalf("request %d should be allowed", i+1)
		}
	}
	// 4th should fail
	if rl.Allow("key1") {
		t.Fatal("4th request should be rate limited")
	}

	// Different key should still pass
	if !rl.Allow("key2") {
		t.Fatal("different key should not be rate limited")
	}
}

func TestAuditHashChain(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.logl")
	al := &AuditLogger{
		file: mustOpenFile(path),
	}

	// Log a few entries
	for i := 0; i < 5; i++ {
		al.Log(AuditEntry{
			Timestamp:  "2026-01-01T00:00:00Z",
			Key:        "cc_live_test***",
			Role:       "operator",
			Method:     "POST",
			Path:       "/mcp",
			StatusCode: 200,
			Duration:   "1ms",
			Allowed:    true,
		})
	}
	al.file.Sync()

	// Verify integrity
	count, err := VerifyAuditChain(path)
	if err != nil {
		t.Fatalf("audit chain verification failed: %v", err)
	}
	if count != 5 {
		t.Fatalf("expected 5 entries, got %d", count)
	}
}

func TestAuditHashChainTamperDetection(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "audit.logl")
	al := &AuditLogger{
		file: mustOpenFile(path),
	}

	// Log 3 entries
	for i := 0; i < 3; i++ {
		al.Log(AuditEntry{
			Timestamp:  "2026-01-01T00:00:00Z",
			Key:        "cc_live_test***",
			Role:       "viewer",
			Method:     "POST",
			Path:       "/mcp",
			StatusCode: 200,
			Duration:   "1ms",
			Allowed:    true,
		})
	}
	al.file.Sync()

	// Tamper: read, modify line 2, write back
	data, _ := os.ReadFile(path)
	lines := []string{}
	for _, l := range splitLines(string(data)) {
		if l != "" {
			lines = append(lines, l)
		}
	}
	// Modify the role in line 2 (this should break the hash chain)
	lines[1] = replaceInString(lines[1], `"role":"viewer"`, `"role":"admin"`)
	os.WriteFile(path, []byte(joinLines(lines)), 0600)

	// Verify should fail
	_, err := VerifyAuditChain(path)
	if err == nil {
		t.Fatal("expected hash chain verification to fail after tampering")
	}
}

func TestMaskKey(t *testing.T) {
	cases := []struct {
		input    string
		expected string
	}{
		{"cc_live_abcdef1234567890", "cc_live_abcd***"},
		{"short", "short"},
		{"", ""},
	}
	for _, tc := range cases {
		got := maskKey(tc.input)
		if got != tc.expected {
			t.Errorf("maskKey('%s'): expected '%s', got '%s'", tc.input, tc.expected, got)
		}
	}
}

func TestExtractAuthHeaders(t *testing.T) {
	// Custom headers
	r1 := mustNewRequest("POST", "/mcp", nil)
	r1.Header.Set("X-API-Key", "cc_live_test")
	r1.Header.Set("X-API-Secret", "sec_test")
	k1, s1 := extractAuth(r1)
	if k1 != "cc_live_test" || s1 != "sec_test" {
		t.Fatalf("custom header extraction failed: key='%s' secret='%s'", k1, s1)
	}

	// Bearer token
	r2 := mustNewRequest("POST", "/mcp", nil)
	r2.Header.Set("Authorization", "Bearer cc_live_test:sec_test")
	k2, s2 := extractAuth(r2)
	if k2 != "cc_live_test" || s2 != "sec_test" {
		t.Fatalf("bearer token extraction failed: key='%s' secret='%s'", k2, s2)
	}

	// No auth
	r3 := mustNewRequest("POST", "/mcp", nil)
	k3, s3 := extractAuth(r3)
	if k3 != "" || s3 != "" {
		t.Fatal("expected empty auth for no headers")
	}
}

// ---- Test helpers ----

func mustOpenFile(path string) *os.File {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		panic(err)
	}
	return f
}

func mustNewRequest(method, url string, body interface{}) *http.Request {
	r, err := http.NewRequest(method, url, nil)
	if err != nil {
		panic(err)
	}
	return r
}

func splitLines(s string) []string {
	var lines []string
	cur := ""
	for _, c := range s {
		if c == '\n' {
			lines = append(lines, cur)
			cur = ""
		} else {
			cur += string(c)
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

func joinLines(lines []string) string {
	result := ""
	for i, l := range lines {
		if i > 0 {
			result += "\n"
		}
		result += l
	}
	return result + "\n"
}

func replaceInString(s, old, new string) string {
	return strings.ReplaceAll(s, old, new)
}
