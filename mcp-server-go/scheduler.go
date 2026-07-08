// Package main: smart scheduling assistant — bin-packing node suggestions.
//
// Implements "structured data reduction for model decision-making": instead of
// dumping all raw node data to the LLM, this pre-computes a bin-packing score
// per node and returns only the top-3 candidates with human-readable reasoning.
// The model receives a compact, ranked shortlist rather than the full node list.
package main

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
)

// NodeScore is the scored recommendation for placing a new container on a node.
type NodeScore struct {
	NodeID            string  `json:"node_id"`
	Score             float64 `json:"score"` // 0.0–1.0, higher = better
	AvailableCPU      float64 `json:"available_cpu"`
	AvailableMemoryMB int     `json:"available_memory_mb"`
	Reasoning         string  `json:"reasoning"`
}

// ---- Public entry point ----

// suggestNode fetches all nodes via the client, scores each with a bin-packing
// preference algorithm, filters out nodes that don't meet the minimum resource
// requirements, and returns the top 3 candidates sorted by score (descending).
func suggestNode(requiredMemMB int, requiredCPU float64) ([]NodeScore, error) {
	data, err := client.ListNodes()
	if err != nil {
		return nil, err
	}

	nodes, ok := data.([]interface{})
	if !ok {
		return nil, fmt.Errorf("unexpected node list format from ListNodes (expected array)")
	}

	var scored []NodeScore
	for _, raw := range nodes {
		node, ok := raw.(map[string]interface{})
		if !ok {
			continue
		}
		ns, ok := scoreNode(node, requiredMemMB, requiredCPU)
		if !ok {
			continue // hard-filtered: below minimum requirements
		}
		scored = append(scored, ns)
	}

	// Sort descending by score (ties broken by available memory, then CPU)
	sort.Slice(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		if scored[i].AvailableMemoryMB != scored[j].AvailableMemoryMB {
			return scored[i].AvailableMemoryMB > scored[j].AvailableMemoryMB
		}
		return scored[i].AvailableCPU > scored[j].AvailableCPU
	})

	// Return top 3
	if len(scored) > 3 {
		scored = scored[:3]
	}
	return scored, nil
}

// ---- Scoring ----

// scoreNode computes a bin-packing score for a single node.
//
// Algorithm:
//   - Base score: average of memory and CPU availability ratios (available/total)
//   - Bonus: +10% for nodes that already have containers (packing efficiency)
//   - Penalty: -50% for nodes above 80% memory utilization (avoid overload)
//   - Hard filter: exclude nodes below minimum memory or CPU requirement
//
// Returns (NodeScore, true) if the node passes filters, or (zero, false) if excluded.
func scoreNode(node map[string]interface{}, requiredMemMB int, requiredCPU float64) (NodeScore, bool) {
	nodeID := nodeStr(node, "id")

	totalCPU := nodeFloat(node, "cpu_count")
	totalMem := nodeFloat(node, "memory_mb")
	usedMem := nodeFloat(node, "used_memory_mb")
	cpuUsagePct := nodeFloat(node, "cpu_usage_percent")
	running := nodeInt(node, "running_containers")

	// Derive available resources
	availableMem := totalMem - usedMem
	if availableMem < 0 {
		availableMem = 0
	}
	availableCPU := totalCPU
	if cpuUsagePct > 0 {
		availableCPU = totalCPU * (1.0 - cpuUsagePct/100.0)
	}

	// Hard filter: must meet minimum requirements
	if availableMem < float64(requiredMemMB) {
		return NodeScore{}, false
	}
	if availableCPU < requiredCPU {
		return NodeScore{}, false
	}

	// Base score: average of memory and CPU availability ratios
	memRatio := 1.0
	if totalMem > 0 {
		memRatio = availableMem / totalMem
	}
	cpuRatio := 1.0
	if totalCPU > 0 {
		cpuRatio = availableCPU / totalCPU
	}
	score := (memRatio + cpuRatio) / 2.0

	// Bonus: prefer nodes that already have containers (+10% packing efficiency)
	if running > 0 {
		score *= 1.10
	}

	// Penalty: nodes above 80% utilization (-50%)
	utilization := 1.0 - memRatio
	if utilization > 0.80 {
		score *= 0.50
	}

	// Clamp to [0.0, 1.0]
	score = math.Max(0.0, math.Min(1.0, score))

	return NodeScore{
		NodeID:            nodeID,
		Score:             round2(score),
		AvailableCPU:      round2(availableCPU),
		AvailableMemoryMB: int(availableMem),
		Reasoning:         buildReasoning(nodeID, availableCPU, availableMem, totalMem, running, utilization),
	}, true
}

// buildReasoning generates a human-readable explanation of why this node was ranked.
func buildReasoning(nodeID string, availCPU, availMem, totalMem float64, running int, utilization float64) string {
	var parts []string

	// Memory headroom
	if totalMem > 0 {
		parts = append(parts, fmt.Sprintf("%.0f MB free of %.0f MB", availMem, totalMem))
	}
	// CPU headroom
	parts = append(parts, fmt.Sprintf("%.1f CPU cores available", availCPU))
	// Packing context
	if running > 0 {
		parts = append(parts, fmt.Sprintf("already running %d container%s", running, pluralS(running)))
	} else {
		parts = append(parts, "no containers running (fresh node)")
	}
	// Overload warning
	if utilization > 0.80 {
		parts = append(parts, fmt.Sprintf("warning: %.0f%% memory utilization", utilization*100))
	}

	return strings.Join(parts, ", ")
}

// ---- MCP handler ----

func handleSuggestNode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	requiredMem := argInt(args, "memory_mb", 0)
	requiredCPU := argFloat(args, "cpu_count", 0)

	scores, err := suggestNode(requiredMem, requiredCPU)
	if err != nil {
		return unwrapError(err), nil
	}

	result := map[string]interface{}{
		"strategy":          "bin-packing (prefer fuller nodes, avoid overload)",
		"required_memory_mb": requiredMem,
		"required_cpu":      requiredCPU,
		"candidates":        scores,
		"count":             len(scores),
	}
	if len(scores) == 0 {
		result["note"] = "No nodes meet the minimum resource requirements"
	}

	return okResult(result), nil
}

// ---- Field extraction helpers (defensive: missing fields default to zero) ----

func nodeFloat(m map[string]interface{}, key string) float64 {
	v, ok := m[key]
	if !ok || v == nil {
		return 0
	}
	switch n := v.(type) {
	case float64:
		return n
	case float32:
		return float64(n)
	case int:
		return float64(n)
	case int64:
		return float64(n)
	}
	return 0
}

func nodeInt(m map[string]interface{}, key string) int {
	return int(nodeFloat(m, key))
}

func nodeStr(m map[string]interface{}, key string) string {
	if v, ok := m[key]; ok && v != nil {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

// ---- Small utilities ----

func pluralS(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func round2(f float64) float64 {
	return math.Round(f*100) / 100
}
