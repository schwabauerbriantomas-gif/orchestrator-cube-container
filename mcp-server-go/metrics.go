// Package main: Prometheus metrics endpoint for the MCP server.
// Exposes cluster state and server counters at GET /metrics (no auth).
// Raw text exposition format — no external dependencies.
package main

import (
	"fmt"
	"net/http"
	"sync"
)

// ---- MetricsCollector: thread-safe internal counters ----

// MetricsCollector holds MCP server counters used by the /metrics endpoint.
type MetricsCollector struct {
	mu sync.Mutex

	// cube_mcp_requests_total{method,status}
	requests map[string]uint64 // key: "method|status"

	// cube_mcp_auth_failures_total
	authFailures uint64

	// cube_mcp_rate_limited_total
	rateLimited uint64

	// cube_mcp_tool_calls_total{tool_name}
	toolCalls map[string]uint64
}

// newMetricsCollector returns an initialized MetricsCollector.
func newMetricsCollector() *MetricsCollector {
	return &MetricsCollector{
		requests: make(map[string]uint64),
		toolCalls: make(map[string]uint64),
	}
}

// IncRequests increments the request counter for a method+status pair.
func (m *MetricsCollector) IncRequests(method, status string) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.requests[method+"|"+status]++
}

// IncAuthFailure increments the auth failure counter.
func (m *MetricsCollector) IncAuthFailure() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.authFailures++
}

// IncRateLimited increments the rate-limited counter.
func (m *MetricsCollector) IncRateLimited() {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rateLimited++
}

