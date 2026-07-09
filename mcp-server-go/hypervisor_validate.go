// Package main: input validation for hypervisor tools.
//
// Centralized validators for VM names, snapshot names, PCI addresses,
// ZFS dataset names, hostnames, and file paths. Every user-controlled
// string that reaches virsh, zfs, qemu-img, or a generated config file
// MUST pass through these validators first.
//
// Rules:
//   - VM names: alphanumeric + hyphen + underscore + dot only (DNS-safe subset)
//   - PCI addresses: strict hex:hex.hex format
//   - ZFS dataset names: alphanumeric + / + @ + _ + - only
//   - Hostnames: RFC 1123 subset (alphanumeric + hyphen)
//   - All reject: shell metacharacters, path traversal, XML/YAML injection chars
package main

import (
	"fmt"
	"net"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
)

// ---- Validators ----

// validateVMName ensures a VM/domain name is safe for virsh, file paths, and XML.
// Allows: a-z A-Z 0-9 - _ .
// Rejects: shell metacharacters, path separators, XML special chars, leading dash.
func validateVMName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 64 {
		return fmt.Errorf("name too long (max 64 chars)")
	}
	if name[0] == '-' {
		return fmt.Errorf("name cannot start with '-'")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.') {
			return fmt.Errorf("name contains invalid character %q (allowed: alphanumeric, -, _, .)", c)
		}
	}
	return nil
}

// validateSnapshotName validates snapshot names for both libvirt and ZFS.
// Same charset as VM names.
func validateSnapshotName(name string) error {
	return validateVMName(name) // same rules apply
}

// validatePCIAddress ensures a PCI address matches hex:hex.hex or hex:hex:hex.hex format.
// Examples valid: "01:00.0", "0000:01:00.0", "10:1a.3"
var rePCIAddress = regexp.MustCompile(`^[0-9A-Fa-f]{2,4}:[0-9A-Fa-f]{2}\.[0-9A-Fa-f]$|^[0-9A-Fa-f]{4}:[0-9A-Fa-f]{2}:[0-9A-Fa-f]{2}\.[0-9A-Fa-f]$`)

func validatePCIAddress(addr string) error {
	if addr == "" {
		return fmt.Errorf("pci_address is required")
	}
	if !rePCIAddress.MatchString(addr) {
		return fmt.Errorf("invalid PCI address format (expected HH:HH.H or HHHH:HH.H)")
	}
	return nil
}

// validateZFSDatasetName validates ZFS pool/dataset/snapshot names.
// Allows: alphanumeric, /, @, _, -, .
// Rejects: shell metacharacters, spaces, path traversal
func validateZFSDatasetName(name string) error {
	if name == "" {
		return fmt.Errorf("name is required")
	}
	if len(name) > 256 {
		return fmt.Errorf("name too long (max 256 chars)")
	}
	if strings.HasPrefix(name, "-") {
		return fmt.Errorf("name cannot start with '-'")
	}
	for _, c := range name {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '/' || c == '@' || c == '_' || c == '-' || c == '.') {
			return fmt.Errorf("name contains invalid character %q", c)
		}
	}
	// Reject path traversal sequences
	if strings.Contains(name, "..") {
		return fmt.Errorf("name cannot contain '..'")
	}
	return nil
}

// validateHostname validates a cloud-init hostname (RFC 1123 subset).
// Allows: alphanumeric and hyphen. Must not start/end with hyphen.
func validateHostname(hostname string) error {
	if hostname == "" {
		return fmt.Errorf("hostname is required")
	}
	if len(hostname) > 253 {
		return fmt.Errorf("hostname too long (max 253 chars)")
	}
	for _, c := range hostname {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-') {
			return fmt.Errorf("hostname contains invalid character %q (allowed: alphanumeric, -)", c)
		}
	}
	if hostname[0] == '-' || hostname[len(hostname)-1] == '-' {
		return fmt.Errorf("hostname cannot start or end with '-'")
	}
	return nil
}

// validateCloudInitUsername validates a username for cloud-init user-data.
// Allows: alphanumeric and underscore.
func validateCloudInitUsername(username string) error {
	if username == "" {
		return fmt.Errorf("username is required")
	}
	if len(username) > 32 {
		return fmt.Errorf("username too long (max 32 chars)")
	}
	for _, c := range username {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '_') {
			return fmt.Errorf("username contains invalid character %q (allowed: alphanumeric, _)", c)
		}
	}
	if username[0] >= '0' && username[0] <= '9' {
		return fmt.Errorf("username cannot start with a digit")
	}
	return nil
}

// validateDestHost validates a migration destination host.
// Allows: hostname, IPv4, IPv6 (in brackets).
func validateDestHost(host string) error {
	if host == "" {
		return fmt.Errorf("dest_host is required")
	}
	// Try IP parse first
	if net.ParseIP(host) != nil {
		return nil
	}
	// Hostname validation
	if len(host) > 253 {
		return fmt.Errorf("dest_host too long")
	}
	for _, c := range host {
		if !((c >= 'a' && c <= 'z') ||
			(c >= 'A' && c <= 'Z') ||
			(c >= '0' && c <= '9') ||
			c == '-' || c == '.') {
			return fmt.Errorf("dest_host contains invalid character %q", c)
		}
	}
	return nil
}

