// Package main: handler functions for the Phase 2 MCP tools (32 tools).
// These follow the exact same pattern as handlers in server.go:
// parseArgs → argString/argInt → manager call → okResult/errResult.
package main

import (
	"context"
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Image lifecycle (5) ----

func handleImageBuild(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	contextDir := argString(args, "context_dir")
	if contextDir == "" {
		return errResult("context_dir is required"), nil
	}
	tag := argString(args, "tag")
	if tag == "" {
		return errResult("tag is required"), nil
	}
	dockerfile := argString(args, "dockerfile")
	data, err := imageMgr.BuildImage(context.Background(), contextDir, dockerfile, tag)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleImagePush(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	tag := argString(args, "tag")
	if tag == "" {
		return errResult("tag is required"), nil
	}
	registry := argString(args, "registry")
	data, err := imageMgr.PushImage(context.Background(), tag, registry)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleImagePull(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	tag := argString(args, "tag")
	if tag == "" {
		return errResult("tag is required"), nil
	}
	data, err := imageMgr.PullImage(context.Background(), tag)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleImageList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	filter := argString(args, "filter")
	data, err := imageMgr.ListImages(context.Background(), filter)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleImageTag(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sourceTag := argString(args, "source_tag")
	if sourceTag == "" {
		return errResult("source_tag is required"), nil
	}
	targetTag := argString(args, "target_tag")
	if targetTag == "" {
		return errResult("target_tag is required"), nil
	}
	data, err := imageMgr.TagImage(context.Background(), sourceTag, targetTag)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Deploy rollout (1) ----

func handleDeployRollout(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	serviceName := argString(args, "service_name")
	if serviceName == "" {
		return errResult("service_name is required"), nil
	}
	newImage := argString(args, "new_image")
	if newImage == "" {
		return errResult("new_image is required"), nil
	}
	strategy := argString(args, "strategy")
	healthWait := argInt(args, "health_wait_seconds", 60)

	abortOnFailure := true
	if v, ok := args["abort_on_failure"]; ok {
		abortOnFailure = fmt.Sprintf("%v", v) != "false"
	}

	data, err := rolloutMgr.Rollout(context.Background(), serviceName, newImage, strategy, healthWait, abortOnFailure)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Log aggregation (2) ----

func handleLogsSearch(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	params := LogSearchParams{
		Pattern:      argString(args, "pattern"),
		Containers:   argStringSlice(args, "containers"),
		Level:        argString(args, "level"),
		SinceMinutes: argInt(args, "since_minutes", 0),
		MaxResults:   argInt(args, "max_results", 100),
	}
	data, err := logAggMgr.Search(params)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleLogsAggregate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerIDs := argStringSlice(args, "containers")
	sinceLines := argInt(args, "since_lines", 200)
	data, err := logAggMgr.Aggregate(containerIDs, sinceLines)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Audit query (1) ----

func handleAuditQuery(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	params := AuditQueryParams{
		SinceHours: argInt(args, "since_hours", 24),
		ToolName:   argString(args, "tool_name"),
		Role:       argString(args, "role"),
		Limit:      argInt(args, "limit", 100),
	}
	data, err := auditQueryMgr.Query(params)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Environments (4) ----

func handleEnvCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	description := argString(args, "description")
	data, err := envMgr.Create(name, description, false)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleEnvList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := envMgr.List()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleEnvGet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	data, err := envMgr.Get(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleEnvPromote(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	sourceEnv := argString(args, "source_env")
	if sourceEnv == "" {
		return errResult("source_env is required"), nil
	}
	targetEnv := argString(args, "target_env")
	if targetEnv == "" {
		return errResult("target_env is required"), nil
	}
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	data, err := envMgr.Promote(sourceEnv, targetEnv, containerID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Notifications (4) ----

func handleNotifyChannelAdd(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	ch := &NotificationChannel{
		Name:       argString(args, "name"),
		Type:       ChannelType(argString(args, "type")),
		WebhookURL: argString(args, "webhook_url"),
		BotToken:   argString(args, "bot_token"),
		ChatID:     argString(args, "chat_id"),
		EmailTo:    argString(args, "email_to"),
		SMTPHost:   argString(args, "smtp_host"),
		Enabled:    true,
	}
	if err := notifyMgr.AddChannel(ch); err != nil {
		return unwrapError(err), nil
	}
	return okResult(ch), nil
}

func handleNotifyChannelList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data := notifyMgr.ListChannels()
	return okResult(data), nil
}

func handleNotifyChannelRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	channelID := argString(args, "channel_id")
	if channelID == "" {
		return errResult("channel_id is required"), nil
	}
	if err := notifyMgr.RemoveChannel(channelID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"channel_id": channelID, "status": "removed"}), nil
}

func handleNotifySend(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	channelID := argString(args, "channel_id")
	if channelID == "" {
		return errResult("channel_id is required"), nil
	}
	title := argString(args, "title")
	if title == "" {
		return errResult("title is required"), nil
	}
	body := argString(args, "body")
	if body == "" {
		return errResult("body is required"), nil
	}
	level := argString(args, "level")
	if level == "" {
		level = "info"
	}
	data, err := notifyMgr.Send(channelID, NotificationMessage{Title: title, Body: body, Level: level})
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Auth tokens (3) ----

func handleAuthCreateToken(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	role := argString(args, "role")
	if role == "" {
		return errResult("role is required"), nil
	}
	label := argString(args, "label")
	data, err := createToken(role, label)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleAuthListTokens(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := listTokens()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleAuthRevokeToken(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	key := argString(args, "key")
	if key == "" {
		return errResult("key is required"), nil
	}
	data, err := revokeToken(key)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Scheduled jobs (4) ----

func handleJobCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	schedule := argString(args, "schedule")
	if schedule == "" {
		return errResult("schedule is required"), nil
	}
	toolName := argString(args, "tool")
	if toolName == "" {
		return errResult("tool is required"), nil
	}
	jobArgs := argMap(args, "args")
	if jobArgs == nil {
		jobArgs = map[string]interface{}{}
	}
	data, err := jobMgr.Create(name, schedule, toolName, jobArgs)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleJobList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := jobMgr.List()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleJobRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	jobID := argString(args, "job_id")
	if jobID == "" {
		return errResult("job_id is required"), nil
	}
	if err := jobMgr.Remove(jobID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"job_id": jobID, "status": "removed"}), nil
}

func handleJobRun(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	jobID := argString(args, "job_id")
	if jobID == "" {
		return errResult("job_id is required"), nil
	}
	data, err := jobMgr.RunNow(jobID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Metrics query (1) ----

func handleMetricsQuery(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	params := MetricsQueryParams{
		Metric:    argString(args, "metric"),
		Container: argString(args, "container"),
		Limit:     argInt(args, "limit", 50),
	}
	data, err := metricsQueryMgr.Query(params)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Database provisioning (3) ----

func handleDatabaseCreate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	params := DatabaseCreateParams{
		Name:     argString(args, "name"),
		Type:     DBType(argString(args, "type")),
		Version:  argString(args, "version"),
		MemoryMB: argInt(args, "memory_mb", 512),
	}
	if params.Name == "" {
		return errResult("name is required"), nil
	}
	if params.Type == "" {
		return errResult("type is required"), nil
	}
	data, err := dbMgr.Create(params)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDatabaseBackup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	dbID := argString(args, "database_id")
	if dbID == "" {
		return errResult("database_id is required"), nil
	}
	data, err := dbMgr.Backup(dbID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDatabaseRestore(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	dbID := argString(args, "database_id")
	if dbID == "" {
		return errResult("database_id is required"), nil
	}
	backupID := argString(args, "backup_id")
	if backupID == "" {
		return errResult("backup_id is required"), nil
	}
	data, err := dbMgr.Restore(dbID, backupID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Certificates (2) ----

func handleCertList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := certMgr.List()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCertRenew(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	domain := argString(args, "domain")
	if domain == "" {
		return errResult("domain is required"), nil
	}
	data, err := certMgr.Renew(domain)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Events (2) ----

func handleEventsList(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	data, err := eventMgr.List(
		argString(args, "event_type"),
		argString(args, "severity"),
		argInt(args, "since_minutes", 0),
		argInt(args, "limit", 50),
	)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleEventsRecent(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := eventMgr.List("", "", 0, 20)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}
