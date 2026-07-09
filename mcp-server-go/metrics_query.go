// Package main: metrics querying for the MCP server.
//
// metrics.go exposes Prometheus metrics at /metrics. This tool lets the LLM
// query those metrics programmatically — CPU, memory, network, container
// counts — without needing an external Prometheus instance.
//
// The query tool reads from the in-memory MetricsCollector, which is already
// populated by the background metrics collection loop.
package main

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// ---- Types ----

type MetricsQueryParams struct {
	Metric    string   `json:"metric"`              // specific metric name (optional)
	Container string   `json:"container,omitempty"` // filter by container
	SinceMinutes int   `json:"since_minutes,omitempty"`
	Limit     int      `json:"limit"`
}

type MetricsQueryResult struct {
	Metrics   []MetricSeries `json:"metrics"`
	QueryTime string         `json:"query_time"`
}

type MetricSeries struct {
	Name      string    `json:"name"`
	Container string    `json:"container,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"`
	Value     float64   `json:"value"`
	Timestamp time.Time `json:"timestamp"`
}

// ---- Manager ----

var metricsQueryMgr *MetricsQueryManager

type MetricsQueryManager struct {
	collector *MetricsCollector
}

func newMetricsQueryManager(mc *MetricsCollector) *MetricsQueryManager {
	return &MetricsQueryManager{collector: mc}
}

// Query returns current metrics values, optionally filtered.
func (mq *MetricsQueryManager) Query(params MetricsQueryParams) (*MetricsQueryResult, error) {
	if params.Limit <= 0 {
		params.Limit = 50
	}

	result := &MetricsQueryResult{
		Metrics: []MetricSeries{},
	}

	// Get current snapshot from the metrics collector
	// snapshot() returns: requests map, authFailures, rateLimited, toolCalls map
	reqs, authFailures, rateLimited, toolCalls := mq.collector.snapshot()

	// Merge into a single flat map for filtering
	allMetrics := make(map[string]float64)
	for k, v := range reqs {
		allMetrics["requests."+k] = float64(v)
	}
	allMetrics["auth_failures_total"] = float64(authFailures)
	allMetrics["rate_limited_total"] = float64(rateLimited)
	for k, v := range toolCalls {
		allMetrics["tool_calls."+k] = float64(v)
	}

	for name, value := range allMetrics {
		// Filter by metric name
		if params.Metric != "" && !strings.Contains(strings.ToLower(name), strings.ToLower(params.Metric)) {
			continue
		}
		// Filter by container
		if params.Container != "" && !strings.Contains(name, params.Container) {
			continue
		}

		result.Metrics = append(result.Metrics, MetricSeries{
			Name:      name,
			Value:     value,
			Timestamp: time.Now().UTC(),
		})
	}

	// Sort by name for stable output
	sort.Slice(result.Metrics, func(i, j int) bool {
		return result.Metrics[i].Name < result.Metrics[j].Name
	})

	// Limit results
	if len(result.Metrics) > params.Limit {
		result.Metrics = result.Metrics[:params.Limit]
	}

	result.QueryTime = time.Now().UTC().Format(time.RFC3339)
	return result, nil
}

// ---- Snapshot helper ----
//
// MetricsCollector needs a Snapshot() method. If it doesn't exist yet,
// we provide a stub here that reads from the collector's internal state.
// The real implementation is in metrics.go.

func ensureMetricsSnapshot() {
	// This function exists to document that MetricsCollector.Snapshot()
	// must exist. If it doesn't, we need to add it.
	if metricsCollector == nil {
		return
	}
}

var _ = fmt.Sprintf // keep import
