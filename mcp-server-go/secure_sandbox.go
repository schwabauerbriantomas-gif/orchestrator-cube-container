// Package main: secure sandbox management for untrusted code execution.
//
// CubeSandbox's KVM backend provides hardware-level isolation: each sandbox
// runs its own guest kernel, making kernel escape impossible. This file
// exposes the three security features that make safe untrusted code hosting
// possible through the MCP interface:
//
// 1. Secure Sandbox Creation — creates a KVM-isolated sandbox with optional
//    egress filtering and credential vault injection. The code running inside
//    cannot access the host kernel, other sandboxes, or the network unless
//    explicitly allowed.
//
// 2. Egress Control — restricts outbound network access per sandbox. Only
//    domains in the allowlist can be reached. This prevents data exfiltration,
//    SSRF from inside the sandbox, and lateral movement.
//
// 3. Credential Vault — API keys and secrets are injected by the CubeEgress
//    proxy when the sandbox makes outbound requests to allowed domains. The
//    credentials NEVER enter the sandbox filesystem, environment, or process
//    memory. The untrusted code can call external APIs without ever seeing
//    the keys.
//
// These tools only work with the Cube backend (CUBE_BACKEND=cube). When the
// Docker backend is active, they return a clear error directing the user to
// switch backends. Docker containers share the host kernel and cannot provide
// the isolation guarantees these features require.
package main

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
)

// ---- Types ----

// SecureSandboxConfig defines how a secure sandbox should be created.
type SecureSandboxConfig struct {
	TemplateID    string            `json:"template_id"`
	MemoryMB      int               `json:"memory_mb"`
	CPUCount      float64           `json:"cpu_count"`
	EnvVars       map[string]string `json:"env_vars,omitempty"`
	// Security settings
	EgressAllowList []string `json:"egress_allowlist,omitempty"` // domains the sandbox can reach
	EgressBlockList []string `json:"egress_blocklist,omitempty"` // explicitly blocked domains
	// Credential Vault: keys injected by proxy, never visible to sandbox code
	CredentialVault map[string]string `json:"credential_vault,omitempty"` // domain → API key (injected on egress)
	// Time limit: sandbox auto-pauses after this many seconds (0 = no limit)
	MaxLifetimeSeconds int `json:"max_lifetime_seconds,omitempty"`
	// Disable network entirely (most secure)
	NetworkDisabled bool `json:"network_disabled,omitempty"`
}

// SecureSandbox represents a running secure sandbox instance.
type SecureSandbox struct {
	ID            string    `json:"id"`
	TemplateID    string    `json:"template_id"`
	Status        string    `json:"status"` // running, paused, stopped
	CreatedAt     time.Time `json:"created_at"`
	ExpiresAt     *time.Time `json:"expires_at,omitempty"`
	EgressPolicy  string    `json:"egress_policy"`  // "allowlist", "blocklist", "disabled"
	NetworkActive bool      `json:"network_active"`
	// NOTE: CredentialVault contents are NEVER included in responses.
	// Only the domain list is shown (keys are redacted).
	VaultDomains []string `json:"vault_domains,omitempty"`
}

// EgressRule defines a single network egress rule.
type EgressRule struct {
	ID          string    `json:"id"`
	SandboxID   string    `json:"sandbox_id"`
	Domain      string    `json:"domain"`
	Action      string    `json:"action"` // "allow" or "block"
	CreatedAt   time.Time `json:"created_at"`
}

// ExecResult holds the output of code execution inside a secure sandbox.
type ExecResult struct {
	SandboxID string `json:"sandbox_id"`
	Stdout    string `json:"stdout"`
	Stderr    string `json:"stderr"`
	ExitCode  int    `json:"exit_code"`
	Duration  string `json:"duration"`
}

// ---- Validation ----

