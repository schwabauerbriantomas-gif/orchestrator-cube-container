//go:build e2e

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

// BenchmarkToolCallHTTP measures HTTP tool call latency through the full stack:
// client → auth middleware → MCP handler → CubeAPI mock
func BenchmarkToolCallHTTP(b *testing.B) {
	mockAPI := newMockCubeAPI()
	defer mockAPI.Close()
	os.Setenv("CUBE_API_URL", mockAPI.URL)
	os.Setenv("CUBE_API_KEY", "test")
	client = newCubeClient()

	dir := b.TempDir()
	ks := &KeyStore{keys: make(map[string]*APIKey), filePath: dir + "/k.json"}
	k, _ := ks.GenerateKey(RoleAdmin, "bench")

	limiter := newRateLimiter(1000000, time.Hour)
	mw := newAuthMiddleware(ks, limiter, &AuditLogger{})

	mcpServer := setupBenchMCP()
	httpSrv := server.NewStreamableHTTPServer(mcpServer)
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpSrv.ServeHTTP(w, r)
	}), extractToolFromRequest)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	body := `{"jsonrpc":"2.0","method":"tools/call","params":{"name":"cluster_health","arguments":{}}}`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(body))
		req.Header.Set("X-API-Key", k.Key)
		req.Header.Set("X-API-Secret", k.Secret)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			b.Fatal(err)
		}
		resp.Body.Close()
	}
}

// BenchmarkAuthValidate measures key validation latency.
func BenchmarkAuthValidate(b *testing.B) {
	dir := b.TempDir()
	ks := &KeyStore{keys: make(map[string]*APIKey), filePath: dir + "/k.json"}
	k, _ := ks.GenerateKey(RoleOperator, "bench")

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ks.Validate(k.Key, k.Secret)
	}
}

// BenchmarkRBACCheck measures RBAC permission check latency.
func BenchmarkRBACCheck(b *testing.B) {
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		canExecute(RoleOperator, "deploy_from_git")
	}
}

// BenchmarkAuditLog measures audit logging latency (with file I/O).
func BenchmarkAuditLog(b *testing.B) {
	dir := b.TempDir()
	al := &AuditLogger{file: mustOpenFile(dir + "/audit.logl")}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		al.Log(AuditEntry{
			Timestamp:  "2026-01-01T00:00:00Z",
			Key:        "cc_live_bench***",
			Role:       "operator",
			Method:     "POST",
			Path:       "/mcp",
			StatusCode: 200,
			Duration:   "1ms",
			Allowed:    true,
		})
	}
}

// BenchmarkRateLimiter measures rate limit check latency.
func BenchmarkRateLimiter(b *testing.B) {
	rl := newRateLimiter(1000000, time.Hour)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rl.Allow("bench-key")
	}
}

// BenchmarkGitURLValidation measures input validation latency.
func BenchmarkGitURLValidation(b *testing.B) {
	url := "https://github.com/user/repo.git"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		validateGitURL(url)
	}
}

// BenchmarkCommandValidation measures command validation latency.
func BenchmarkCommandValidation(b *testing.B) {
	cmd := "pip install -r requirements.txt && cd /app && uvicorn main:app --host 0.0.0.0 --port 8000"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		validateCommand(cmd)
	}
}

// TestPerformance_BinarySize measures the compiled binary size.
func TestPerformance_BinarySize(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping binary size check in short mode")
	}
	binary := "/tmp/cube-mcp-bench"
	cmd := exec.Command("go", "build", "-ldflags", "-s -w", "-o", binary, ".")
	if err := cmd.Run(); err != nil {
		t.Skipf("could not build: %v", err)
	}
	info, _ := os.Stat(binary)
	sizeMB := float64(info.Size()) / (1024 * 1024)
	t.Logf("Binary size: %.2f MB (%d bytes)", sizeMB, info.Size())
	os.Remove(binary)
}

