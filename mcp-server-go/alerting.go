// Package main: alerting rules and notification dispatch.
//
// Monitors cluster conditions and fires alerts when thresholds are crossed.
// Alert rules are user-configurable and persisted to disk. Notifications are
// sent to configured channels (webhook URL for Telegram/Slack/Discord).
//
// Rule types:
//   - container_down: container health check reports unhealthy or container not found
//   - cpu_high:       node CPU usage exceeds threshold for sustained period
//   - disk_high:      disk usage exceeds threshold
//   - mem_high:       memory usage exceeds threshold
//
// The alerting watcher runs as a background goroutine, checking every 30s.
// Each rule has a cooldown period to prevent alert storms.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// AlertSeverity classifies the urgency of an alert.
type AlertSeverity string

const (
	AlertCritical AlertSeverity = "critical"
	AlertWarning  AlertSeverity = "warning"
	AlertInfo     AlertSeverity = "info"
)

// AlertRuleType identifies what condition a rule watches.
type AlertRuleType string

const (
	AlertContainerDown AlertRuleType = "container_down"
	AlertCPUHigh       AlertRuleType = "cpu_high"
	AlertDiskHigh      AlertRuleType = "disk_high"
	AlertMemHigh       AlertRuleType = "mem_high"
)

// AlertRule defines a monitoring condition.
type AlertRule struct {
	ID          string         `json:"id"`
	Name        string         `json:"name"`
	Type        AlertRuleType  `json:"type"`
	Severity    AlertSeverity  `json:"severity"`
	Threshold   float64        `json:"threshold"`     // e.g. 80 for 80%
	ContainerID string         `json:"container_id"`  // for container_down
	NodeID      string         `json:"node_id"`       // for cpu/disk/mem (optional)
	WebhookURL  string         `json:"webhook_url"`   // override global webhook
	CooldownSec int            `json:"cooldown_sec"`  // min seconds between repeats
	Enabled     bool           `json:"enabled"`
	CreatedAt   time.Time      `json:"created_at"`

	// Runtime state
	LastFired time.Time `json:"last_fired,omitempty"`
	FireCount int       `json:"fire_count"`
}

// AlertEvent is the payload sent to notification channels.
type AlertEvent struct {
	RuleID    string        `json:"rule_id"`
	RuleName  string        `json:"rule_name"`
	Type      string        `json:"type"`
	Severity  string        `json:"severity"`
	Message   string        `json:"message"`
	Value     float64       `json:"value,omitempty"`
	Threshold float64       `json:"threshold,omitempty"`
	Node      string        `json:"node,omitempty"`
	FiredAt   string        `json:"fired_at"`
}

// AlertSummary is the list view.
type AlertSummary struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Type      string `json:"type"`
	Severity  string `json:"severity"`
	Enabled   bool   `json:"enabled"`
	FireCount int    `json:"fire_count"`
	LastFired string `json:"last_fired"`
}

// ---- Manager ----

var alertMgr *AlertManager

// AlertManager stores rules, evaluates them, and dispatches notifications.
type AlertManager struct {
	mu         sync.Mutex
	rules      map[string]*AlertRule
	rootDir    string
	webhookURL string // global default (CUBE_ALERT_WEBHOOK)
	stopCh     chan struct{}
	running    bool
}

func newAlertManager() *AlertManager {
	am := &AlertManager{
		rules:      make(map[string]*AlertRule),
		rootDir:    envOr("CUBE_ALERT_ROOT", "/var/lib/cube-container/alerts"),
		webhookURL: os.Getenv("CUBE_ALERT_WEBHOOK"),
		stopCh:     make(chan struct{}),
	}
	am.loadFromDisk()
	return am
}

// ---- Disk persistence ----

func (am *AlertManager) ruleFilePath(id string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(id)
	return filepath.Join(am.rootDir, safe+".json")
}

