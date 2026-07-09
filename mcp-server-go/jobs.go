// Package main: scheduled job execution.
//
// Jobs are periodic tasks that run MCP tools on a schedule. They use cron-like
// expressions or simple intervals. Examples:
//   - Backup a volume every night at 2 AM
//   - Prune images weekly
//   - Send a daily cluster health report
//   - Run a health check script every 5 minutes
//
// Jobs are persisted to disk and survive restarts. The scheduler runs in a
// background goroutine started from main().
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Types ----

type Job struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Schedule    string            `json:"schedule"`    // "every 5m", "every 1h", "hourly", "daily", or cron-like "* * * * *"
	Tool        string            `json:"tool"`        // MCP tool to call
	Args        map[string]interface{} `json:"args,omitempty"` // arguments for the tool
	Enabled     bool              `json:"enabled"`
	CreatedAt   time.Time         `json:"created_at"`
	LastRun     time.Time         `json:"last_run,omitempty"`
	NextRun     time.Time         `json:"next_run,omitempty"`
	RunCount    int               `json:"run_count"`
	LastStatus  string            `json:"last_status,omitempty"` // success, error
	LastError   string            `json:"last_error,omitempty"`
}

type JobListResult struct {
	Jobs  []Job `json:"jobs"`
	Total int   `json:"total"`
}

// ---- Manager ----

var jobMgr *JobManager

type JobManager struct {
	mu      sync.Mutex
	jobs    map[string]*Job
	rootDir string
	stopCh  chan struct{}
	running bool
}

func newJobManager() *JobManager {
	jm := &JobManager{
		jobs:    make(map[string]*Job),
		rootDir: envOr("CUBE_JOBS_ROOT", "/var/lib/cube-container/jobs"),
		stopCh:  make(chan struct{}),
	}
	jm.loadFromDisk()
	return jm
}

// ---- Disk persistence ----

func (jm *JobManager) jobFilePath() string {
	return filepath.Join(jm.rootDir, "jobs.json")
}

func (jm *JobManager) loadFromDisk() {
	data, err := os.ReadFile(jm.jobFilePath())
	if err != nil {
		return
	}
	var jobs []Job
	if err := json.Unmarshal(data, &jobs); err != nil {
		return
	}
	for i := range jobs {
		jm.jobs[jobs[i].ID] = &jobs[i]
	}
}

func (jm *JobManager) saveToDisk() error {
	if err := os.MkdirAll(jm.rootDir, 0700); err != nil {
		return err
	}
	jobs := make([]Job, 0, len(jm.jobs))
	for _, j := range jm.jobs {
		jobs = append(jobs, *j)
	}
	data, err := json.MarshalIndent(jobs, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(jm.jobFilePath(), data, 0600)
}

// ---- Operations ----

func (jm *JobManager) Create(name, schedule, toolName string, args map[string]interface{}) (*Job, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if name == "" {
		return nil, fmt.Errorf("job name is required")
	}
	if schedule == "" {
		return nil, fmt.Errorf("schedule is required")
	}
	if toolName == "" {
		return nil, fmt.Errorf("tool name is required")
	}

	// Validate schedule
	interval, err := parseSchedule(schedule)
	if err != nil {
		return nil, fmt.Errorf("invalid schedule: %w", err)
	}

	// Validate tool exists
	if _, ok := toolPermissions[toolName]; !ok {
		return nil, fmt.Errorf("unknown tool: %s", toolName)
	}
	// M9 fix: block privileged tools from being scheduled — prevents escalation
	// via job_create(job, tool=auth_revoke_token) or self-deletion via auth_create_token.
	if strings.HasPrefix(toolName, "auth_") || toolName == "job_create" || toolName == "job_remove" {
		return nil, fmt.Errorf("tool '%s' cannot be scheduled as a job for security reasons", toolName)
	}

	id := generateID("job")
	job := &Job{
		ID:        id,
		Name:      name,
		Schedule:  schedule,
		Tool:      toolName,
		Args:      args,
		Enabled:   true,
		CreatedAt: time.Now().UTC(),
		NextRun:   time.Now().UTC().Add(interval),
	}
	jm.jobs[id] = job
	if err := jm.saveToDisk(); err != nil {
		delete(jm.jobs, id)
		return nil, fmt.Errorf("failed to persist job: %w", err)
	}
	return job, nil
}

func (jm *JobManager) List() (*JobListResult, error) {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	result := &JobListResult{
		Jobs: []Job{},
	}
	for _, j := range jm.jobs {
		result.Jobs = append(result.Jobs, *j)
	}
	sort.Slice(result.Jobs, func(i, j int) bool {
		return result.Jobs[i].NextRun.Before(result.Jobs[j].NextRun)
	})
	result.Total = len(result.Jobs)
	return result, nil
}

func (jm *JobManager) Remove(id string) error {
	jm.mu.Lock()
	defer jm.mu.Unlock()

	if _, ok := jm.jobs[id]; !ok {
		return fmt.Errorf("job '%s' not found", id)
	}
	delete(jm.jobs, id)
	return jm.saveToDisk()
}

func (jm *JobManager) RunNow(id string) (*Job, error) {
	jm.mu.Lock()
	job, ok := jm.jobs[id]
	jm.mu.Unlock()

	if !ok {
		return nil, fmt.Errorf("job '%s' not found", id)
	}

	// Execute the job (unlocked so the watcher doesn't deadlock)
	jm.executeJob(job)

	jm.mu.Lock()
	defer jm.mu.Unlock()
	return job, nil
}

// ---- Watcher ----

func (jm *JobManager) Start() {
	if jm.running {
		return
	}
	jm.running = true
	go jm.watcherLoop()
}

func (jm *JobManager) watcherLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-jm.stopCh:
			return
		case <-ticker.C:
			jm.tick()
		}
	}
}

