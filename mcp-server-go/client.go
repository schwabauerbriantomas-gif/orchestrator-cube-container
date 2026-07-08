// Package main: HTTP client for CubeAPI REST surface.
// Port of client.py — wraps all endpoints with typed Go methods.
package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"
)

// CubeAPIError is raised when CubeAPI returns an error.
type CubeAPIError struct {
	Status int
	Detail string
}

func (e *CubeAPIError) Error() string {
	return fmt.Sprintf("CubeAPI %d: %s", e.Status, e.Detail)
}

// CubeClient is the HTTP client for CubeAPI.
type CubeClient struct {
	BaseURL   string
	APIKey    string
	HTTP      *http.Client
	UserAgent string
}

func (c *CubeClient) BackendName() string { return "cube" }

func (c *CubeClient) Endpoint() string { return c.BaseURL }

func newCubeClient() *CubeClient {
	baseURL := envOr("CUBE_API_URL", "http://localhost:3000")
	apiKey := envOr("CUBE_API_KEY", "e2b_000000")
	return &CubeClient{
		BaseURL: baseURL,
		APIKey:  apiKey,
		HTTP: &http.Client{
			Timeout: 30 * time.Second,
		},
		UserAgent: "cube-mcp-go/1.0",
	}
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func (c *CubeClient) request(method, path string, body interface{}, params url.Values) (interface{}, error) {
	u := c.BaseURL + path
	if len(params) > 0 {
		u += "?" + params.Encode()
	}

	var bodyReader io.Reader
	if body != nil {
		jsonBytes, err := json.Marshal(body)
		if err != nil {
			return nil, fmt.Errorf("marshal error: %w", err)
		}
		bodyReader = bytes.NewReader(jsonBytes)
	}

	req, err := http.NewRequest(method, u, bodyReader)
	if err != nil {
		return nil, fmt.Errorf("request creation error: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.APIKey)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", c.UserAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(resp.Body)

	if resp.StatusCode >= 400 {
		return nil, &CubeAPIError{Status: resp.StatusCode, Detail: string(respBody)}
	}

	if resp.StatusCode == 204 || len(respBody) == 0 {
		return map[string]interface{}{}, nil
	}

	var result interface{}
	if err := json.Unmarshal(respBody, &result); err != nil {
		// Not JSON — return raw text
		return string(respBody), nil
	}
	return result, nil
}

// ---- Cluster endpoints ----

func (c *CubeClient) Health() (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/health", nil, nil)
}

func (c *CubeClient) ClusterOverview() (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/cluster/overview", nil, nil)
}

func (c *CubeClient) ClusterVersions() (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/cluster/versions", nil, nil)
}

func (c *CubeClient) ListNodes() (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/nodes", nil, nil)
}

func (c *CubeClient) GetNode(nodeID string) (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/nodes/"+nodeID, nil, nil)
}

// ---- Container lifecycle ----

func (c *CubeClient) ListSandboxes(state string, limit int) (interface{}, error) {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	if state != "" {
		params.Set("state", state)
	}
	return c.request("GET", "/cubeapi/v1/v2/sandboxes", nil, params)
}

func (c *CubeClient) GetSandbox(sandboxID string) (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/sandboxes/"+sandboxID, nil, nil)
}

func (c *CubeClient) KillSandbox(sandboxID string) (interface{}, error) {
	return c.request("DELETE", "/cubeapi/v1/sandboxes/"+sandboxID, nil, nil)
}

// RestartSandbox stops and starts a container without removing it.
func (c *CubeClient) RestartSandbox(sandboxID string) (interface{}, error) {
	return c.request("POST", "/cubeapi/v1/sandboxes/"+sandboxID+"/restart", nil, nil)
}

func (c *CubeClient) PauseSandbox(sandboxID string) (interface{}, error) {
	return c.request("POST", "/cubeapi/v1/sandboxes/"+sandboxID+"/pause", nil, nil)
}

func (c *CubeClient) ResumeSandbox(sandboxID string) (interface{}, error) {
	return c.request("POST", "/cubeapi/v1/sandboxes/"+sandboxID+"/resume", nil, nil)
}

func (c *CubeClient) GetSandboxLogs(sandboxID string, limit int) (interface{}, error) {
	params := url.Values{}
	params.Set("limit", fmt.Sprintf("%d", limit))
	return c.request("GET", "/cubeapi/v1/v2/sandboxes/"+sandboxID+"/logs", nil, params)
}

func (c *CubeClient) CreateSandbox(templateID string, memoryMB int, cpuCount float64, envVars, metadata map[string]interface{}) (interface{}, error) {
	body := map[string]interface{}{
		"templateID": templateID,
		"memoryMB":   memoryMB,
		"cpuCount":   cpuCount,
	}
	if envVars != nil {
		body["envVars"] = envVars
	}
	if metadata != nil {
		body["metadata"] = metadata
	}
	return c.request("POST", "/v1/sandboxes", body, nil)
}

// ---- Templates ----

func (c *CubeClient) ListTemplates() (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/templates", nil, nil)
}

func (c *CubeClient) GetTemplate(templateID string) (interface{}, error) {
	return c.request("GET", "/cubeapi/v1/templates/"+templateID, nil, nil)
}

func (c *CubeClient) CreateTemplateFromImage(image string, exposePorts []int, writableLayerSizeGB int, mounts []map[string]interface{}, envVars map[string]interface{}, startCmd string) (interface{}, error) {
	body := map[string]interface{}{
		"image":               image,
		"writableLayerSizeGB": writableLayerSizeGB,
	}
	if len(exposePorts) > 0 {
		body["exposePorts"] = exposePorts
	}
	if mounts != nil {
		body["mounts"] = mounts
	}
	if envVars != nil {
		body["envVars"] = envVars
	}
	if startCmd != "" {
		body["startCmd"] = startCmd
	}
	return c.request("POST", "/cubeapi/v1/templates", body, nil)
}

func (c *CubeClient) ExecInSandbox(sandboxID, command string, timeout int) (interface{}, error) {
	body := map[string]interface{}{
		"command": command,
		"timeout": timeout,
	}
	return c.request("POST", "/cubeapi/v1/v2/sandboxes/"+sandboxID+"/exec", body, nil)
}
