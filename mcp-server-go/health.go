// Package main: health checks and automatic container restart.
//
// Defines HTTP, TCP, and exec probes that the HealthManager runs periodically
// against running containers. When a container fails its probe consecutively
// beyond a threshold, the manager attempts an automatic restart via the
// ContainerBackend.RestartSandbox method.
//
// Health check definitions are persisted to disk as per-container JSON files
// (same pattern as RouteManager). The watcher loop runs as a background
// goroutine started from main().
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

// HealthCheckType identifies the probe mechanism.
type HealthCheckType string

const (
	HealthHTTP HealthCheckType = "http"
	HealthTCP  HealthCheckType = "tcp"
	HealthExec HealthCheckType = "exec"
)

// HealthCheck defines a single probe configuration for one container.
type HealthCheck struct {
	ContainerID      string           `json:"container_id"`
	Type             HealthCheckType  `json:"type"`
	IntervalSeconds  int              `json:"interval_seconds"`
	TimeoutSeconds   int              `json:"timeout_seconds"`
	FailureThreshold int              `json:"failure_threshold"`
	// HTTP probe fields
	Host         string `json:"host,omitempty"`
	HTTPPath     string `json:"http_path,omitempty"`
	HTTPPort     int    `json:"http_port,omitempty"`
	HTTPScheme   string `json:"http_scheme,omitempty"` // http or https
	// TCP probe fields
	TCPPort int `json:"tcp_port,omitempty"`
	// Exec probe fields
	ExecCommand string `json:"exec_command,omitempty"`

	// Runtime state (not set by user — updated by watcher)
	ConsecutiveFailures int       `json:"consecutive_failures"`
	LastChecked         time.Time `json:"last_checked,omitempty"`
	LastStatus          string    `json:"last_status"` // healthy, unhealthy, unknown
	LastError           string    `json:"last_error,omitempty"`
	RestartCount        int       `json:"restart_count"`
	Enabled             bool      `json:"enabled"`
	CreatedAt           time.Time `json:"created_at"`
}

// HealthSummary is the JSON view returned by the health_check_list tool.
type HealthSummary struct {
	ContainerID         string    `json:"container_id"`
	Type                string    `json:"type"`
	Status              string    `json:"status"`
	ConsecutiveFailures int       `json:"consecutive_failures"`
	FailureThreshold    int       `json:"failure_threshold"`
	RestartCount        int       `json:"restart_count"`
	LastChecked         string    `json:"last_checked"`
	Enabled             bool      `json:"enabled"`
}

// ---- Manager ----

// healthMgr is the process-wide health check coordinator.
var healthMgr *HealthManager

// HealthManager stores probe definitions, runs the watcher loop, and
// triggers restarts when containers fail.
type HealthManager struct {
	mu      sync.Mutex
	checks  map[string]*HealthCheck // keyed by container_id
	rootDir string                  // disk persistence
	backend ContainerBackend
	stopCh  chan struct{}
	running bool
}

// newHealthManager loads existing checks from disk and returns a ready manager.
// The caller must call Start() to begin the watcher loop.
func newHealthManager(b ContainerBackend) *HealthManager {
	hm := &HealthManager{
		checks:  make(map[string]*HealthCheck),
		rootDir: envOr("CUBE_HEALTH_ROOT", "/var/lib/cube-container/health"),
		backend: b,
		stopCh:  make(chan struct{}),
	}
	hm.loadFromDisk()
	return hm
}

// ---- Disk persistence ----

func (hm *HealthManager) checkFilePath(containerID string) string {
	safe := strings.NewReplacer("/", "_", "\\", "_").Replace(containerID)
	return filepath.Join(hm.rootDir, safe+".json")
}

// loadFromDisk reads all persisted health checks into memory.
func (hm *HealthManager) loadFromDisk() {
	entries, err := os.ReadDir(hm.rootDir)
	if err != nil {
		return // dir doesn't exist yet — fine
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(hm.rootDir, entry.Name()))
		if err != nil {
			continue
		}
		var hc HealthCheck
		if err := jsonDecode(data, &hc); err != nil {
			continue
		}
		hm.checks[hc.ContainerID] = &hc
	}
}

