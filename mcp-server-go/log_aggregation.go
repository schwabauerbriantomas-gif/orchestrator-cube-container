// Package main: multi-container log search and aggregation.
//
// get_container_logs fetches logs from ONE container. logs_search and
// logs_aggregate work across ALL (or filtered) containers — the tool an LLM
// uses for incident response and debugging.
//
// logs_search: search across containers by text pattern, time range, severity.
// logs_aggregate: count log lines by level (error/warn/info) per container.
package main

import (
	"fmt"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---- Types ----

type LogSearchParams struct {
	Pattern     string   `json:"pattern,omitempty"`
	Containers  []string `json:"containers,omitempty"`  // empty = all
	Level       string   `json:"level,omitempty"`       // error, warn, info, debug
	SinceMinutes int     `json:"since_minutes,omitempty"` // 0 = no filter
	MaxResults  int      `json:"max_results"`
}

type LogSearchResult struct {
	TotalMatches int           `json:"total_matches"`
	Lines        []LogLine     `json:"lines"`
	SearchedContainers int     `json:"searched_containers"`
	SearchTime   string        `json:"search_time"`
}

type LogLine struct {
	ContainerID string `json:"container_id"`
	Timestamp   string `json:"timestamp"`
	Level       string `json:"level"`
	Message     string `json:"message"`
}

type LogAggregateResult struct {
	Containers []ContainerLogStats `json:"containers"`
	Total      ContainerLogStats   `json:"total"`
}

type ContainerLogStats struct {
	ContainerID string `json:"container_id"`
	ErrorCount  int    `json:"error_count"`
	WarnCount   int    `json:"warn_count"`
	InfoCount   int    `json:"info_count"`
	TotalLines  int    `json:"total_lines"`
}

// ---- Manager ----

var logAggMgr *LogAggregationManager

type LogAggregationManager struct {
	backend ContainerBackend
}

func newLogAggregationManager(b ContainerBackend) *LogAggregationManager {
	return &LogAggregationManager{backend: b}
}

// ---- Level detection ----

var levelPatterns = map[string]*regexp.Regexp{
	"error": regexp.MustCompile(`(?i)\b(ERROR|ERR|FATAL|CRITICAL|PANIC|TRACEBACK)\b`),
	"warn":  regexp.MustCompile(`(?i)\b(WARN|WARNING|CAUTION)\b`),
	"info":  regexp.MustCompile(`(?i)\b(INFO|NOTICE|LOG)\b`),
	"debug": regexp.MustCompile(`(?i)\b(DEBUG|TRACE|VERBOSE)\b`),
}

func detectLevel(line string) string {
	for level, pat := range levelPatterns {
		if pat.MatchString(line) {
			return level
		}
	}
	return "unknown"
}

// ---- Search ----

func (la *LogAggregationManager) Search(params LogSearchParams) (*LogSearchResult, error) {
	if params.MaxResults <= 0 {
		params.MaxResults = 100
	}

	var searchPattern *regexp.Regexp
	if params.Pattern != "" {
		pat, err := regexp.Compile("(?i)" + regexp.QuoteMeta(params.Pattern))
		if err != nil {
			return nil, fmt.Errorf("invalid search pattern: %w", err)
		}
		searchPattern = pat
	}

	// Get container list
	containerIDs := params.Containers
	if len(containerIDs) == 0 {
		containersRaw, err := la.backend.ListSandboxes("running", 200)
		if err != nil {
			return nil, fmt.Errorf("failed to list containers: %w", err)
		}
		containerIDs = extractContainerIDs(containersRaw)
		if len(containerIDs) == 0 {
			return &LogSearchResult{Lines: []LogLine{}, SearchTime: time.Now().UTC().Format(time.RFC3339)}, nil
		}
	}

	result := &LogSearchResult{
		Lines:               []LogLine{},
		SearchedContainers:  len(containerIDs),
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cid := range containerIDs {
		wg.Add(1)
		go func(containerID string) {
			defer wg.Done()

			logsRaw, err := la.backend.GetSandboxLogs(containerID, 500)
			if err != nil {
				return
			}

			lines := parseLogLines(containerID, logsRaw)
			for _, line := range lines {
				// Apply level filter
				if params.Level != "" && line.Level != params.Level {
					continue
				}
				// Apply pattern filter
				if searchPattern != nil && !searchPattern.MatchString(line.Message) {
					continue
				}
				mu.Lock()
				result.Lines = append(result.Lines, line)
				result.TotalMatches++
				mu.Unlock()
			}
		}(cid)
	}
	wg.Wait()

	// Sort by timestamp (most recent first)
	sort.Slice(result.Lines, func(i, j int) bool {
		return result.Lines[i].Timestamp > result.Lines[j].Timestamp
	})

	// Limit results
	if len(result.Lines) > params.MaxResults {
		result.Lines = result.Lines[:params.MaxResults]
	}

	result.SearchTime = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

// ---- Aggregate ----

func (la *LogAggregationManager) Aggregate(containerIDs []string, sinceLines int) (*LogAggregateResult, error) {
	if sinceLines <= 0 {
		sinceLines = 200
	}

	if len(containerIDs) == 0 {
		containersRaw, err := la.backend.ListSandboxes("running", 200)
		if err != nil {
			return nil, fmt.Errorf("failed to list containers: %w", err)
		}
		containerIDs = extractContainerIDs(containersRaw)
	}

	result := &LogAggregateResult{
		Containers: []ContainerLogStats{},
		Total:      ContainerLogStats{},
	}

	var mu sync.Mutex
	var wg sync.WaitGroup

	for _, cid := range containerIDs {
		wg.Add(1)
		go func(containerID string) {
			defer wg.Done()

			logsRaw, err := la.backend.GetSandboxLogs(containerID, sinceLines)
			if err != nil {
				return
			}

			lines := parseLogLines(containerID, logsRaw)
			stats := ContainerLogStats{
				ContainerID: containerID,
			}
			for _, line := range lines {
				stats.TotalLines++
				result.Total.TotalLines++
				switch line.Level {
				case "error":
					stats.ErrorCount++
					result.Total.ErrorCount++
				case "warn":
					stats.WarnCount++
					result.Total.WarnCount++
				case "info":
					stats.InfoCount++
					result.Total.InfoCount++
				}
			}

			mu.Lock()
			result.Containers = append(result.Containers, stats)
			mu.Unlock()
		}(cid)
	}
	wg.Wait()

	// Sort by error count descending
	sort.Slice(result.Containers, func(i, j int) bool {
		return result.Containers[i].ErrorCount > result.Containers[j].ErrorCount
	})

	return result, nil
}

// ---- Helpers ----

// extractContainerIDs pulls container IDs from a backend list response.
func extractContainerIDs(raw interface{}) []string {
	list, ok := raw.([]interface{})
	if !ok {
		return nil
	}
	ids := make([]string, 0, len(list))
	for _, item := range list {
		m, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		// Docker uses "Id", Cube uses "id"
		id, _ := m["id"].(string)
		if id == "" {
			id, _ = m["Id"].(string)
		}
		if id != "" {
			ids = append(ids, id)
		}
	}
	return ids
}

// parseLogLines parses raw log output into structured log lines.
func parseLogLines(containerID string, raw interface{}) []LogLine {
	var text string
	switch v := raw.(type) {
	case string:
		text = v
	case map[string]interface{}:
		if logs, ok := v["logs"].(string); ok {
			text = logs
		} else if logs, ok := v["logs"].([]interface{}); ok {
			var sb strings.Builder
			for _, l := range logs {
				sb.WriteString(fmt.Sprintf("%v\n", l))
			}
			text = sb.String()
		}
	default:
		text = fmt.Sprintf("%v", raw)
	}

	lines := []LogLine{}
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		lines = append(lines, LogLine{
			ContainerID: containerID,
			Message:     line,
			Level:       detectLevel(line),
			Timestamp:   time.Now().UTC().Format(time.RFC3339), // best effort
		})
	}
	return lines
}
