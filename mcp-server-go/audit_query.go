// Package main: audit log querying for incident investigation.
//
// The audit logger in auth.go records every tool invocation with a hash chain
// for tamper resistance. audit_query lets the LLM search this trail to answer
// "what happened in the last 2 hours?" or "who deployed X?".
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// ---- Types ----

type AuditQueryParams struct {
	SinceHours  int      `json:"since_hours,omitempty"`  // default 24
	ToolName    string   `json:"tool_name,omitempty"`    // filter by tool
	APIKeyLabel string   `json:"api_key_label,omitempty"` // filter by caller
	Role        string   `json:"role,omitempty"`         // filter by role
	Success     *bool    `json:"success,omitempty"`      // filter by success/failure
	Limit       int      `json:"limit"`
}

// AuditQueryEntry is the parsed representation of an audit log entry.
// It matches the JSON structure written by auth.go's AuditEntry but with
// parsed time types for filtering.
type AuditQueryEntry struct {
	Timestamp  string `json:"timestamp"`
	Key        string `json:"key"`
	Role       string `json:"role"`
	Method     string `json:"method"`
	Path       string `json:"path"`
	Tool       string `json:"tool,omitempty"`
	StatusCode int    `json:"status_code"`
	Duration   string `json:"duration"`
	Allowed    bool   `json:"allowed"`
	Reason     string `json:"reason,omitempty"`
	PrevHash   string `json:"prev_hash"`
	Hash       string `json:"hash"`
}

type AuditQueryResult struct {
	Entries  []AuditQueryEntry `json:"entries"`
	Total    int               `json:"total"`
	Filtered int               `json:"filtered"`
}

// ---- Manager ----

var auditQueryMgr *AuditQueryManager

type AuditQueryManager struct {
	logDir string
}

func newAuditQueryManager() *AuditQueryManager {
	return &AuditQueryManager{
		logDir: envOr("CUBE_AUDIT_DIR", "/var/lib/cube-container/audit"),
	}
}

// Query searches the audit log with the given parameters.
func (aq *AuditQueryManager) Query(params AuditQueryParams) (*AuditQueryResult, error) {
	if params.SinceHours <= 0 {
		params.SinceHours = 24
	}
	if params.Limit <= 0 {
		params.Limit = 100
	}
	if params.Limit > 1000 {
		params.Limit = 1000
	}

	cutoff := time.Now().UTC().Add(time.Duration(-params.SinceHours) * time.Hour)

	// Audit logs are stored as daily files: audit-2026-07-08.logl
	// Read from today backwards until we're past the cutoff
	var entries []AuditQueryEntry

	// Scan last N days (cutoff days + 1)
	days := int(time.Since(cutoff).Hours()/24) + 2
	for i := 0; i <= days; i++ {
		day := time.Now().UTC().AddDate(0, 0, -i)
		filename := filepath.Join(aq.logDir, fmt.Sprintf("audit-%s.logl", day.Format("2006-01-02")))
		dayEntries, err := aq.readAuditFile(filename)
		if err != nil {
			continue // file might not exist
		}
		entries = append(entries, dayEntries...)
	}

	// Filter
	var filtered []AuditQueryEntry
	for _, e := range entries {
		// Parse timestamp for cutoff comparison
		entryTime, err := time.Parse(time.RFC3339Nano, e.Timestamp)
		if err != nil {
			continue
		}
		if entryTime.Before(cutoff) {
			continue
		}
		if params.ToolName != "" && e.Tool != params.ToolName {
			continue
		}
		if params.Role != "" && e.Role != params.Role {
			continue
		}
		if params.Success != nil && e.Allowed != *params.Success {
			continue
		}
		filtered = append(filtered, e)
	}

	// Sort by timestamp descending (parsed time)
	sort.Slice(filtered, func(i, j int) bool {
		return filtered[i].Timestamp > filtered[j].Timestamp
	})

	// Limit
	if len(filtered) > params.Limit {
		filtered = filtered[:params.Limit]
	}

	return &AuditQueryResult{
		Entries:  filtered,
		Total:    len(entries),
		Filtered: len(filtered),
	}, nil
}

// readAuditFile reads a single .logl audit file and parses entries.
// Each line is a JSON object (JSONL format).
func (aq *AuditQueryManager) readAuditFile(path string) ([]AuditQueryEntry, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}

	var entries []AuditQueryEntry
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var entry AuditQueryEntry
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			continue // skip malformed lines
		}
		entries = append(entries, entry)
	}

	return entries, nil
}
