// Package main: security validation for input sanitization.
// Port of security.py — path traversal prevention, git URL validation,
// command injection blocking.
package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

var safeNameRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$`)

// validateSafeName validates that a name is safe to use as directory/volume name.
// Rejects path traversal, path separators, null bytes, names starting with dot.
func validateSafeName(name string) (string, error) {
	if name == "" {
		return "", fmt.Errorf("name cannot be empty")
	}
	if !safeNameRe.MatchString(name) {
		return "", fmt.Errorf("invalid name '%s': must be alphanumeric with dots, dashes, or underscores, 1-64 chars, cannot start with dot", name)
	}
	return name, nil
}

var allowedGitProtocols = []string{"https://", "ssh://"}

// allowedGitProtocolsInsecure holds protocols that are accepted ONLY when
// CUBE_ALLOW_INSECURE_GIT=true. These are http:// and git:// (plaintext).
var allowedGitProtocolsInsecure = []string{"http://", "git://"}

// validateCommand validates a command for exec_in_container.
//
// We use an ALLOWLIST approach: only permit commands that match known-safe
// prefixes. This is more restrictive than a blacklist but far harder to bypass.
// The allowlist can be extended via CUBE_EXEC_ALLOWLIST (comma-separated).
var execAllowlist = []string{
	"ls", "cat", "head", "tail", "grep", "find", "wc", "sort", "uniq",
	"ps", "top", "env", "printenv", "whoami", "hostname", "date", "uname",
	"df", "du", "free", "uptime", "id", "pwd", "stat", "file", "diff",
	"echo", "printf", "test", "true", "false",
	"python", "python3", "pip", "pip3", "node", "npm", "go", "cargo",
	"git", "curl", "wget",
	"pgrep", "pkill", "kill", "killall",
	"mkdir", "touch", "cp", "mv",
	"tar", "zip", "unzip", "gzip", "gunzip",
	"sed", "awk", "cut", "tr",
	"redis-cli", "psql", "mysql",
	"nc", "ss", "netstat",
	"sh", "bash",  // shells are allowed but chained destructive commands are blocked below
}

// execDenylist catches destructive patterns even when the binary is allowed
// (e.g. "sh -c 'rm -rf /'"). These are checked against the FULL command string.
var execDenylist = []string{
	"rm -rf /",
	"rm -rf /*",
	"mkfs",
	"dd if=/dev/zero",
	"dd if=/dev/random",
	":(){ :|:& };:",
	"chmod 777 /",
	"shutdown",
	"reboot",
	"halt",
	"poweroff",
	"| sh",
	"| bash",
	"|/bin/sh",
	"|/bin/bash",
}

func init() {
	// Allow extending the allowlist via env var.
	if extra := os.Getenv("CUBE_EXEC_ALLOWLIST"); extra != "" {
		for _, cmd := range splitAndTrim(extra) {
			execAllowlist = append(execAllowlist, cmd)
		}
	}
}

func splitAndTrim(s string) []string {
	var result []string
	for _, part := range strings.Split(s, ",") {
		part = strings.TrimSpace(part)
		if part != "" {
			result = append(result, part)
		}
	}
	return result
}

func validateCommand(cmd string) (string, error) {
	cmd = strings.TrimSpace(cmd)
	if cmd == "" {
		return "", fmt.Errorf("command cannot be empty")
	}

	// Check denylist against the full command string (case-insensitive).
	lowerCmd := strings.ToLower(cmd)
	for _, pattern := range execDenylist {
		if strings.Contains(lowerCmd, pattern) {
			return "", fmt.Errorf("command contains blocked pattern: %s", pattern)
		}
	}

	// Extract the base command (first token) and check against allowlist.
	// Handle leading env vars (FOO=bar cmd) by skipping assignments.
	tokens := strings.Fields(cmd)
	binStart := 0
	for i := 0; i < len(tokens); i++ {
		if !strings.Contains(tokens[i], "=") || i == 0 && !strings.Contains(tokens[i], "=") {
			break
		}
		// If it looks like VAR=value, skip it
		if strings.Contains(tokens[i], "=") && !strings.HasPrefix(tokens[i], "-") {
			binStart = i + 1
			continue
		}
		break
	}

	if binStart >= len(tokens) {
		return "", fmt.Errorf("no command found (only env var assignments)")
	}

	baseCmd := tokens[binStart]
	// Strip path prefix: /usr/bin/ls → ls
	if idx := strings.LastIndex(baseCmd, "/"); idx >= 0 {
		baseCmd = baseCmd[idx+1:]
	}
	baseCmd = strings.ToLower(baseCmd)

	allowed := false
	for _, permitted := range execAllowlist {
		if baseCmd == permitted {
			allowed = true
			break
		}
	}

	if !allowed {
		return "", fmt.Errorf("command '%s' is not in the allowlist (extend via CUBE_EXEC_ALLOWLIST env var)", baseCmd)
	}

	return cmd, nil
}

// validatePathSafe ensures a resolved path is contained within root.
func validatePathSafe(path, root string) (string, error) {
	resolved := resolvePath(path)
	rootResolved := resolvePath(root)
	if !strings.HasPrefix(resolved+string(filepath.Separator), rootResolved+string(filepath.Separator)) && resolved != rootResolved {
		return "", fmt.Errorf("path '%s' escapes allowed root '%s'", path, root)
	}
	return resolved, nil
}

// validateGitURL validates that a git URL uses an allowed protocol and does not
// target private/internal IP ranges (SSRF prevention, H1).
// Only https:// and ssh:// are allowed in production. http:// and git:// are
// accepted for local dev (Gitea/Gogs on localhost) but logged.
func validateGitURL(url string) (string, error) {
	url = strings.TrimSpace(url)
	if url == "" {
		return "", fmt.Errorf("git URL cannot be empty")
	}

	// Check secure protocols first
	allowed := false
	for _, proto := range allowedGitProtocols {
		if strings.HasPrefix(url, proto) {
			allowed = true
			break
		}
	}

	// Check insecure protocols (only if explicitly enabled)
	if !allowed && strings.EqualFold(os.Getenv("CUBE_ALLOW_INSECURE_GIT"), "true") {
		for _, proto := range allowedGitProtocolsInsecure {
			if strings.HasPrefix(url, proto) {
				allowed = true
				fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: insecure git protocol '%s' accepted (CUBE_ALLOW_INSECURE_GIT=true)\n", proto)
				break
			}
		}
	}

	if !allowed {
		return "", fmt.Errorf("invalid git URL '%s': only %s are allowed (http:// and git:// require CUBE_ALLOW_INSECURE_GIT=true)", url, strings.Join(allowedGitProtocols, ", "))
	}
	// Reject URLs with embedded credentials (user:pass@host)
	parts := strings.SplitN(url, "://", 2)
	if len(parts) == 2 {
		hostPart := strings.SplitN(parts[1], "/", 2)[0]
		// Strip port for IP check
		hostOnly := strings.SplitN(hostPart, ":", 2)[0]
		// Reject @ in the host portion (embedded credentials)
		if strings.Contains(hostPart, "@") {
			// But allow user@host format for ssh (e.g. git@github.com)
			// Only reject if there's user:pass@ (colon before @)
			atIdx := strings.LastIndex(hostPart, "@")
			beforeAt := hostPart[:atIdx]
			if strings.Contains(beforeAt, ":") {
				return "", fmt.Errorf("git URL with embedded credentials is not allowed")
			}
		}
		// SSRF check: reject private/internal IP ranges
		if isPrivateHost(hostOnly) {
			return "", fmt.Errorf("git URL points to a private/internal host '%s' — SSRF protection blocks this", hostOnly)
		}
	}
	return url, nil
}

// isPrivateHost returns true if the hostname is a private/internal address.
// Checks RFC 1918 ranges, loopback, link-local, and cloud metadata endpoints.
func isPrivateHost(host string) bool {
	// Cloud metadata endpoints — must be blocked
	if host == "169.254.169.254" || host == "metadata.google.internal" || host == "metadata" {
		return true
	}

	// Check if it's a literal IP
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		// Parse as IPv4
		var octets [4]int
		valid := true
		for i, p := range parts {
			n := 0
			for _, c := range p {
				if c < '0' || c > '9' {
					valid = false
					break
				}
				n = n*10 + int(c-'0')
			}
			if !valid || n > 255 {
				valid = false
				break
			}
			octets[i] = n
		}
		if valid {
			// 10.0.0.0/8
			if octets[0] == 10 {
				return true
			}
			// 172.16.0.0/12
			if octets[0] == 172 && octets[1] >= 16 && octets[1] <= 31 {
				return true
			}
			// 192.168.0.0/16
			if octets[0] == 192 && octets[1] == 168 {
				return true
			}
			// 127.0.0.0/8 (loopback)
			if octets[0] == 127 {
				return true
			}
			// 169.254.0.0/16 (link-local + cloud metadata)
			if octets[0] == 169 && octets[1] == 254 {
				return true
			}
			// 0.0.0.0/8
			if octets[0] == 0 {
				return true
			}
		}
	}

	// localhost variants
	host = strings.ToLower(host)
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return true
	}

	return false
}

// sanitizeGitURLForName extracts a safe directory name from a git URL.
func sanitizeGitURLForName(url string) string {
	url = strings.TrimRight(url, "/")
	parts := strings.Split(url, "/")
	name := strings.TrimSuffix(parts[len(parts)-1], ".git")
	// Replace any non-safe char with dash
	reg := regexp.MustCompile(`[^a-zA-Z0-9._-]`)
	name = reg.ReplaceAllString(name, "-")
	// Ensure it doesn't start with dot or dash
	if len(name) > 0 && (name[0] == '.' || name[0] == '-') {
		name = "app-" + name
	}
	if name == "" {
		name = "unnamed-app"
	}
	return name
}

// resolvePath resolves symlinks and returns absolute path.
func resolvePath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		return path
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		// Path doesn't exist yet — use the abs path directly
		return abs
	}
	return resolved
}

// isGitInstalled checks if git is available on the system.
func isGitInstalled() bool {
	_, err := exec.LookPath("git")
	return err == nil
}
