// Package main: basic tool handlers for cluster, containers, templates,
// deploy, volumes, and backup operations.
// Extracted from server.go for maintainability (AUDIT FIX L-02).
package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Tool handlers: Backend introspection ----

func handleBackendInfo(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	info := map[string]interface{}{
		"backend":  client.BackendName(),
		"endpoint": client.Endpoint(),
		"features": []string{
			"container_lifecycle", "templates", "deploy_from_git", "deploy_from_code",
			"volumes", "backup", "rollback", "routing_tls", "networking", "exec",
			"images", "rolling_deploy", "log_aggregation", "audit_trail",
			"environments", "notifications", "auth_tokens", "scheduled_jobs",
			"metrics_query", "database_provisioning", "certificates", "events",
		},
		"tool_count": 129,
	}
	return okResult(info), nil
}

// ---- Tool handlers: Cluster ----

func handleClusterHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.Health()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleClusterOverview(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ClusterOverview()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleClusterVersions(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ClusterVersions()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListNodes(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ListNodes()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetNode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	nodeID := argString(args, "node_id")
	if nodeID == "" {
		return errResult("node_id is required"), nil
	}
	data, err := client.GetNode(nodeID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Containers ----

func handleListContainers(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	state := argString(args, "state")
	limit := argInt(args, "limit", 50)
	data, err := client.ListSandboxes(state, limit)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.GetSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	tmplID := argString(args, "template_id")
	if tmplID == "" {
		return errResult("template_id is required"), nil
	}
	memMB := argInt(args, "memory_mb", 512)
	cpuCount := argFloat(args, "cpu_count", 1.0)
	envVars := argMap(args, "env_vars")
	metadata := argMap(args, "metadata")

	// Inject ConfigMap entries as environment variables
	if cmName := argString(args, "configmap"); cmName != "" {
		cmEnv, err := configMgr.asEnvVars(cmName)
		if err != nil {
			return errResult(fmt.Sprintf("configmap %s: %v", cmName, err)), nil
		}
		// ConfigMap values don't override explicit env_vars
		for k, v := range cmEnv {
			if _, exists := envVars[k]; !exists {
				envVars[k] = v
			}
		}
	}

	data, err := client.CreateSandbox(tmplID, memMB, cpuCount, envVars, metadata)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleKillContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.KillSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handlePauseContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.PauseSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleResumeContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.ResumeSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetContainerLogs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	limit := argInt(args, "limit", 100)
	data, err := client.GetSandboxLogs(id, limit)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Templates ----

func handleListTemplates(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ListTemplates()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateTemplate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	image := argString(args, "image")
	if image == "" {
		return errResult("image is required"), nil
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	layerGB := argInt(args, "writable_layer_size_gb", 1)
	data, err := client.CreateTemplateFromImage(image, ports, layerGB, nil, nil, "")
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetTemplate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "template_id")
	if id == "" {
		return errResult("template_id is required"), nil
	}
	data, err := client.GetTemplate(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Deploy ----

func handleDeployFromGit(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	gitURL := argString(args, "git_url")
	if gitURL == "" {
		return errResult("git_url is required"), nil
	}
	branch := argString(args, "branch")
	if branch == "" {
		branch = "main"
	}
	image := argString(args, "image")
	if image == "" {
		image = "python:3.12-slim"
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	envVars := argMap(args, "env_vars")
	startCmd := argString(args, "start_cmd")
	volumeName := argString(args, "volume_name")
	memMB := argInt(args, "memory_mb", 256)

	data, err := deploy.DeployFromGit(gitURL, branch, image, ports, envVars, startCmd, volumeName, memMB, 1.0)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeployFromCode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	appName := argString(args, "app_name")
	if appName == "" {
		return errResult("app_name is required"), nil
	}
	// files is a map of filename→content
	files := make(map[string]string)
	if rawFiles, ok := args["files"].(map[string]interface{}); ok {
		for k, v := range rawFiles {
			files[k] = fmt.Sprintf("%v", v)
		}
	}
	if len(files) == 0 {
		return errResult("files is required (and must not be empty)"), nil
	}
	image := argString(args, "image")
	if image == "" {
		image = "python:3.12-slim"
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	envVars := argMap(args, "env_vars")
	startCmd := argString(args, "start_cmd")
	memMB := argInt(args, "memory_mb", 256)

	data, err := deploy.DeployFromCode(appName, files, image, ports, envVars, startCmd, memMB)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleUpdateCode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	gitURL := argString(args, "git_url")
	if gitURL == "" {
		return errResult("git_url is required"), nil
	}
	branch := argString(args, "branch")
	if branch == "" {
		branch = "main"
	}

	data, err := deploy.UpdateCode(containerID, gitURL, branch)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleExecInContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	command := argString(args, "command")
	if command == "" {
		return errResult("command is required"), nil
	}
	// Validate command against allowlist + denylist
	if _, err := validateCommand(command); err != nil {
		return errResult(err.Error()), nil
	}
	// Cap timeout at 300s (AS-2 fix) — matches secure_sandbox_exec limit
	timeout := argInt(args, "timeout", 30)
	if timeout > 300 {
		timeout = 300
	}
	if timeout < 1 {
		timeout = 30
	}
	data, err := client.ExecInSandbox(containerID, command, timeout)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Volumes ----

func handleListVolumes(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := deploy.ListVolumes()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	data, err := deploy.CreateVolume(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeleteVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	data, err := deploy.DeleteVolume(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Backup & Restore ----

func handleBackupVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	volName := argString(args, "volume_name")
	if volName == "" {
		return errResult("volume_name is required"), nil
	}
	data, err := backupMgr.BackupVolume(volName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleBackupContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	data, err := backupMgr.BackupContainer(containerID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListBackups(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := backupMgr.ListBackups()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleRestoreBackup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	backupID := argString(args, "backup_id")
	if backupID == "" {
		return errResult("backup_id is required"), nil
	}
	data, err := backupMgr.RestoreBackup(backupID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeleteBackup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	backupID := argString(args, "backup_id")
	if backupID == "" {
		return errResult("backup_id is required"), nil
	}
	if err := backupMgr.DeleteBackup(backupID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"backup_id": backupID, "status": "deleted"}), nil
}
