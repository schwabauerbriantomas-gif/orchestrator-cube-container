// Package main: mock CubeAPI server for end-to-end testing.
// Build with: go test -tags=e2e -v
//go:build e2e

package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

// TestE2E_FullWorkflow exercises all tools against a mock CubeAPI.
func TestE2E_FullWorkflow(t *testing.T) {
	// 1. Start mock CubeAPI
	mockAPI := newMockCubeAPI()
	defer mockAPI.Close()

	// 2. Set environment so MCP client talks to mock
	t.Setenv("CUBE_API_URL", mockAPI.URL)
	t.Setenv("CUBE_API_KEY", "test-key")

	// 3. Initialize the real client + deploy manager
	client = newCubeClient()
	deploy = newDeployManager(client)

	// 4. Test each cluster tool directly
	t.Run("cluster_health", func(t *testing.T) {
		data, err := client.Health()
		if err != nil {
			t.Fatal(err)
		}
		m := data.(map[string]interface{})
		if m["status"] != "healthy" {
			t.Fatalf("expected healthy, got %v", m["status"])
		}
		t.Logf("✓ health: %v", data)
	})

	t.Run("cluster_overview", func(t *testing.T) {
		data, err := client.ClusterOverview()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ overview: %v", data)
	})

	t.Run("list_nodes", func(t *testing.T) {
		data, err := client.ListNodes()
		if err != nil {
			t.Fatal(err)
		}
		nodes := data.([]interface{})
		if len(nodes) == 0 {
			t.Fatal("expected at least 1 node")
		}
		t.Logf("✓ nodes: %d", len(nodes))
	})

	t.Run("list_templates", func(t *testing.T) {
		data, err := client.ListTemplates()
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ templates: %v", data)
	})

	t.Run("create_and_kill_container", func(t *testing.T) {
		// Create
		data, err := client.CreateSandbox("tmpl-test", 256, 1.0, nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		m := data.(map[string]interface{})
		id := fmt.Sprintf("%v", m["sandboxID"])
		if id == "" {
			t.Fatal("no sandboxID in response")
		}
		t.Logf("✓ created container: %s", id)

		// Pause
		_, err = client.PauseSandbox(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ paused: %s", id)

		// Resume
		_, err = client.ResumeSandbox(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ resumed: %s", id)

		// Kill
		_, err = client.KillSandbox(id)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ killed: %s", id)
	})

	t.Run("exec_in_container", func(t *testing.T) {
		_, err := client.ExecInSandbox("any-id", "ls -la", 30)
		if err != nil {
			t.Fatal(err)
		}
		t.Logf("✓ exec worked")
	})
}

// TestE2E_Volumes tests volume management end-to-end.
func TestE2E_Volumes(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CUBE_VOLUMES_ROOT", dir+"/volumes")
	t.Setenv("CUBE_WORKSPACES_ROOT", dir+"/workspaces")

	deploy = newDeployManager(newCubeClient())

	// Create
	result, err := deploy.CreateVolume("test-vol")
	if err != nil {
		t.Fatal(err)
	}
	if result["status"] != "created" {
		t.Fatalf("expected created, got %v", result["status"])
	}
	t.Logf("✓ created volume: %v", result)

	// List
	volumes, err := deploy.ListVolumes()
	if err != nil {
		t.Fatal(err)
	}
	if len(volumes) != 1 {
		t.Fatalf("expected 1 volume, got %d", len(volumes))
	}
	t.Logf("✓ listed: %d volumes", len(volumes))

	// Delete
	delResult, err := deploy.DeleteVolume("test-vol")
	if err != nil {
		t.Fatal(err)
	}
	if delResult["status"] != "deleted" {
		t.Fatalf("expected deleted, got %v", delResult["status"])
	}
	t.Logf("✓ deleted volume")
}

// TestE2E_AuthHTTP tests the full auth pipeline over HTTP.
func TestE2E_AuthHTTP(t *testing.T) {
	// Set up key store in temp dir
	dir := t.TempDir()
	t.Setenv("CUBE_AUTH_KEYS_FILE", dir+"/keys.json")

	ks := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: dir + "/keys.json",
	}

	// Generate keys for each role
	viewerKey, _ := ks.GenerateKey(RoleViewer, "e2e-viewer")
	operatorKey, _ := ks.GenerateKey(RoleOperator, "e2e-operator")
	adminKey, _ := ks.GenerateKey(RoleAdmin, "e2e-admin")

	// Create a simple handler that checks auth
	limiter := newRateLimiter(100, time.Minute)
	auditFile := dir + "/audit.logl"
	al := &AuditLogger{file: mustOpenFile2(auditFile)}
	mw := newAuthMiddleware(ks, limiter, al)

	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"ok":true}`))
	}), func(r *http.Request) string { return "list_containers" })

	server := httptest.NewServer(handler)
	defer server.Close()

	// Test viewer can read
	t.Run("viewer_can_read", func(t *testing.T) {
		code := doAuthedRequest(t, server.URL, viewerKey, "list_containers")
		if code != 200 {
			t.Fatalf("expected 200, got %d", code)
		}
	})

	// Test viewer cannot deploy
	handler2 := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}), func(r *http.Request) string { return "deploy_from_git" })
	server2 := httptest.NewServer(handler2)
	defer server2.Close()

	t.Run("viewer_cannot_deploy", func(t *testing.T) {
		code := doAuthedRequest(t, server2.URL, viewerKey, "deploy_from_git")
		if code != 403 {
			t.Fatalf("expected 403, got %d", code)
		}
	})

	// Test operator can deploy
	t.Run("operator_can_deploy", func(t *testing.T) {
		code := doAuthedRequest(t, server2.URL, operatorKey, "deploy_from_git")
		if code != 200 {
			t.Fatalf("expected 200, got %d", code)
		}
	})

	// Test operator cannot delete_volume
	handler3 := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}), func(r *http.Request) string { return "delete_volume" })
	server3 := httptest.NewServer(handler3)
	defer server3.Close()

	t.Run("operator_cannot_delete", func(t *testing.T) {
		code := doAuthedRequest(t, server3.URL, operatorKey, "delete_volume")
		if code != 403 {
			t.Fatalf("expected 403, got %d", code)
		}
	})

	// Test admin can do anything
	t.Run("admin_can_delete", func(t *testing.T) {
		code := doAuthedRequest(t, server3.URL, adminKey, "delete_volume")
		if code != 200 {
			t.Fatalf("expected 200, got %d", code)
		}
	})

	// Test invalid auth
	t.Run("invalid_auth", func(t *testing.T) {
		code := doAuthedRequest(t, server.URL, &APIKey{Key: "bad", Secret: "bad"}, "list_containers")
		if code != 401 {
			t.Fatalf("expected 401, got %d", code)
		}
	})

	// Verify audit chain integrity
	t.Run("audit_integrity", func(t *testing.T) {
		al.file.Sync()
		count, err := VerifyAuditChain(auditFile)
		if err != nil {
			t.Fatalf("audit chain broken: %v", err)
		}
		t.Logf("✓ audit chain verified: %d entries", count)
	})
}

// TestE2E_RateLimit verifies rate limiting triggers.
func TestE2E_RateLimit(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CUBE_AUTH_KEYS_FILE", dir+"/keys.json")

	ks := &KeyStore{
		keys:     make(map[string]*APIKey),
		filePath: dir + "/keys.json",
	}
	k, _ := ks.GenerateKey(RoleViewer, "rate-test")

	// Very tight limit: 3 per minute
	limiter := newRateLimiter(3, time.Minute)
	mw := newAuthMiddleware(ks, limiter, &AuditLogger{})

	handler := mw.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}), nil)

	server := httptest.NewServer(handler)
	defer server.Close()

	// First 3 pass
	for i := 0; i < 3; i++ {
		code := doAuthedRequest(t, server.URL, k, "list_containers")
		if code != 200 {
			t.Fatalf("request %d: expected 200, got %d", i+1, code)
		}
	}
	t.Logf("✓ first 3 requests allowed")

	// 4th should be 429
	code := doAuthedRequest(t, server.URL, k, "list_containers")
	if code != 429 {
		t.Fatalf("4th request: expected 429, got %d", code)
	}
	t.Logf("✓ 4th request rate limited (429)")
}

// ---- Mock CubeAPI ----

func newMockCubeAPI() *httptest.Server {
	mux := http.NewServeMux()

	mux.HandleFunc("/cubeapi/v1/health", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, map[string]string{"status": "healthy", "version": "1.0"})
	})
	mux.HandleFunc("/cubeapi/v1/cluster/overview", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, map[string]interface{}{
			"nodes": 3, "running": 5, "paused": 2,
			"cpu_total": 12, "ram_total_mb": 12288,
		})
	})
	mux.HandleFunc("/cubeapi/v1/cluster/versions", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, map[string]string{
			"cubeapi": "1.0", "cubemaster": "1.0", "cubelet": "1.0",
		})
	})
	mux.HandleFunc("/cubeapi/v1/nodes", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, []map[string]interface{}{
			{"id": "node-1", "cpu_count": 4, "memory_mb": 4096},
			{"id": "node-2", "cpu_count": 4, "memory_mb": 4096},
			{"id": "node-3", "cpu_count": 4, "memory_mb": 4096},
		})
	})
	mux.HandleFunc("/cubeapi/v1/nodes/", func(w http.ResponseWriter, r *http.Request) {
		nodeID := strings.TrimPrefix(r.URL.Path, "/cubeapi/v1/nodes/")
		writeMockJSON(w, map[string]interface{}{"id": nodeID, "status": "online"})
	})
	mux.HandleFunc("/cubeapi/v1/v2/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" {
			writeMockJSON(w, []map[string]interface{}{
				{"id": "sbx-001", "state": "running"},
			})
			return
		}
	})
	mux.HandleFunc("/cubeapi/v1/sandboxes/", func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(r.URL.Path, "/")
		id := parts[len(parts)-1]
		if r.Method == "POST" && strings.Contains(r.URL.Path, "/pause") {
			writeMockJSON(w, map[string]string{"id": id, "state": "paused"})
		} else if r.Method == "POST" && strings.Contains(r.URL.Path, "/resume") {
			writeMockJSON(w, map[string]string{"id": id, "state": "running"})
		} else if r.Method == "DELETE" {
			writeMockJSON(w, map[string]string{"id": id, "state": "killed"})
		} else {
			writeMockJSON(w, map[string]string{"id": id, "state": "running"})
		}
	})
	mux.HandleFunc("/v1/sandboxes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writeMockJSON(w, map[string]interface{}{
				"sandboxID": "sbx-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000),
				"state":     "running",
			})
		}
	})
	mux.HandleFunc("/cubeapi/v1/templates", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "POST" {
			writeMockJSON(w, map[string]interface{}{
				"templateID": "tmpl-" + fmt.Sprintf("%d", time.Now().UnixNano()%10000),
			})
			return
		}
		writeMockJSON(w, []map[string]interface{}{
			{"id": "tmpl-python", "image": "python:3.12-slim"},
		})
	})

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeMockJSON(w, map[string]string{"mock": "ok", "path": r.URL.Path})
	})

	return httptest.NewServer(mux)
}

func writeMockJSON(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(data)
}

func doAuthedRequest(t *testing.T, url string, key *APIKey, toolName string) int {
	body := fmt.Sprintf(`{"jsonrpc":"2.0","method":"tools/call","params":{"name":"%s","arguments":{}}}`, toolName)
	req, _ := http.NewRequest("POST", url, strings.NewReader(body))
	req.Header.Set("X-API-Key", key.Key)
	req.Header.Set("X-API-Secret", key.Secret)
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

func mustOpenFile2(path string) *os.File {
	return mustOpenFile(path)
}
