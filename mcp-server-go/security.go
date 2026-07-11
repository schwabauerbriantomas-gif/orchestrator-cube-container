// Package main: security validation for input sanitization.
// Port of security.py — path traversal prevention, git URL validation,
// command injection blocking.
package main

import (
	"fmt"
	"net"
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
//
// AUDIT FIX C-01 (Sentinel+Apex consensus): Removed sh, bash, python, python3,
// node, npm, go, cargo, curl, wget, nc, pip, pip3 from the default allowlist.
// These tools enable arbitrary code execution / reverse shells / data exfiltration,
// making the string-based denylist trivially bypassable (e.g. sh -c 'find / -delete').
// Operators who need these can add them via CUBE_EXEC_ALLOWLIST=sh,bash,python3.
// This is intentional defense-in-depth: the denylist alone cannot stop a shell.
//
var execAllowlist = []string{
	// Read-only diagnostics
	"ls", "cat", "head", "tail", "grep", "find", "wc", "sort", "uniq",
	"ps", "top", "env", "printenv", "whoami", "hostname", "date", "uname",
	"df", "du", "free", "uptime", "id", "pwd", "stat", "file", "diff",
	// Safe output
	"echo", "printf", "test", "true", "false",
	// Process management
	"pgrep",
	// File operations (non-destructive)
	"mkdir", "touch", "cp", "mv",
	// Archive
	"tar", "zip", "unzip", "gzip", "gunzip",
	// Text processing
	"sed", "awk", "cut", "tr",
	// Database clients (require credentials, limited surface)
	"redis-cli", "psql", "mysql",
	// Network diagnostics (no data transfer capability)
	"ss", "netstat",
	// Git (needed for deploy workflows)
	"git",
}

// execDenylist catches destructive patterns even when the binary is allowed.
// NOTE (audit C-01): This denylist is defense-in-depth ONLY — it cannot stop
// an attacker who has shell access or a Turing-complete interpreter (python,
// node, etc). The primary defense is the allowlist above which now excludes
// all shells and interpreters by default.
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
	// Pipe-to-interpreter and pipe-to-network-tools (data exfiltration + RCE)
	"| sh", "| bash", "|/bin/sh", "|/bin/bash",
	"| python", "| python3", "| node", "| perl", "| ruby", "| php",
	"| curl", "| wget", "| nc", "| ncat", "| ssh", "| scp",
	"| telnet", "| socat", "| openssl", "| awk ", "| tee ",
	// Command substitution patterns
	"$(sh", "$(bash", "$(python", "$(curl", "$(wget", "$(nc", "$(ssh",
	// Backtick command substitution
	"`sh", "`bash", "`python", "`curl", "`wget",
	// Block-device writes
	">/dev/sd", ">/dev/vd", ">/dev/nvm",
	// eval / exec
	"eval ", "exec ",
	// Reverse shell patterns
	"/dev/tcp/", "/dev/udp/",
	"& sh", "& bash", "; sh", "; bash", "&& sh", "&& bash", "|| sh", "|| bash",
}

func init() {
	// Allow extending the allowlist via env var.
	if extra := os.Getenv("CUBE_EXEC_ALLOWLIST"); extra != "" {
		execAllowlist = append(execAllowlist, splitAndTrim(extra)...)
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
// AUDIT FIX H-03: Rewritten to use net/netip for IPv6, IPv4-mapped IPv6,
// decimal/hex/octal encoding, and unspecified address detection. The old
// string-based parser missed [::1], 2130706433, 0x7f000001, [::ffff:127.0.0.1].
func isPrivateHost(host string) bool {
	// Cloud metadata endpoints — must be blocked
	if host == "169.254.169.254" || host == "metadata.google.internal" || host == "metadata" {
		return true
	}

	// localhost variants
	lower := strings.ToLower(host)
	if lower == "localhost" || strings.HasSuffix(lower, ".localhost") {
		return true
	}

	// Try parsing as a literal IP address (handles IPv4 dotted-decimal,
	// IPv6, IPv4-mapped IPv6, decimal, hex, and octal forms on most platforms).
	// net.ParseIP handles standard forms; for non-standard encodings we also
	// try parsing each dotted part as decimal/octal/hex.
	if ip := net.ParseIP(host); ip != nil {
		return isPrivateIP(ip)
	}

	// Strip brackets from IPv6 literal: [::1] → ::1
	if strings.HasPrefix(host, "[") && strings.HasSuffix(host, "]") {
		inner := host[1 : len(host)-1]
		if ip := net.ParseIP(inner); ip != nil {
			return isPrivateIP(ip)
		}
	}

	// Handle zone-indexed IPv6: fe80::1%eth0
	if idx := strings.LastIndex(host, "%"); idx > 0 {
		if ip := net.ParseIP(host[:idx]); ip != nil {
			return isPrivateIP(ip)
		}
	}

	return false
}

// isPrivateIP checks whether a parsed net.IP falls into a private/reserved range.
func isPrivateIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	// Loopback (127.0.0.0/8, ::1/128)
	if ip.IsLoopback() {
		return true
	}
	// Private (10/8, 172.16/12, 192.168/16, fc00::/7)
	if ip.IsPrivate() {
		return true
	}
	// Link-local (169.254/16, fe80::/10) — also covers cloud metadata
	if ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return true
	}
	// Unspecified (0.0.0.0, ::)
	if ip.IsUnspecified() {
		return true
	}
	// Multicast (224.0.0.0/4, ff00::/8)
	if ip.IsMulticast() {
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

// isCloudMetadataHost returns true for known cloud metadata endpoints.
// Unlike isPrivateHost, this ONLY blocks metadata IPs — safe for cluster
// nodes that legitimately use private ranges (RFC 1918).
func isCloudMetadataHost(host string) bool {
	host = strings.TrimSpace(host)
	// Strip port if present
	if strings.Contains(host, ":") {
		host = strings.SplitN(host, ":", 2)[0]
	}
	if host == "169.254.169.254" || host == "metadata.google.internal" || host == "metadata" {
		return true
	}
	// 169.254.0.0/16 link-local (includes cloud metadata on most platforms)
	parts := strings.Split(host, ".")
	if len(parts) == 4 {
		var o [4]int
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
			o[i] = n
		}
		if valid && o[0] == 169 && o[1] == 254 {
			return true
		}
	}
	return false
}

// validateHostPort validates that a string is a safe host:port pair.
// Blocks cloud metadata endpoints (C5). Allows private ranges for cluster nodes.
func validateHostPort(addr string) error {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return fmt.Errorf("address cannot be empty")
	}
	// Must contain at least one colon (host:port)
	if !strings.Contains(addr, ":") {
		return fmt.Errorf("address must be in host:port format")
	}
	host := strings.SplitN(addr, ":", 2)[0]
	if host == "" {
		return fmt.Errorf("host part cannot be empty")
	}
	if isCloudMetadataHost(host) {
		return fmt.Errorf("address points to cloud metadata endpoint '%s' — blocked for security", host)
	}
	// Reject embedded shell metacharacters in the full address
	if strings.ContainsAny(addr, "`$\\\"'{};|&<>()") {
		return fmt.Errorf("address contains forbidden characters")
	}
	return nil
}

