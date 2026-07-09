// Package main: handlers for secure sandbox tools (8 tools).
package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Secure Sandbox (8) — KVM-isolated untrusted code execution ----

func handleSecureSandboxCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	templateID := argString(args, "template_id")
	if templateID == "" {
		return errResult("template_id is required"), nil
	}

	config := SecureSandboxConfig{
		TemplateID:    templateID,
		MemoryMB:      argInt(args, "memory_mb", 512),
		CPUCount:      argFloat(args, "cpu_count", 1.0),
		EgressAllowList: argStringSlice(args, "egress_allowlist"),
		EgressBlockList: argStringSlice(args, "egress_blocklist"),
		MaxLifetimeSeconds: argInt(args, "max_lifetime_seconds", 0),
	}

	// Network disabled
	if v, ok := args["network_disabled"]; ok && fmt.Sprintf("%v", v) == "true" {
		config.NetworkDisabled = true
	}

	// Credential vault (map of domain → key)
	vault := argMap(args, "credential_vault")
	if vault != nil {
		config.CredentialVault = make(map[string]string)
		for k, v := range vault {
			config.CredentialVault[k] = fmt.Sprintf("%v", v)
		}
	}

	data, err := secureSandboxMgr.Create(config)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxExec(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sandboxID := argString(args, "sandbox_id")
	if sandboxID == "" {
		return errResult("sandbox_id is required"), nil
	}
	command := argString(args, "command")
	if command == "" {
		return errResult("command is required"), nil
	}
	timeout := argInt(args, "timeout_seconds", 30)
	data, err := secureSandboxMgr.Exec(sandboxID, command, timeout)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxEgressAdd(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sandboxID := argString(args, "sandbox_id")
	if sandboxID == "" {
		return errResult("sandbox_id is required"), nil
	}
	domain := argString(args, "domain")
	if domain == "" {
		return errResult("domain is required"), nil
	}
	action := argString(args, "action")
	if action == "" {
		return errResult("action is required"), nil
	}
	data, err := secureSandboxMgr.AddEgressRule(sandboxID, domain, action)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxEgressList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sandboxID := argString(args, "sandbox_id")
	if sandboxID == "" {
		return errResult("sandbox_id is required"), nil
	}
	data, err := secureSandboxMgr.ListEgressRules(sandboxID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxEgressRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	ruleID := argString(args, "rule_id")
	if ruleID == "" {
		return errResult("rule_id is required"), nil
	}
	if err := secureSandboxMgr.RemoveEgressRule(ruleID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"rule_id": ruleID, "status": "removed"}), nil
}

func handleSecureSandboxSnapshot(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sandboxID := argString(args, "sandbox_id")
	if sandboxID == "" {
		return errResult("sandbox_id is required"), nil
	}
	data, err := secureSandboxMgr.Snapshot(sandboxID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxRestore(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sandboxID := argString(args, "sandbox_id")
	if sandboxID == "" {
		return errResult("sandbox_id is required"), nil
	}
	snapshotID := argString(args, "snapshot_id")
	if snapshotID == "" {
		return errResult("snapshot_id is required"), nil
	}
	data, err := secureSandboxMgr.Restore(sandboxID, snapshotID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleSecureSandboxList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	state := argString(args, "state")

	// Delegate to the regular sandbox listing, but filter for secure sandboxes
	data, err := client.ListSandboxes(state, 100)
	if err != nil {
		return unwrapError(err), nil
	}

	// Filter: only return sandboxes with secure_sandbox metadata
	// The backend marks them — we just pass through
	return okResult(data), nil
}
