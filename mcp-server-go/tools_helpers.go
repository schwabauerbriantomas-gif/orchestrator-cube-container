// Package main: shared tool builder and argument extraction helpers.
// Extracted from server.go for maintainability (AUDIT FIX L-02).
package main

import (
	"fmt"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// ---- Tool builders ----

func tool(name, desc string) mcp.Tool {
	return mcp.NewTool(name, mcp.WithDescription(desc))
}

func toolWithArgs(name, desc string, opts ...mcp.ToolOption) mcp.Tool {
	allOpts := append([]mcp.ToolOption{mcp.WithDescription(desc)}, opts...)
	return mcp.NewTool(name, allOpts...)
}

// ---- Handler registry (for scheduled job execution) ----

// toolHandlerRegistry maps tool names to their handler functions.
// Populated by registerAllTools, consumed by the job scheduler.
var toolHandlerRegistry = make(map[string]server.ToolHandlerFunc)

// registerTool registers a tool with the MCP server AND records the handler
// in the registry so the job scheduler can invoke it internally.
func registerTool(s *server.MCPServer, t mcp.Tool, handler server.ToolHandlerFunc) {
	toolHandlerRegistry[t.Name] = handler
	s.AddTool(t, handler)
}

// ---- Argument extraction helpers ----

func argString(args map[string]interface{}, key string) string {
	if v, ok := args[key]; ok {
		return fmt.Sprintf("%v", v)
	}
	return ""
}

func argInt(args map[string]interface{}, key string, def int) int {
	if v, ok := args[key]; ok {
		switch n := v.(type) {
		case float64:
			return int(n)
		case int:
			return n
		}
	}
	return def
}

func argFloat(args map[string]interface{}, key string, def float64) float64 {
	if v, ok := args[key]; ok {
		if n, ok := v.(float64); ok {
			return n
		}
	}
	return def
}

func argMap(args map[string]interface{}, key string) map[string]interface{} {
	if v, ok := args[key].(map[string]interface{}); ok {
		return v
	}
	return nil
}

func argStringSlice(args map[string]interface{}, key string) []string {
	if v, ok := args[key].([]interface{}); ok {
		result := make([]string, 0, len(v))
		for _, item := range v {
			result = append(result, fmt.Sprintf("%v", item))
		}
		return result
	}
	return nil
}

func argIntSlice(args map[string]interface{}, key string) []int {
	if v, ok := args[key].([]interface{}); ok {
		result := make([]int, 0, len(v))
		for _, item := range v {
			switch n := item.(type) {
			case float64:
				result = append(result, int(n))
			case int:
				result = append(result, n)
			}
		}
		return result
	}
	return nil
}

// ---- Result helpers ----

func okResult(data interface{}) *mcp.CallToolResult {
	return mcp.NewToolResultText(toJSON(data))
}

func errResult(msg string) *mcp.CallToolResult {
	return mcp.NewToolResultError(fmt.Sprintf("Error: %s", msg))
}

func parseArgs(request mcp.CallToolRequest) map[string]interface{} {
	return request.GetArguments()
}

func unwrapError(err error) *mcp.CallToolResult {
	if apiErr, ok := err.(*CubeAPIError); ok {
		return errResult(fmt.Sprintf("API error %d: %s", apiErr.Status, apiErr.Detail))
	}
	return errResult(err.Error())
}