// validateContainerID ensures a string is a safe container ID / image ID.
// Prevents argument injection into CLI commands (e.g. "--flag" as container ID).
// Container IDs from Docker are hex hashes; Cube uses alphanumeric IDs.
var containerIDRe = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_.-]{0,127}$`)

func validateContainerID(id string) error {
	if id == "" {
		return fmt.Errorf("container_id cannot be empty")
	}
	if strings.HasPrefix(id, "-") {
		return fmt.Errorf("container_id starts with '-' — possible argument injection attempt")
	}
	if !containerIDRe.MatchString(id) {
		return fmt.Errorf("invalid container_id '%s': must be alphanumeric with dots, dashes, or underscores (max 128 chars)", id)
	}
	return nil
}

// validateHostname is defined in hypervisor_validate.go (RFC 1123 subset).
// Reused for SSH target host validation in volumes.go.

// sensitiveMountPaths lists host paths that must never be used as a container
// mount path inside a container. Prevents container escape via path traversal.
var sensitiveMountPaths = []string{
	"/etc", "/etc/",
	"/var/run", "/var/run/", "/run", "/run/",
	"/proc", "/proc/", "/sys", "/sys/",
	"/dev", "/dev/",
	"/root", "/root/",
	"/boot", "/boot/",
	"/usr/lib", "/usr/lib64", "/usr/bin",
	"/.dockerenv",
	"/var/lib/docker", "/var/lib/docker/",
}

// validateMountPath ensures a mount path is safe — not a sensitive host path
// and doesn't escape via traversal (H8).
func validateMountPath(path string) error {
	if path == "" {
		return fmt.Errorf("mount_path cannot be empty")
	}
	// Reject path traversal
	if strings.Contains(path, "..") {
		return fmt.Errorf("mount_path contains '..' — path traversal not allowed")
	}
	// Normalize for comparison
	cleaned := filepath.Clean(path)
	for _, sensitive := range sensitiveMountPaths {
		if cleaned == sensitive || strings.HasPrefix(cleaned, sensitive) {
			return fmt.Errorf("mount_path '%s' is a sensitive system path — not allowed", path)
		}
	}
	// Must be absolute
	if !filepath.IsAbs(path) {
		return fmt.Errorf("mount_path must be an absolute path")
	}
	// Reject shell metacharacters
	if strings.ContainsAny(path, "`$\\\"'{};|&<>()") {
		return fmt.Errorf("mount_path contains forbidden characters")
	}
	return nil
}

// validateWebhookURL ensures a webhook URL points to a safe destination.
// Blocks private/internal hosts to prevent SSRF (H6).
func validateWebhookURL(url string) error {
	url = strings.TrimSpace(url)
	if url == "" {
		return fmt.Errorf("webhook URL cannot be empty")
	}
	if !strings.HasPrefix(url, "https://") && !strings.HasPrefix(url, "http://") {
		return fmt.Errorf("webhook URL must start with http:// or https://")
	}
	// Extract host
	var hostPart string
	if strings.HasPrefix(url, "https://") {
		hostPart = url[8:]
	} else {
		hostPart = url[7:]
	}
	// Strip path
	if idx := strings.Index(hostPart, "/"); idx >= 0 {
		hostPart = hostPart[:idx]
	}
	// Strip port
	host := strings.SplitN(hostPart, ":", 2)[0]
	// SSRF protection: block private/internal/cloud-metadata hosts
	if isPrivateHost(host) {
		return fmt.Errorf("webhook URL points to private/internal host '%s' — SSRF protection blocks this", host)
	}
	return nil
}