// TestPerformance_StartupTime measures process startup + MCP initialize response time.
func TestPerformance_StartupTime(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping startup test in short mode")
	}
	binary := "/tmp/cube-mcp-startup"
	exec.Command("go", "build", "-o", binary, ".").Run()
	defer os.Remove(binary)

	// Send initialize + a 500ms sleep + close stdin
	initMsg := `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{},"clientInfo":{"name":"bench","version":"1.0"}}}`

	start := time.Now()
	cmd := exec.Command(binary, "-mode", "stdio")

	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}

	// Write init message
	stdin.Write([]byte(initMsg + "\n"))

	// Wait for response to appear in stdout
	time.Sleep(300 * time.Millisecond)
	elapsed := time.Since(start)

	// Close stdin to let the process exit
	stdin.Close()
	cmd.Wait()

	t.Logf("Startup + initialize response: %v", elapsed)

	// Verify we got a valid response
	var resp map[string]interface{}
	if err := json.Unmarshal(bytes.TrimSpace(stdout.Bytes()), &resp); err != nil {
		t.Fatalf("invalid response: %s", stdout.String())
	}
	result, ok := resp["result"].(map[string]interface{})
	if !ok {
		t.Fatal("no result in response")
	}
	serverInfo, ok := result["serverInfo"].(map[string]interface{})
	if !ok {
		t.Fatal("no serverInfo")
	}
	if serverInfo["name"] != "cube-container-mcp" {
		t.Fatalf("wrong server name: %v", serverInfo["name"])
	}
	t.Logf("Server: %s v%s", serverInfo["name"], serverInfo["version"])
}

// TestPerformance_HTTPLatency measures round-trip latency for HTTP mode.
func TestPerformance_HTTPLatency(t *testing.T) {
	mockAPI := newMockCubeAPI()
	defer mockAPI.Close()
	os.Setenv("CUBE_API_URL", mockAPI.URL)
	client = newCubeClient()

	dir := t.TempDir()
	ks := &KeyStore{keys: make(map[string]*APIKey), filePath: dir + "/k.json"}
	k, _ := ks.GenerateKey(RoleAdmin, "latency")

	limiter := newRateLimiter(1000000, time.Hour)
	mw := newAuthMiddleware(ks, limiter, &AuditLogger{})

	mcpServer := setupBenchMCP()
	httpSrv := server.NewStreamableHTTPServer(mcpServer)
	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		httpSrv.ServeHTTP(w, r)
	}), extractToolFromRequest)

	srv := httptest.NewServer(handler)
	defer srv.Close()

	// Warm up
	for i := 0; i < 10; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"cluster_health","arguments":{}}}`))
		req.Header.Set("X-API-Key", k.Key)
		req.Header.Set("X-API-Secret", k.Secret)
		resp, _ := http.DefaultClient.Do(req)
		resp.Body.Close()
	}

	// Measure 100 requests
	latencies := make([]time.Duration, 100)
	for i := 0; i < 100; i++ {
		req, _ := http.NewRequest("POST", srv.URL+"/mcp", strings.NewReader(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"cluster_health","arguments":{}}}`))
		req.Header.Set("X-API-Key", k.Key)
		req.Header.Set("X-API-Secret", k.Secret)

		start := time.Now()
		resp, err := http.DefaultClient.Do(req)
		latencies[i] = time.Since(start)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
	}

	// Calculate stats
	var total time.Duration
	min := latencies[0]
	max := latencies[0]
	for _, d := range latencies {
		total += d
		if d < min {
			min = d
		}
		if d > max {
			max = d
		}
	}
	avg := total / time.Duration(len(latencies))
	p99 := latencies[int(float64(len(latencies))*0.99)]

	t.Logf("HTTP latency (100 requests, full auth stack):")
	t.Logf("  Min: %v", min)
	t.Logf("  Avg: %v", avg)
	t.Logf("  Max: %v", max)
	t.Logf("  P99: %v", p99)
	t.Logf("  RPS: %.1f", 1000.0/avg.Seconds())
}

// TestPerformance_MemoryBaseline measures RSS of the running process.
func TestPerformance_MemoryBaseline(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping memory test in short mode")
	}
	binary := "/tmp/cube-mcp-mem"
	exec.Command("go", "build", "-o", binary, ".").Run()
	defer os.Remove(binary)

	// Start in HTTP mode
	cmd := exec.Command(binary, "-mode", "http", "-port", "18099")
	cmd.Start()
	defer cmd.Process.Kill()

	time.Sleep(500 * time.Millisecond) // let it start

	// Read /proc/PID/status
	data, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", cmd.Process.Pid))
	if err != nil {
		t.Skipf("could not read process status: %v", err)
	}

	for _, line := range strings.Split(string(data), "\n") {
		if strings.HasPrefix(line, "VmRSS") || strings.HasPrefix(line, "VmSize") {
			t.Logf("  %s", strings.TrimSpace(line))
		}
	}
}

func setupBenchMCP() *server.MCPServer {
	client = newCubeClient()
	deploy = newDeployManager(client)
	s := server.NewMCPServer("cube-bench", "1.0", server.WithToolCapabilities(false))
	registerAllTools(s)
	return s
}