func (jm *JobManager) tick() {
	now := time.Now().UTC()
	jm.mu.Lock()
	var due []*Job
	for _, job := range jm.jobs {
		if job.Enabled && !job.NextRun.IsZero() && now.After(job.NextRun) {
			due = append(due, job)
		}
	}
	jm.mu.Unlock()

	for _, job := range due {
		jm.executeJob(job)
	}
}

// executeJob runs the configured tool and updates the job state.
// IMPORTANT: this does NOT hold the mutex during execution.
func (jm *JobManager) executeJob(job *Job) {
	jm.mu.Lock()
	job.LastRun = time.Now().UTC()
	jm.mu.Unlock()

	// Parse the interval for next run
	interval, err := parseSchedule(job.Schedule)
	if err != nil {
		jm.mu.Lock()
		job.LastStatus = "error"
		job.LastError = fmt.Sprintf("invalid schedule: %v", err)
		job.NextRun = time.Now().UTC().Add(1 * time.Hour) // retry in 1h
		jm.mu.Unlock()
		return
	}

	// Look up the tool handler in the registry
	handler, ok := toolHandlerRegistry[job.Tool]
	if !ok {
		jm.mu.Lock()
		job.LastStatus = "error"
		job.LastError = fmt.Sprintf("tool '%s' not found in handler registry", job.Tool)
		job.NextRun = time.Now().UTC().Add(interval)
		_ = jm.saveToDisk()
		jm.mu.Unlock()
		return
	}

	// Build a CallToolRequest from the job's stored args
	req := mcp.CallToolRequest{}
	req.Params.Arguments = job.Args
	if req.Params.Arguments == nil {
		req.Params.Arguments = make(map[string]interface{})
	}

	// Execute the tool handler
	result, err := handler(context.Background(), req)
	errMsg := ""
	if err != nil {
		errMsg = err.Error()
	} else if result != nil && result.IsError {
		// Extract text from the error result
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				if errMsg == "" {
					errMsg = tc.Text
				} else {
					errMsg += "; " + tc.Text
				}
			}
		}
	}

	jm.mu.Lock()
	job.RunCount++
	job.NextRun = time.Now().UTC().Add(interval)
	if errMsg != "" {
		job.LastStatus = "error"
		job.LastError = errMsg
	} else {
		job.LastStatus = "success"
		job.LastError = ""
	}
	_ = jm.saveToDisk()
	jm.mu.Unlock()
}

// ---- Schedule parsing ----

// parseSchedule converts a schedule string into a time.Duration.
// Supported formats:
//   "every 5m" → 5 minutes
//   "every 1h" → 1 hour
//   "every 30s" → 30 seconds
//   "hourly" → 1 hour
//   "daily" → 24 hours
//   "weekly" → 7 * 24 hours
func parseSchedule(s string) (time.Duration, error) {
	s = strings.TrimSpace(strings.ToLower(s))

	if s == "hourly" {
		return time.Hour, nil
	}
	if s == "daily" {
		return 24 * time.Hour, nil
	}
	if s == "weekly" {
		return 7 * 24 * time.Hour, nil
	}

	if strings.HasPrefix(s, "every ") {
		rest := strings.TrimPrefix(s, "every ")
		// Parse like "5m", "1h", "30s", "2d"
		d, err := time.ParseDuration(rest)
		if err != nil {
			return 0, fmt.Errorf("cannot parse duration '%s': %w", rest, err)
		}
		if d < 30*time.Second {
			return 0, fmt.Errorf("minimum interval is 30 seconds, got %s", d)
		}
		return d, nil
	}

	return 0, fmt.Errorf("unsupported schedule format: '%s' (use 'every 5m', 'hourly', 'daily', or 'weekly')", s)
}

// ---- Helpers ----

func generateID(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
}