// validateFilePath ensures a file path is absolute and within an allowed directory.
// Prevents path traversal attacks.
func validateFilePath(path string, allowedDirs ...string) error {
	if path == "" {
		return fmt.Errorf("path is required")
	}
	if !filepath.IsAbs(path) {
		return fmt.Errorf("path must be absolute")
	}
	// Clean the path to resolve any ../ sequences
	cleaned := filepath.Clean(path)
	if cleaned != path {
		// Path contained traversal sequences
		return fmt.Errorf("path contains traversal sequences")
	}
	// Check against allowed directories
	if len(allowedDirs) > 0 {
		allowed := false
		for _, dir := range allowedDirs {
			if strings.HasPrefix(cleaned, dir+"/") || cleaned == dir {
				allowed = true
				break
			}
		}
		if !allowed {
			return fmt.Errorf("path must be within: %s", strings.Join(allowedDirs, ", "))
		}
	}
	return nil
}

// ---- Resource limit constants ----

const (
	maxVCPUPerVM     = 64
	maxMemoryMBPerVM = 262144 // 256GB
	maxDiskGBPerVM   = 8192   // 8TB
)

// ---- R8 audit validators ----

// validateDevicePath validates a block device path for zpool create.
// Must be /dev/-prefixed, no shell metacharacters, no path traversal.
var reDevicePath = regexp.MustCompile(`^/dev/(sd[a-z]+|nvme[0-9]+n[0-9]+|vd[a-z]+|loop[0-9]+|disk[0-9]+|md[0-9]+)(p[0-9]+)?$`)

func validateDevicePath(devices string) error {
	if devices == "" {
		return fmt.Errorf("devices is required")
	}
	for _, dev := range strings.Fields(devices) {
		if strings.HasPrefix(dev, "-") {
			return fmt.Errorf("device path cannot start with '-' (argument injection)")
		}
		if !reDevicePath.MatchString(dev) {
			return fmt.Errorf("invalid device path %q (must match /dev/sdX, /dev/nvmeXnY, etc.)", dev)
		}
	}
	return nil
}

// validateZFSCompression validates ZFS compression property value.
var allowedCompression = map[string]bool{
	"on": true, "off": true, "lz4": true, "gzip": true,
	"zstd": true, "lzjb": true,
}

func validateZFSCompression(val string) error {
	if val == "" {
		return nil
	}
	if !allowedCompression[strings.ToLower(val)] {
		return fmt.Errorf("invalid compression value %q (allowed: on, off, lz4, gzip, zstd, lzjb)", val)
	}
	return nil
}

// validateZFSRecordSize validates ZFS recordsize property (power of 2, 512–1M).
func validateZFSRecordSize(val string) error {
	if val == "" {
		return nil
	}
	n, err := strconv.Atoi(val)
	if err != nil {
		return fmt.Errorf("recordsize must be numeric")
	}
	if n < 512 || n > 1048576 {
		return fmt.Errorf("recordsize must be between 512 and 1048576")
	}
	// Check power of 2
	if n&(n-1) != 0 {
		return fmt.Errorf("recordsize must be a power of 2")
	}
	return nil
}

// validateFilePathOrEmpty validates a file path but allows empty (optional fields).
func validateFilePathOrEmpty(path string, allowedDirs ...string) error {
	if path == "" {
		return nil
	}
	return validateFilePath(path, allowedDirs...)
}

// validateNetworkName validates a libvirt network name for domain XML.
// Same charset as VM name.
func validateNetworkName(name string) error {
	if name == "" {
		return nil // default is applied by caller
	}
	return validateVMName(name)
}

// validatePackageName validates a package name for cloud-init (apt/dnf safe).
var rePackageName = regexp.MustCompile(`^[a-zA-Z0-9._:+-]+$`)

func validatePackageName(pkg string) error {
	if !rePackageName.MatchString(pkg) {
		return fmt.Errorf("invalid package name %q (allowed: alphanumeric, ., _, :, +, -)", pkg)
	}
	return nil
}

// validateSSHKey validates an SSH public key format.
var reSSHKey = regexp.MustCompile(`^(ssh-rsa|ssh-ed25519|ssh-dss|ecdsa-sha2-nistp(256|384|521)|sk-ssh-ed25519@openssh\.com|sk-ecdsa-sha2-nistp256@openssh\.com)\s+[A-Za-z0-9+/=]+(\s+.+)?$`)

func validateSSHKey(key string) error {
	if !reSSHKey.MatchString(strings.TrimSpace(key)) {
		return fmt.Errorf("invalid SSH key format")
	}
	return nil
}

// xmlEscape escapes XML special characters for safe interpolation in text/template.
func xmlEscape(s string) string {
	s = strings.ReplaceAll(s, "&", "&amp;")
	s = strings.ReplaceAll(s, "<", "&lt;")
	s = strings.ReplaceAll(s, ">", "&gt;")
	s = strings.ReplaceAll(s, "'", "&apos;")
	s = strings.ReplaceAll(s, "\"", "&quot;")
	return s
}