// saveCheck writes a single health check to disk.
func (hm *HealthManager) saveCheck(hc *HealthCheck) error {
	if err := os.MkdirAll(hm.rootDir, 0700); err != nil {
		return fmt.Errorf("create health root: %w", err)
	}
	data, err := jsonEncode(hc)
	if err != nil {
		return fmt.Errorf("marshal health check: %w", err)
	}
	return os.WriteFile(hm.checkFilePath(hc.ContainerID), data, 0600)
}

// deleteCheckFile removes a health check file from disk.
func (hm *HealthManager) deleteCheckFile(containerID string) {
	os.Remove(hm.checkFilePath(containerID))
}

// ---- CRUD ----

// setHealthCheck creates or updates a health check for a container.
func (hm *HealthManager) setHealthCheck(hc *HealthCheck) error {
	if hc.ContainerID == "" {
		return fmt.Errorf("container_id is required")
	}
	if hc.Type != HealthHTTP && hc.Type != HealthTCP && hc.Type != HealthExec {
		return fmt.Errorf("invalid type: %s (use http, tcp, or exec)", hc.Type)
	}
	if hc.IntervalSeconds < 5 {
		hc.IntervalSeconds = 30
	}
	if hc.TimeoutSeconds < 1 {
		hc.TimeoutSeconds = 5
	}
	if hc.FailureThreshold < 1 {
		hc.FailureThreshold = 3
	}
	if hc.Type == HealthHTTP {
		if hc.HTTPPort <= 0 || hc.HTTPPort > 65535 {
			return fmt.Errorf("http_port must be between 1 and 65535")
		}
		if hc.HTTPPath == "" {
			hc.HTTPPath = "/"
		}
		if hc.HTTPScheme == "" {
			hc.HTTPScheme = "http"
		}
		if hc.Host == "" {
			hc.Host = "localhost"
		}
		// SSRF prevention (H7): block cloud metadata and shell metachars
		if isCloudMetadataHost(hc.Host) {
			return fmt.Errorf("health probe host '%s' is a cloud metadata endpoint — blocked", hc.Host)
		}
	}
	if hc.Type == HealthTCP {
		if hc.TCPPort <= 0 || hc.TCPPort > 65535 {
			return fmt.Errorf("tcp_port must be between 1 and 65535")
		}
		if hc.Host == "" {
			hc.Host = "localhost"
		}
		// SSRF prevention (H7): block cloud metadata and shell metachars
		if isCloudMetadataHost(hc.Host) {
			return fmt.Errorf("health probe host '%s' is a cloud metadata endpoint — blocked", hc.Host)
		}
	}
	if hc.Type == HealthExec && hc.ExecCommand == "" {
		return fmt.Errorf("exec_command is required for exec probes")
	}

	hm.mu.Lock()
	defer hm.mu.Unlock()

	// Preserve runtime state if updating an existing check
	if existing, ok := hm.checks[hc.ContainerID]; ok {
		hc.ConsecutiveFailures = existing.ConsecutiveFailures
		hc.LastChecked = existing.LastChecked
		hc.LastStatus = existing.LastStatus
		hc.RestartCount = existing.RestartCount
		hc.CreatedAt = existing.CreatedAt
	} else {
		hc.CreatedAt = time.Now().UTC()
	}
	hc.Enabled = true
	hc.LastStatus = "unknown"

	hm.checks[hc.ContainerID] = hc
	return hm.saveCheck(hc)
}

// removeHealthCheck deletes a health check.
func (hm *HealthManager) removeHealthCheck(containerID string) error {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	if _, ok := hm.checks[containerID]; !ok {
		return fmt.Errorf("no health check found for container %s", containerID)
	}
	delete(hm.checks, containerID)
	hm.deleteCheckFile(containerID)
	return nil
}

// listHealthChecks returns a summary of all registered checks.
func (hm *HealthManager) listHealthChecks() []HealthSummary {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	out := make([]HealthSummary, 0, len(hm.checks))
	for _, hc := range hm.checks {
		last := ""
		if !hc.LastChecked.IsZero() {
			last = hc.LastChecked.UTC().Format(time.RFC3339)
		}
		out = append(out, HealthSummary{
			ContainerID:         hc.ContainerID,
			Type:                string(hc.Type),
			Status:              hc.LastStatus,
			ConsecutiveFailures: hc.ConsecutiveFailures,
			FailureThreshold:    hc.FailureThreshold,
			RestartCount:        hc.RestartCount,
			LastChecked:         last,
			Enabled:             hc.Enabled,
		})
	}
	return out
}