func (am *AlertManager) loadFromDisk() {
	entries, err := os.ReadDir(am.rootDir)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(am.rootDir, entry.Name()))
		if err != nil {
			continue
		}
		var rule AlertRule
		if err := json.Unmarshal(data, &rule); err != nil {
			continue
		}
		am.rules[rule.ID] = &rule
	}
}

func (am *AlertManager) saveRule(rule *AlertRule) error {
	if err := os.MkdirAll(am.rootDir, 0700); err != nil {
		return fmt.Errorf("create alerts root: %w", err)
	}
	data, err := json.MarshalIndent(rule, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal rule: %w", err)
	}
	return os.WriteFile(am.ruleFilePath(rule.ID), data, 0600)
}

func (am *AlertManager) deleteRuleFile(id string) {
	os.Remove(am.ruleFilePath(id))
}

// ---- CRUD ----

func (am *AlertManager) addRule(rule *AlertRule) error {
	if rule.ID == "" {
		return fmt.Errorf("id is required")
	}
	if rule.Name == "" {
		return fmt.Errorf("name is required")
	}
	if rule.CooldownSec < 30 {
		rule.CooldownSec = 300 // 5 min default
	}
	if rule.Severity == "" {
		rule.Severity = AlertWarning
	}
	rule.Enabled = true
	rule.CreatedAt = time.Now().UTC()

	am.mu.Lock()
	defer am.mu.Unlock()

	if _, exists := am.rules[rule.ID]; exists {
		return fmt.Errorf("rule %s already exists", rule.ID)
	}
	am.rules[rule.ID] = rule
	return am.saveRule(rule)
}

func (am *AlertManager) removeRule(id string) error {
	am.mu.Lock()
	defer am.mu.Unlock()

	if _, ok := am.rules[id]; !ok {
		return fmt.Errorf("rule %s not found", id)
	}
	delete(am.rules, id)
	am.deleteRuleFile(id)
	return nil
}

func (am *AlertManager) listRules() []AlertSummary {
	am.mu.Lock()
	defer am.mu.Unlock()

	out := make([]AlertSummary, 0, len(am.rules))
	for _, r := range am.rules {
		last := ""
		if !r.LastFired.IsZero() {
			last = r.LastFired.UTC().Format(time.RFC3339)
		}
		out = append(out, AlertSummary{
			ID:        r.ID,
			Name:      r.Name,
			Type:      string(r.Type),
			Severity:  string(r.Severity),
			Enabled:   r.Enabled,
			FireCount: r.FireCount,
			LastFired: last,
		})
	}
	return out
}

func (am *AlertManager) testRule(id string) (*AlertEvent, error) {
	am.mu.Lock()
	rule, ok := am.rules[id]
	am.mu.Unlock()
	if !ok {
		return nil, fmt.Errorf("rule %s not found", id)
	}

	event := &AlertEvent{
		RuleID:    rule.ID,
		RuleName:  rule.Name,
		Type:      string(rule.Type),
		Severity:  string(rule.Severity),
		Message:   fmt.Sprintf("TEST ALERT: rule '%s' fired manually", rule.Name),
		FiredAt:   time.Now().UTC().Format(time.RFC3339),
	}

	if err := am.dispatch(event, rule); err != nil {
		return event, fmt.Errorf("dispatch failed: %w", err)
	}
	return event, nil
}

// ---- Watcher loop ----

func (am *AlertManager) Start() {
	am.mu.Lock()
	if am.running {
		am.mu.Unlock()
		return
	}
	am.running = true
	am.mu.Unlock()

	go am.watch()
	fmt.Fprintf(os.Stderr, "[cube-mcp] alert watcher started\n")
}

func (am *AlertManager) watch() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-am.stopCh:
			return
		case <-ticker.C:
			am.evaluateRules()
		}
	}
}

func (am *AlertManager) evaluateRules() {
	am.mu.Lock()
	due := make([]*AlertRule, 0)
	now := time.Now().UTC()
	for _, r := range am.rules {
		if !r.Enabled {
			continue
		}
		// Respect cooldown
		if !r.LastFired.IsZero() && now.Sub(r.LastFired) < time.Duration(r.CooldownSec)*time.Second {
			continue
		}
		due = append(due, r)
	}
	am.mu.Unlock()

	for _, r := range due {
		am.evaluateRule(r)
	}
}

