// Package main: real-time log streaming via Server-Sent Events (SSE).
//
// Provides GET /streams/{container_id}/logs — an SSE endpoint that polls the
// CubeAPI logs endpoint every 2 seconds and pushes only new lines to the
// client. This lets AI agents watch container logs in real-time without
// burning tokens on repeated one-shot polling.
//
// Also provides the tail_container_logs MCP tool for one-shot recent logs
// with a higher default limit than get_container_logs.
//
// Uses only the standard library (net/http for SSE).
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
)

// ---- Configuration constants ----

const (
	// streamPollInterval is how often the CubeAPI logs endpoint is polled.
	streamPollInterval = 2 * time.Second
	// streamLineLimit is the number of log lines requested per poll.
	streamLineLimit = 50
	// streamHeartbeatInterval keeps the SSE connection alive through proxies.
	streamHeartbeatInterval = 30 * time.Second
	// streamDefaultTimeout caps how long a single SSE connection may live.
	streamDefaultTimeout = 5 * time.Minute
)

// streamTimeout returns the max SSE connection duration, configurable via the
// CUBE_STREAM_TIMEOUT env var (e.g. "5m", "10m", "30s"). Falls back to 5m.
func streamTimeout() time.Duration {
	if v := os.Getenv("CUBE_STREAM_TIMEOUT"); v != "" {
		if d, err := time.ParseDuration(v); err == nil && d > 0 {
			return d
		}
	}
	return streamDefaultTimeout
}

// ---- SSE log streaming handler ----

// handleLogStream is the HTTP handler for GET /streams/{container_id}/logs.
//
// It opens a Server-Sent Events connection, polls the CubeAPI logs endpoint
// every 2 seconds, and pushes only new (unseen) log lines as SSE events. The
// connection closes when the client disconnects or the configured max stream
// duration (CUBE_STREAM_TIMEOUT, default 5m) is reached.
func handleLogStream(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed; use GET", http.StatusMethodNotAllowed)
		return
	}

	containerID := extractStreamContainerID(r.URL.Path)
	if containerID == "" {
		http.Error(w, "container_id required: GET /streams/{container_id}/logs", http.StatusBadRequest)
		return
	}
	if err := validateContainerID(containerID); err != nil {
		http.Error(w, fmt.Sprintf("invalid container_id: %s", err), http.StatusBadRequest)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported (ResponseWriter is not a Flusher)", http.StatusInternalServerError)
		return
	}

	// SSE headers — standard Server-Sent Events.
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	// Hint to nginx and similar reverse proxies to disable response buffering.
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	// Initial comment line (SSE comments start with ':') signals the stream is open.
	fmt.Fprintf(w, ": connected container_id=%s\n\n", containerID) //nosec G705 -- containerID validated by validateContainerID above
	flusher.Flush()

	ctx := r.Context()
	deadline := time.Now().Add(streamTimeout())
	seen := make(map[uint64]struct{})

	// Send an initial batch immediately so the client doesn't wait 2s for first data.
	pollAndSend(w, flusher, containerID, seen)

	ticker := time.NewTicker(streamPollInterval)
	defer ticker.Stop()

	lastHeartbeat := time.Now()

	for {
		select {
		case <-ctx.Done():
			// Client disconnected — stop streaming.
			return
		case <-ticker.C:
			// Poll for new logs and push any unseen lines.
			pollAndSend(w, flusher, containerID, seen)

			// Periodic heartbeat keeps proxies from closing idle connections.
			if time.Since(lastHeartbeat) >= streamHeartbeatInterval {
				fmt.Fprintf(w, ": heartbeat %s\n\n", time.Now().UTC().Format(time.RFC3339))
				flusher.Flush()
				lastHeartbeat = time.Now()
			}

			// Enforce max connection duration.
			if time.Now().After(deadline) {
				fmt.Fprintf(w, "event: timeout\ndata: {\"message\":\"stream timeout reached (%s)\"}\n\n", streamTimeout())
				flusher.Flush()
				return
			}
		}
	}
}

// pollAndSend fetches the latest logs from CubeAPI and writes any unseen lines
// as SSE events. Duplicate lines (by content hash) are suppressed for the
// lifetime of the connection.
func pollAndSend(w http.ResponseWriter, flusher http.Flusher, containerID string, seen map[uint64]struct{}) {
	data, err := client.GetSandboxLogs(containerID, streamLineLimit)
	if err != nil {
		// Non-fatal: report the error as an SSE comment and keep the stream alive.
		fmt.Fprintf(w, ": error fetching logs: %v\n\n", err)
		flusher.Flush()
		return
	}

	for _, line := range extractLogLines(data) {
		h := fnvHash(line)
		if _, dup := seen[h]; dup {
			continue
		}
		seen[h] = struct{}{}
		writeSSELogEvent(w, containerID, line)
	}
	flusher.Flush()
}

// writeSSELogEvent writes a single log line as an SSE event:
//
//	event: log
//	data: {"timestamp":"...","line":"...","container_id":"..."}
func writeSSELogEvent(w http.ResponseWriter, containerID, line string) {
	payload := map[string]string{
		"timestamp":    time.Now().UTC().Format(time.RFC3339),
		"line":         line,
		"container_id": containerID,
	}
	b, _ := json.Marshal(payload)
	fmt.Fprintf(w, "event: log\ndata: %s\n\n", b)
}

// extractStreamContainerID parses the container ID from a path of the form
// /streams/{container_id}/logs. Returns "" if the path is malformed.
func extractStreamContainerID(path string) string {
	s := strings.TrimPrefix(path, "/streams/")
	s = strings.TrimSuffix(s, "/logs")
	return strings.Trim(s, "/")
}

// ---- Log line normalization ----

// extractLogLines converts the loosely-typed CubeAPI logs response into a flat
// slice of non-empty log lines. Handles strings (split on newlines), arrays,
// and common JSON wrapper keys (logs, lines, output, stdout) as well as
// single log-entry objects (line, message, text, msg).
func extractLogLines(data interface{}) []string {
	switch v := data.(type) {
	case string:
		return splitNonEmpty(v)
	case []interface{}:
		lines := make([]string, 0, len(v))
		for _, item := range v {
			lines = append(lines, extractLogLines(item)...)
		}
		return lines
	case map[string]interface{}:
		// Unwrap common collection keys.
		for _, key := range []string{"logs", "lines", "output", "stdout"} {
			if inner, ok := v[key]; ok {
				return extractLogLines(inner)
			}
		}
		// Single log-entry object.
		for _, key := range []string{"line", "message", "text", "msg"} {
			if s, ok := v[key].(string); ok {
				return []string{s}
			}
		}
		return nil
	default:
		return nil
	}
}

// splitNonEmpty splits text on newlines and drops blank/whitespace-only lines.
func splitNonEmpty(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) != "" {
			out = append(out, line)
		}
	}
	return out
}

// fnvHash returns a 64-bit FNV-1a hash of s, used for O(1) duplicate detection.
func fnvHash(s string) uint64 {
	h := fnv.New64a()
	h.Write([]byte(s))
	return h.Sum64()
}

// ---- One-shot tail tool (MCP) ----

// handleTailLogs is the MCP tool handler for tail_container_logs: a one-shot
// fetch of the last N log lines. Uses a higher default limit (200) than
// get_container_logs (100) to better suit "show me recent activity" use cases.
// For continuous real-time streaming, clients should use the SSE endpoint at
// GET /streams/{container_id}/logs.
func handleTailLogs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	limit := argInt(args, "limit", 200)
	data, err := client.GetSandboxLogs(id, limit)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}