// getHealthStatus returns the full check detail for one container.
func (hm *HealthManager) getHealthStatus(containerID string) (*HealthCheck, error) {
	hm.mu.Lock()
	defer hm.mu.Unlock()

	hc, ok := hm.checks[containerID]
	if !ok {
		return nil, fmt.Errorf("no health check found for container %s", containerID)
	}
	return hc, nil
}

// ---- Watcher loop ----

// Start launches the background watcher goroutine. It is safe to call once.
func (hm *HealthManager) Start() {
	hm.mu.Lock()
	if hm.running {
		hm.mu.Unlock()
		return
	}
	hm.running = true
	hm.mu.Unlock()

	go hm.watch()
	fmt.Fprintf(os.Stderr, "[cube-mcp] health watcher started\n")
}

// Stop signals the watcher goroutine to exit.
func (hm *HealthManager) Stop() {
	hm.mu.Lock()
	defer hm.mu.Unlock()
	if hm.running {
		close(hm.stopCh)
		hm.running = false
	}
}

// watch is the main loop. It ticks every 5 seconds and runs any checks
// whose interval has elapsed.
func (hm *HealthManager) watch() {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-hm.stopCh:
			return
		case <-ticker.C:
			hm.runDueChecks()
		}
	}
}

// runDueChecks iterates all checks and runs probes for those past their interval.
func (hm *HealthManager) runDueChecks() {
	hm.mu.Lock()
	due := make([]*HealthCheck, 0)
	now := time.Now().UTC()
	for _, hc := range hm.checks {
		if !hc.Enabled {
			continue
		}
		if hc.LastChecked.IsZero() || now.Sub(hc.LastChecked) >= time.Duration(hc.IntervalSeconds)*time.Second {
			due = append(due, hc)
		}
	}
	hm.mu.Unlock()

	for _, hc := range due {
		hm.runProbe(hc)
	}
}

// runProbe executes a single probe and handles restart logic.
func (hm *HealthManager) runProbe(hc *HealthCheck) {
	healthy, err := hm.executeProbe(hc)

	hm.mu.Lock()
	hc.LastChecked = time.Now().UTC()
	if healthy {
		hc.LastStatus = "healthy"
		hc.LastError = ""
		hc.ConsecutiveFailures = 0
	} else {
		hc.LastStatus = "unhealthy"
		hc.ConsecutiveFailures++
		if err != nil {
			hc.LastError = err.Error()
		} else {
			hc.LastError = "probe failed"
		}
		_ = hm.saveCheck(hc) // persist failure state
		hm.mu.Unlock()

		// Check if we should restart
		if hc.ConsecutiveFailures >= hc.FailureThreshold {
			hm.attemptRestart(hc.ContainerID, hc.ConsecutiveFailures)
		}
		return
	}
	_ = hm.saveCheck(hc)
	hm.mu.Unlock()
}

// attemptRestart tries to restart the container via the backend.
func (hm *HealthManager) attemptRestart(containerID string, failures int) {
	fmt.Fprintf(os.Stderr, "[cube-mcp] health: container %s unhealthy (%d consecutive failures) — attempting restart\n",
		containerID, failures)

	_, err := hm.backend.RestartSandbox(containerID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] health: restart failed for %s: %v\n", containerID, err)
		// Update the check with the error
		hm.mu.Lock()
		if hc, ok := hm.checks[containerID]; ok {
			hc.LastError = fmt.Sprintf("restart failed: %v", err)
			_ = hm.saveCheck(hc)
		}
		hm.mu.Unlock()
		return
	}

	// Reset failure count after successful restart
	hm.mu.Lock()
	if hc, ok := hm.checks[containerID]; ok {
		hc.RestartCount++
		hc.ConsecutiveFailures = 0
		hc.LastStatus = "restarting"
		hc.LastError = ""
		_ = hm.saveCheck(hc)
	}
	hm.mu.Unlock()

	fmt.Fprintf(os.Stderr, "[cube-mcp] health: container %s restarted successfully\n", containerID)
}

// ---- Probe execution ----