// IncToolCall increments the tool call counter for a given tool name.
func (m *MetricsCollector) IncToolCall(toolName string) {
	if m == nil || toolName == "" {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.toolCalls[toolName]++
}

// snapshot returns thread-safe copies of all counter values.
func (m *MetricsCollector) snapshot() (map[string]uint64, uint64, uint64, map[string]uint64) {
	if m == nil {
		return nil, 0, 0, nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()

	reqs := make(map[string]uint64, len(m.requests))
	for k, v := range m.requests {
		reqs[k] = v
	}
	tools := make(map[string]uint64, len(m.toolCalls))
	for k, v := range m.toolCalls {
		tools[k] = v
	}
	return reqs, m.authFailures, m.rateLimited, tools
}

// ---- Prometheus text exposition writer ----

// writeMetrics writes all metrics to w in Prometheus text exposition format.
func writeMetrics(w http.ResponseWriter, collector *MetricsCollector, client *CubeClient) {
	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")

	// --- Cluster state gauges (collected live from CubeAPI) ---
	var (
		nodesTotal      int
		containersRun   int
		containersPause int
		cpuTotal        float64
		memTotalMB      float64
		memUsedMB       float64
	)

	if client != nil {
		// Cluster overview: nodes, containers, CPU, memory
		if overview, err := client.ClusterOverview(); err == nil {
			if m, ok := overview.(map[string]interface{}); ok {
				nodesTotal = metricToInt(m["nodeCount"])
				containersRun = metricToInt(m["runningContainers"])
				cpuTotal = metricToFloat(m["cpuTotalCores"])
				memTotalMB = metricToFloat(m["memoryTotalMB"])
				memUsedMB = metricToFloat(m["memoryUsedMB"])
			}
		}

		// Paused containers — query the API directly
		if paused, err := client.ListSandboxes("paused", 1000); err == nil {
			if arr, ok := paused.([]interface{}); ok {
				containersPause = len(arr)
			} else if m, ok := paused.(map[string]interface{}); ok {
				if arr, ok := m["sandboxes"].([]interface{}); ok {
					containersPause = len(arr)
				}
			}
		}
	}

	fmt.Fprintf(w, "# HELP cube_cluster_nodes_total Total nodes in the cluster.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_nodes_total gauge\n")
	fmt.Fprintf(w, "cube_cluster_nodes_total %d\n\n", nodesTotal)

	fmt.Fprintf(w, "# HELP cube_cluster_containers_running Number of running containers.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_containers_running gauge\n")
	fmt.Fprintf(w, "cube_cluster_containers_running %d\n\n", containersRun)

	fmt.Fprintf(w, "# HELP cube_cluster_containers_paused Number of paused containers.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_containers_paused gauge\n")
	fmt.Fprintf(w, "cube_cluster_containers_paused %d\n\n", containersPause)

	fmt.Fprintf(w, "# HELP cube_cluster_cpu_total Total CPU cores in the cluster.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_cpu_total gauge\n")
	fmt.Fprintf(w, "cube_cluster_cpu_total %s\n\n", floatToStr(cpuTotal))

	fmt.Fprintf(w, "# HELP cube_cluster_memory_total_mb Total memory in MB.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_memory_total_mb gauge\n")
	fmt.Fprintf(w, "cube_cluster_memory_total_mb %s\n\n", floatToStr(memTotalMB))

	fmt.Fprintf(w, "# HELP cube_cluster_memory_used_mb Used memory in MB.\n")
	fmt.Fprintf(w, "# TYPE cube_cluster_memory_used_mb gauge\n")
	fmt.Fprintf(w, "cube_cluster_memory_used_mb %s\n\n", floatToStr(memUsedMB))

	// --- MCP server counters ---
	reqs, authFail, rateLimit, tools := collector.snapshot()

	fmt.Fprintf(w, "# HELP cube_mcp_requests_total Total HTTP requests handled.\n")
	fmt.Fprintf(w, "# TYPE cube_mcp_requests_total counter\n")
	// Sort keys for deterministic output
	for _, key := range sortedKeys(reqs) {
		fmt.Fprintf(w, "cube_mcp_requests_total%s %d\n", splitLabels(key), reqs[key])
	}
	if len(reqs) == 0 {
		fmt.Fprintf(w, "cube_mcp_requests_total{method=\"\",status=\"\"} 0\n")
	}
	fmt.Fprintln(w)

	fmt.Fprintf(w, "# HELP cube_mcp_auth_failures_total Total authentication failures.\n")
	fmt.Fprintf(w, "# TYPE cube_mcp_auth_failures_total counter\n")
	fmt.Fprintf(w, "cube_mcp_auth_failures_total %d\n\n", authFail)

	fmt.Fprintf(w, "# HELP cube_mcp_rate_limited_total Total requests rejected by rate limiter.\n")
	fmt.Fprintf(w, "# TYPE cube_mcp_rate_limited_total counter\n")
	fmt.Fprintf(w, "cube_mcp_rate_limited_total %d\n\n", rateLimit)

	fmt.Fprintf(w, "# HELP cube_mcp_tool_calls_total Total tool calls by tool name.\n")
	fmt.Fprintf(w, "# TYPE cube_mcp_tool_calls_total counter\n")
	for _, name := range sortedKeys(tools) {
		fmt.Fprintf(w, "cube_mcp_tool_calls_total{tool_name=\"%s\"} %d\n", name, tools[name])
	}
	if len(tools) == 0 {
		fmt.Fprintf(w, "cube_mcp_tool_calls_total{tool_name=\"\"} 0\n")
	}
	fmt.Fprintln(w)
}

// metricsHandler is the HTTP handler for GET /metrics (no auth).
func metricsHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	writeMetrics(w, metricsCollector, client)
}

// ---- Helpers ----

// metricToInt safely converts interface{} (from JSON) to int.
func metricToInt(v interface{}) int {
	switch n := v.(type) {
	case float64:
		return int(n)
	case int:
		return n
	}
	return 0
}

// metricToFloat safely converts interface{} (from JSON) to float64.
func metricToFloat(v interface{}) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int:
		return float64(n)
	}
	return 0
}

// floatToStr formats a float for Prometheus output (trailing zeros trimmed).
func floatToStr(f float64) string {
	return fmt.Sprintf("%g", f)
}

// splitLabels takes a "method|status" composite key and returns Prometheus
// label syntax: {method="GET",status="200"}.
func splitLabels(composite string) string {
	// Split on "|" delimiter
	parts := splitPipe(composite)
	method := ""
	status := ""
	if len(parts) > 0 {
		method = parts[0]
	}
	if len(parts) > 1 {
		status = parts[1]
	}
	return fmt.Sprintf("{method=\"%s\",status=\"%s\"}", method, status)
}

// splitPipe splits a string on "|" without using strings.Split to keep imports minimal.
func splitPipe(s string) []string {
	var result []string
	start := 0
	for i, c := range s {
		if c == '|' {
			result = append(result, s[start:i])
			start = i + 1
		}
	}
	result = append(result, s[start:])
	return result
}

// sortedKeys returns map keys sorted alphabetically (for deterministic output).
func sortedKeys(m map[string]uint64) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	// Simple insertion sort — maps are small.
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j-1] > keys[j]; j-- {
			keys[j-1], keys[j] = keys[j], keys[j-1]
		}
	}
	return keys
}