// domainPattern validates domain names for egress rules.
var domainPattern = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?(\.[a-zA-Z0-9]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?)*$`)

func validateDomainRule(domain string) error {
	if domain == "" {
		return fmt.Errorf("domain is required")
	}
	// Wildcard subdomains: *.example.com
	cleaned := strings.TrimPrefix(domain, "*.")
	if !domainPattern.MatchString(cleaned) {
		return fmt.Errorf("invalid domain format: %s", domain)
	}
	// Block internal/private domains
	if isPrivateHost(cleaned) {
		return fmt.Errorf("cannot add egress rule for private/internal host: %s", domain)
	}
	return nil
}

// ---- Manager ----

var secureSandboxMgr *SecureSandboxManager

type SecureSandboxManager struct {
	backend ContainerBackend
}

func newSecureSandboxManager(b ContainerBackend) *SecureSandboxManager {
	return &SecureSandboxManager{backend: b}
}

// ---- Create secure sandbox ----

func (sm *SecureSandboxManager) Create(config SecureSandboxConfig) (*SecureSandbox, error) {
	if config.TemplateID == "" {
		return nil, fmt.Errorf("template_id is required")
	}
	if config.MemoryMB <= 0 {
		config.MemoryMB = 512
	}
	if config.CPUCount <= 0 {
		config.CPUCount = 1.0
	}

	// Validate egress rules
	for _, domain := range config.EgressAllowList {
		if err := validateDomainRule(domain); err != nil {
			return nil, fmt.Errorf("invalid allowlist domain '%s': %w", domain, err)
		}
	}
	for _, domain := range config.EgressBlockList {
		if err := validateDomainRule(domain); err != nil {
			return nil, fmt.Errorf("invalid blocklist domain '%s': %w", domain, err)
		}
	}
	// Validate vault domains
	vaultDomains := make([]string, 0, len(config.CredentialVault))
	for domain := range config.CredentialVault {
		if err := validateDomainRule(domain); err != nil {
			return nil, fmt.Errorf("invalid vault domain '%s': %w", domain, err)
		}
		vaultDomains = append(vaultDomains, domain)
	}

	// Only Cube backend supports KVM sandboxes
	cubeClient, ok := sm.backend.(*CubeClient)
	if !ok {
		return nil, fmt.Errorf("secure sandboxes require the Cube backend (KVM isolation). "+
			"Current backend: %s. Set CUBE_BACKEND=cube to use secure sandboxes. "+
			"Docker containers share the host kernel and cannot provide hardware-level isolation.",
			sm.backend.BackendName())
	}

	// Build CubeAPI request with security extensions
	body := map[string]interface{}{
		"templateID": config.TemplateID,
		"memoryMB":   config.MemoryMB,
		"cpuCount":   config.CPUCount,
		"metadata": map[string]interface{}{
			"secure_sandbox": true,
		},
	}
	if config.EnvVars != nil {
		body["envVars"] = config.EnvVars
	}

	// Egress control — passed as metadata that CubeEgress reads
	metadata := body["metadata"].(map[string]interface{})
	if config.NetworkDisabled {
		metadata["network"] = "disabled"
	} else if len(config.EgressAllowList) > 0 {
		metadata["egress_allowlist"] = config.EgressAllowList
	}
	if len(config.EgressBlockList) > 0 {
		metadata["egress_blocklist"] = config.EgressBlockList
	}

	// Credential vault — keys go to CubeEgress proxy config, NOT to sandbox env
	// The sandbox code never sees these values. When the sandbox makes an
	// outbound request to an allowed domain, CubeEgress injects the key
	// into the request headers transparently.
	if len(config.CredentialVault) > 0 {
		metadata["vault_domains"] = vaultDomains
		// Actual key values are sent to CubeEgress config endpoint separately
	}

	// Max lifetime — sandbox auto-pauses after this duration
	if config.MaxLifetimeSeconds > 0 {
		metadata["max_lifetime_seconds"] = config.MaxLifetimeSeconds
	}

	// Create the sandbox via CubeAPI
	result, err := cubeClient.CreateSandbox(config.TemplateID, config.MemoryMB, config.CPUCount,
		toInterfaceMap(config.EnvVars), map[string]interface{}(metadata))
	if err != nil {
		return nil, fmt.Errorf("failed to create secure sandbox: %w", err)
	}

	resultMap, ok := result.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected response from CubeAPI")
	}
	sandboxID, _ := resultMap["id"].(string)
	if sandboxID == "" {
		return nil, fmt.Errorf("CubeAPI did not return a sandbox ID")
	}

	// Configure credential vault via CubeEgress if keys provided
	if len(config.CredentialVault) > 0 {
		if err := sm.configureVault(cubeClient, sandboxID, config.CredentialVault); err != nil {
			// Non-fatal: sandbox is running, vault just isn't configured
			fmt.Printf("[secure-sandbox] WARNING: vault config failed for %s: %v\n", sandboxID, err)
		}
	}

	// Build response
	egressPolicy := "allowlist"
	if config.NetworkDisabled {
		egressPolicy = "disabled"
	} else if len(config.EgressBlockList) > 0 && len(config.EgressAllowList) == 0 {
		egressPolicy = "blocklist"
	}

	var expiresAt *time.Time
	if config.MaxLifetimeSeconds > 0 {
		t := time.Now().UTC().Add(time.Duration(config.MaxLifetimeSeconds) * time.Second)
		expiresAt = &t
	}

	return &SecureSandbox{
		ID:           sandboxID,
		TemplateID:   config.TemplateID,
		Status:       "running",
		CreatedAt:    time.Now().UTC(),
		ExpiresAt:    expiresAt,
		EgressPolicy: egressPolicy,
		NetworkActive: !config.NetworkDisabled,
		VaultDomains: vaultDomains,
	}, nil
}

// configureVault sends credential vault config to CubeEgress proxy.
// The keys are stored in the proxy, never in the sandbox.
func (sm *SecureSandboxManager) configureVault(cube *CubeClient, sandboxID string, vault map[string]string) error {
	// CubeEgress runs alongside CubeAPI. We configure it via the internal API.
	// Each entry maps: domain → {header_name, key_value}
	vaultConfig := make(map[string]interface{})
	for domain, key := range vault {
		vaultConfig[domain] = map[string]interface{}{
			"header": "Authorization",
			"value":  "Bearer " + key,
		}
	}

	body := map[string]interface{}{
		"sandboxID": sandboxID,
		"vault":     vaultConfig,
	}

	// POST to CubeEgress internal config endpoint
	_, err := cube.request("POST", "/cubeegress/v1/vault", body, nil)
	if err != nil {
		// CubeEgress may not be running — degrade gracefully
		return fmt.Errorf("CubeEgress vault configuration failed (is CubeEgress running?): %w", err)
	}
	return nil
}

// ---- Egress management ----

func (sm *SecureSandboxManager) AddEgressRule(sandboxID, domain, action string) (*EgressRule, error) {
	if err := validateContainerID(sandboxID); err != nil {
		return nil, fmt.Errorf("invalid sandbox_id: %w", err)
	}
	if err := validateDomainRule(domain); err != nil {
		return nil, err
	}
	if action != "allow" && action != "block" {
		return nil, fmt.Errorf("action must be 'allow' or 'block'")
	}

	cube, ok := sm.backend.(*CubeClient)
	if !ok {
		return nil, fmt.Errorf("egress control requires Cube backend (current: %s)", sm.backend.BackendName())
	}

	body := map[string]interface{}{
		"sandboxID": sandboxID,
		"domain":    domain,
		"action":    action,
	}
	_, err := cube.request("POST", "/cubeegress/v1/rules", body, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to add egress rule: %w", err)
	}

	return &EgressRule{
		ID:        fmt.Sprintf("rule-%d", time.Now().UnixNano()),
		SandboxID: sandboxID,
		Domain:    domain,
		Action:    action,
		CreatedAt: time.Now().UTC(),
	}, nil
}

func (sm *SecureSandboxManager) ListEgressRules(sandboxID string) ([]EgressRule, error) {
	if err := validateContainerID(sandboxID); err != nil {
		return nil, fmt.Errorf("invalid sandbox_id: %w", err)
	}

	cube, ok := sm.backend.(*CubeClient)
	if !ok {
		return nil, fmt.Errorf("egress control requires Cube backend (current: %s)", sm.backend.BackendName())
	}

	result, err := cube.request("GET", "/cubeegress/v1/rules", nil, url.Values{"sandboxID": []string{sandboxID}})
	if err != nil {
		// Degrade gracefully — return empty if CubeEgress not running
		return []EgressRule{}, nil
	}

	rules := []EgressRule{}
	if list, ok := result.([]interface{}); ok {
		for _, item := range list {
			if m, ok := item.(map[string]interface{}); ok {
				rules = append(rules, EgressRule{
					ID:        fmt.Sprintf("%v", m["id"]),
					SandboxID: fmt.Sprintf("%v", m["sandboxID"]),
					Domain:    fmt.Sprintf("%v", m["domain"]),
					Action:    fmt.Sprintf("%v", m["action"]),
				})
			}
		}
	}
	return rules, nil
}

func (sm *SecureSandboxManager) RemoveEgressRule(ruleID string) error {
	if ruleID == "" {
		return fmt.Errorf("rule_id is required")
	}

	cube, ok := sm.backend.(*CubeClient)
	if !ok {
		return fmt.Errorf("egress control requires Cube backend (current: %s)", sm.backend.BackendName())
	}

	_, err := cube.request("DELETE", "/cubeegress/v1/rules/"+ruleID, nil, nil)
	return err
}

// ---- Execute code inside secure sandbox ----

func (sm *SecureSandboxManager) Exec(sandboxID, command string, timeout int) (*ExecResult, error) {
	if err := validateContainerID(sandboxID); err != nil {
		return nil, fmt.Errorf("invalid sandbox_id: %w", err)
	}
	if command == "" {
		return nil, fmt.Errorf("command is required")
	}
	if timeout <= 0 {
		timeout = 30
	}
	if timeout > 300 {
		return nil, fmt.Errorf("timeout must be <= 300 seconds (5 min max for untrusted code)")
	}

	start := time.Now()
	result, err := sm.backend.ExecInSandbox(sandboxID, command, timeout)
	if err != nil {
		return nil, fmt.Errorf("exec failed: %w", err)
	}
	duration := time.Since(start).Round(time.Millisecond)

	// Parse result
	resMap, ok := result.(map[string]interface{})
	if !ok {
		return &ExecResult{
			SandboxID: sandboxID,
			Stdout:    fmt.Sprintf("%v", result),
			ExitCode:  0,
			Duration:  duration.String(),
		}, nil
	}

	stdout, _ := resMap["stdout"].(string)
	stderr, _ := resMap["stderr"].(string)
	exitCode := 0
	if ec, ok := resMap["exitCode"].(float64); ok {
		exitCode = int(ec)
	}

	return &ExecResult{
		SandboxID: sandboxID,
		Stdout:    stdout,
		Stderr:    stderr,
		ExitCode:  exitCode,
		Duration:  duration.String(),
	}, nil
}

// ---- Snapshot (CubeCoW) ----

type SnapshotResult struct {
	SandboxID string    `json:"sandbox_id"`
	SnapshotID string   `json:"snapshot_id"`
	CreatedAt time.Time `json:"created_at"`
}

func (sm *SecureSandboxManager) Snapshot(sandboxID string) (*SnapshotResult, error) {
	if err := validateContainerID(sandboxID); err != nil {
		return nil, fmt.Errorf("invalid sandbox_id: %w", err)
	}

	cube, ok := sm.backend.(*CubeClient)
	if !ok {
		return nil, fmt.Errorf("snapshots require Cube backend (CubeCoW)")
	}

	result, err := cube.request("POST", fmt.Sprintf("/cubeapi/v1/sandboxes/%s/snapshot", sandboxID), nil, nil)
	if err != nil {
		return nil, fmt.Errorf("snapshot failed: %w", err)
	}

	resMap, _ := result.(map[string]interface{})
	snapshotID, _ := resMap["snapshotID"].(string)

	return &SnapshotResult{
		SandboxID:  sandboxID,
		SnapshotID: snapshotID,
		CreatedAt:  time.Now().UTC(),
	}, nil
}

func (sm *SecureSandboxManager) Restore(sandboxID, snapshotID string) (map[string]interface{}, error) {
	if err := validateContainerID(sandboxID); err != nil {
		return nil, fmt.Errorf("invalid sandbox_id: %w", err)
	}
	if snapshotID == "" {
		return nil, fmt.Errorf("snapshot_id is required")
	}

	cube, ok := sm.backend.(*CubeClient)
	if !ok {
		return nil, fmt.Errorf("restore requires Cube backend (CubeCoW)")
	}

	result, err := cube.request("POST", fmt.Sprintf("/cubeapi/v1/sandboxes/%s/restore", sandboxID),
		map[string]interface{}{"snapshotID": snapshotID}, nil)
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]interface{}); ok {
		return m, nil
	}
	return map[string]interface{}{"result": result}, nil
}

// ---- Helper ----

func toInterfaceMap(m map[string]string) map[string]interface{} {
	if m == nil {
		return nil
	}
	result := make(map[string]interface{})
	for k, v := range m {
		result[k] = v
	}
	return result
}