func (hm *HealthManager) executeProbe(hc *HealthCheck) (bool, error) {
	switch hc.Type {
	case HealthHTTP:
		return hm.runHTTPProbe(hc)
	case HealthTCP:
		return hm.runTCPProbe(hc)
	case HealthExec:
		return hm.runExecProbe(hc)
	default:
		return false, fmt.Errorf("unknown probe type: %s", hc.Type)
	}
}

// runHTTPProbe performs an HTTP GET and considers 2xx/3xx as healthy.
func (hm *HealthManager) runHTTPProbe(hc *HealthCheck) (bool, error) {
	scheme := hc.HTTPScheme
	if scheme == "" {
		scheme = "http"
	}
	host := hc.Host
	if host == "" {
		host = "localhost"
	}
	url := fmt.Sprintf("%s://%s:%d%s", scheme, host, hc.HTTPPort, hc.HTTPPath)

	timeout := time.Duration(hc.TimeoutSeconds) * time.Second
	if timeout < 1*time.Second {
		timeout = 5 * time.Second
	}

	client := &http.Client{Timeout: timeout}
	resp, err := client.Get(url)
	if err != nil {
		return false, fmt.Errorf("http probe: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 400 {
		return true, nil
	}
	return false, fmt.Errorf("http probe: status %d", resp.StatusCode)
}

// runTCPProbe tries to establish a TCP connection. Success = healthy.
func (hm *HealthManager) runTCPProbe(hc *HealthCheck) (bool, error) {
	host := hc.Host
	if host == "" {
		host = "localhost"
	}
	addr := net.JoinHostPort(host, fmt.Sprintf("%d", hc.TCPPort))

	timeout := time.Duration(hc.TimeoutSeconds) * time.Second
	if timeout < 1*time.Second {
		timeout = 5 * time.Second
	}

	conn, err := net.DialTimeout("tcp", addr, timeout)
	if err != nil {
		return false, fmt.Errorf("tcp probe: %w", err)
	}
	conn.Close()
	return true, nil
}

// runExecProbe runs a command inside the container. Exit 0 = healthy.
func (hm *HealthManager) runExecProbe(hc *HealthCheck) (bool, error) {
	result, err := hm.backend.ExecInSandbox(hc.ContainerID, hc.ExecCommand, hc.TimeoutSeconds)
	if err != nil {
		return false, fmt.Errorf("exec probe: %w", err)
	}

	m := asMap(result)
	exitCode := toInt(mapGet(m, "exitCode"))
	if exitCode == 0 {
		return true, nil
	}
	return false, fmt.Errorf("exec probe: exit code %d", exitCode)
}

// ---- JSON helpers ----

func jsonEncode(v interface{}) ([]byte, error) {
	return json.MarshalIndent(v, "", "  ")
}

func jsonDecode(data []byte, v interface{}) error {
	return json.Unmarshal(data, v)
}

// ---- Tool handlers: Health checks ----

func handleHealthCheckSet(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)

	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}

	hc := &HealthCheck{
		ContainerID:      containerID,
		Type:             HealthCheckType(argString(args, "type")),
		IntervalSeconds:  argInt(args, "interval_seconds", 30),
		TimeoutSeconds:   argInt(args, "timeout_seconds", 5),
		FailureThreshold: argInt(args, "failure_threshold", 3),
		Host:             argString(args, "host"),
		HTTPPath:         argString(args, "http_path"),
		HTTPPort:         argInt(args, "http_port", 0),
		HTTPScheme:       argString(args, "http_scheme"),
		TCPPort:          argInt(args, "tcp_port", 0),
		ExecCommand:      argString(args, "exec_command"),
	}

	if err := healthMgr.setHealthCheck(hc); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"container_id":      hc.ContainerID,
		"type":              hc.Type,
		"interval_seconds":  hc.IntervalSeconds,
		"timeout_seconds":   hc.TimeoutSeconds,
		"failure_threshold": hc.FailureThreshold,
		"status":            "configured",
	}), nil
}

func handleHealthCheckRemove(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	if err := healthMgr.removeHealthCheck(containerID); err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(map[string]interface{}{
		"container_id": containerID,
		"status":       "removed",
	}), nil
}

func handleHealthCheckList(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	summaries := healthMgr.listHealthChecks()
	return okResult(summaries), nil
}

func handleHealthCheckStatus(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	hc, err := healthMgr.getHealthStatus(containerID)
	if err != nil {
		return errResult(err.Error()), nil
	}
	return okResult(hc), nil
}