func (am *AlertManager) evaluateRule(r *AlertRule) {
	// For container_down: check health status if available
	if r.Type == AlertContainerDown && r.ContainerID != "" {
		if healthMgr != nil {
			hc, err := healthMgr.getHealthStatus(r.ContainerID)
			if err != nil {
				// Container not found or no health check — alert
				am.fireAlert(r, "container not found or no health check", 1.0)
				return
			}
			if hc.LastStatus == "unhealthy" {
				am.fireAlert(r, fmt.Sprintf("container %s is unhealthy: %s", r.ContainerID, hc.LastError), float64(hc.ConsecutiveFailures))
			}
		}
		return
	}

	// For cpu/disk/mem: we'd need system metrics. For now, these require
	// external metric collection (Prometheus node_exporter) to feed values.
	// The alerting infrastructure is ready — evaluation is a stub until
	// metrics are wired in.
}

func (am *AlertManager) fireAlert(r *AlertRule, message string, value float64) {
	event := &AlertEvent{
		RuleID:    r.ID,
		RuleName:  r.Name,
		Type:      string(r.Type),
		Severity:  string(r.Severity),
		Message:   message,
		Value:     value,
		Threshold: r.Threshold,
		Node:      r.NodeID,
		FiredAt:   time.Now().UTC().Format(time.RFC3339),
	}

	if err := am.dispatch(event, r); err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] alert: dispatch failed for %s: %v\n", r.ID, err)
	}

	am.mu.Lock()
	r.LastFired = time.Now().UTC()
	r.FireCount++
	_ = am.saveRule(r)
	am.mu.Unlock()

	fmt.Fprintf(os.Stderr, "[cube-mcp] alert FIRED: %s [%s] — %s\n", r.Name, r.Severity, message)
}

// ---- Notification dispatch ----

func (am *AlertManager) dispatch(event *AlertEvent, rule *AlertRule) error {
	url := rule.WebhookURL
	if url == "" {
		url = am.webhookURL
	}
	if url == "" {
		// No webhook configured — log to stderr only
		fmt.Fprintf(os.Stderr, "[cube-mcp] ALERT (no webhook): %+v\n", event)
		return nil
	}
	// SSRF prevention (H6): validate webhook URL before connecting
	if err := validateWebhookURL(url); err != nil {
		return fmt.Errorf("webhook URL rejected: %w", err)
	}

	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Post(url, "application/json", bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("webhook POST: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 400 {
		return fmt.Errorf("webhook returned %d", resp.StatusCode)
	}
	return nil
}

// ---- Tool handlers: Alerting ----

func handleAlertRuleAdd(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)

	rule := &AlertRule{
		ID:          argString(args, "id"),
		Name:        argString(args, "name"),
		Type:        AlertRuleType(argString(args, "type")),
		Severity:    AlertSeverity(argString(args, "severity")),
		Threshold:   argFloat(args, "threshold", 0),
		ContainerID: argString(args, "container_id"),
		NodeID:      argString(args, "node_id"),
		WebhookURL:  argString(args, "webhook_url"),
		CooldownSec: argInt(args, "cooldown_sec", 300),
	}

	if err := alertMgr.addRule(rule); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"id":      rule.ID,
		"name":    rule.Name,
		"type":    rule.Type,
		"status":  "configured",
	}), nil
}

func handleAlertRuleRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "id")
	if id == "" {
		return errResult("id is required"), nil
	}
	if err := alertMgr.removeRule(id); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{"id": id, "status": "removed"}), nil
}

func handleAlertList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	return okResult(alertMgr.listRules()), nil
}

func handleAlertTest(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "id")
	if id == "" {
		return errResult("id is required"), nil
	}
	event, err := alertMgr.testRule(id)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(event), nil
}
