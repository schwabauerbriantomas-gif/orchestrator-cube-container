// Package main: MCP server exposing Cube Container cluster operations.
// 161 tools across the full DevOps lifecycle: containers, images, deploy,
// scaling, health, networking, routing, secrets, backup, HA, multi-node,
// environments, notifications, jobs, databases, certificates, events.
//
// AUDIT FIX L-02: Tool registration moved to tools_registration.go.
// Basic handlers moved to handlers_basic.go.
// Helper functions moved to tools_helpers.go.
// This file now contains only main() and HTTP middleware wiring.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/mark3labs/mcp-go/server"
)

var (
	client          ContainerBackend
	deploy          *DeployManager
	keyStore        *KeyStore
	backupMgr       *BackupManager
	metricsCollector *MetricsCollector
	routeMgr        *RouteManager
	netMgr          *NetworkManager
	version         = "1.0.0"
)

func main() {
	mode := flag.String("mode", "stdio", "Server mode: stdio or http")
	port := flag.Int("port", 8080, "HTTP port (only used in http mode)")
	genKey := flag.String("gen-key", "", "Generate a new API key: viewer|operator|admin")
	keyLabel := flag.String("label", "", "Label for generated key")
	revokeKey := flag.String("revoke-key", "", "Revoke an API key by ID")
	verifyAudit := flag.String("verify-audit", "", "Verify audit log integrity (path to .logl file)")
	flag.Parse()

	// Admin subcommands
	if *genKey != "" {
		keyStore := newKeyStore()
		role := Role(*genKey)
		if role != RoleViewer && role != RoleOperator && role != RoleAdmin {
			fmt.Fprintf(os.Stderr, "invalid role: %s (use viewer, operator, or admin)\n", *genKey)
			os.Exit(1)
		}
		k, err := keyStore.GenerateKey(role, *keyLabel)
		if err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("Key:    %s\n", k.Key)
		fmt.Printf("Secret: %s\n", k.Secret)
		fmt.Printf("Role:   %s\n", k.Role)
		os.Exit(0)
	}
	if *revokeKey != "" {
		keyStore := newKeyStore()
		if err := keyStore.Revoke(*revokeKey); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("revoked")
		os.Exit(0)
	}
	if *verifyAudit != "" {
		count, err := VerifyAuditChain(*verifyAudit)
		if err != nil {
			fmt.Fprintf(os.Stderr, "FAIL: %v\n", err)
			os.Exit(1)
		}
		fmt.Printf("OK: %d entries verified\n", count)
		os.Exit(0)
	}

	client = newBackend()
	deploy = newDeployManager(client)
	keyStore = newKeyStore()
	backupMgr = newBackupManager(deploy, client)
	metricsCollector = newMetricsCollector()
	routeMgr = newRouteManager()
	versionMgr = newVersionManager(deploy)
	netMgr = newNetworkManager()

	// Proxmox VE backend — initialized from env vars (optional)
	initProxmoxBackend()

	// Health check manager — runs probes and auto-restarts failed containers
	healthMgr = newHealthManager(client)

	// Multi-node registry — cluster catalog of physical machines
	nodeRegistry = newNodeRegistry()

	// Scaling manager — replica groups + load balancing
	scaleMgr = newScaleManager(client)

	// Alerting — monitoring rules + webhook notifications
	alertMgr = newAlertManager()

	// ConfigMap manager — non-sensitive configuration data
	configMgr = newConfigMapManager()

	// Service discovery — logical name → endpoint registry
	discoveryMgr = newDiscoveryManager()

	// Volume lifecycle — attach/detach/migrate beyond basic create/delete
	volumeMgr = newVolumeManager(deploy, client)

	// Resource limits — enforce memory/CPU quotas on containers
	resourceMgr = newResourceManager()

	// Garbage collector — prevent disk exhaustion on edge nodes
	gc = newGarbageCollector()

	// Secrets manager (optional — degrades gracefully if key unavailable)
	sm, err := newSecretsManager()
	if err != nil {
		fmt.Fprintf(os.Stderr, "[cube-mcp] WARNING: secrets manager disabled: %v\n", err)
	} else {
		secretsMgr = sm
		// AS-7: Use the same secrets key to HMAC the audit chain, making it
		// tamper-proof against full-file rewrites.
		if key := getSecretsKeyForAudit(); key != nil {
			auditMACKey = key
		}
	}

	// HA manager (active-passive CubeMaster failover)
	haManager = newHAManager()

	// Start health watcher (auto-restart failed containers)
	if healthMgr != nil {
		healthMgr.Start()
	}

	// Start alert watcher (evaluate monitoring rules)
	if alertMgr != nil {
		alertMgr.Start()
	}

	// Start garbage collector (auto-prune when disk > threshold)
	if gc != nil {
		gc.Start()
	}

	// ---- Phase 2 managers (DevOps-complete feature set) ----
	secureSandboxMgr = newSecureSandboxManager(client)
	imageMgr = newImageManager(client)
	rolloutMgr = newRolloutManager(client)
	logAggMgr = newLogAggregationManager(client)
	auditQueryMgr = newAuditQueryManager()
	envMgr = newEnvironmentManager()
	notifyMgr = newNotificationManager()
	jobMgr = newJobManager()
	metricsQueryMgr = newMetricsQueryManager(metricsCollector)
	dbMgr = newDatabaseManager()
	certMgr = newCertificateManager()
	eventMgr = newEventManager()

	// Start job scheduler
	if jobMgr != nil {
		jobMgr.Start()
	}

	s := server.NewMCPServer(
		"cube-container-mcp",
		version,
		server.WithToolCapabilities(false),
	)

	registerAllTools(s)

	switch *mode {
	case "stdio":
		fmt.Fprintf(os.Stderr, "[cube-mcp] stdio mode → backend=%s endpoint=%s\n", client.BackendName(), client.Endpoint())
		if err := server.ServeStdio(s); err != nil {
			fmt.Fprintf(os.Stderr, "[cube-mcp] error: %v\n", err)
			os.Exit(1)
		}
	case "http":
		fmt.Fprintf(os.Stderr, "[cube-mcp] HTTP mode on :%d → backend=%s endpoint=%s\n", *port, client.BackendName(), client.Endpoint())
		limiter := newRateLimiter(120, time.Minute)
		audit := newAuditLogger()
		middleware := newAuthMiddleware(keyStore, limiter, audit)
		adminAPI := &AuthAdminAPI{keys: keyStore}

		// The MCP streamable HTTP server from mcp-go handles /mcp
		mcpHTTP := server.NewStreamableHTTPServer(s)
		mcpHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			mcpHTTP.ServeHTTP(w, r)
		})

		// Wrap MCP with auth (tool name extracted from JSON body for RBAC)
		authedMCP := middleware.Wrap(mcpHandler, extractToolFromRequest)

		mux := http.NewServeMux()
		mux.Handle("/mcp", authedMCP)

		// Admin key management — requires admin auth
		mux.Handle("/auth/keys", middleware.RequireAdmin(adminAPI))
		mux.Handle("/auth/keys/", middleware.RequireAdmin(adminAPI))

		mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
			w.Write([]byte(`{"status":"ok"}`))
		})

		// Metrics — requires viewer auth (cluster state is sensitive)
		mux.Handle("/metrics", middleware.RequireRole(http.HandlerFunc(metricsHandler), RoleViewer))

		// Container logs streaming — requires viewer auth
		mux.Handle("/streams/", middleware.RequireRole(http.HandlerFunc(handleLogStream), RoleViewer))

		// Git webhook — uses its own secret validation (constant-time)
		mux.HandleFunc("/webhook/git", handleGitWebhook)

		// HA endpoints — requires viewer auth
		if haManager != nil {
			mux.Handle("/ha/heartbeat", http.HandlerFunc(haManager.HandleHeartbeat))
			mux.Handle("/ha/state", middleware.RequireRole(http.HandlerFunc(haManager.HandleHAGetState), RoleViewer))
		}

		// Wrap the entire mux with body size limiting + per-IP connection limit
		limitedMux := withBodyLimit(mux)

		addr := fmt.Sprintf(":%d", *port)
		fmt.Fprintf(os.Stderr, "[cube-mcp] listening on %s\n", addr)
		fmt.Fprintf(os.Stderr, "[cube-mcp] endpoints: POST /mcp, GET /health, POST /auth/keys\n")

		// Listener with per-IP connection limit (B2).
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "[cube-mcp] error: %v\n", err)
			os.Exit(1)
		}
		ln = &maxConnsListener{Listener: ln, limit: maxConnsPerIP}

		httpServer := &http.Server{
			Addr:              addr,
			Handler:           limitedMux,
			ReadHeaderTimeout: 10 * time.Second,
			ReadTimeout:       30 * time.Second,
			WriteTimeout:      0, // SSE streams need no write timeout
			IdleTimeout:       120 * time.Second,
			MaxHeaderBytes:    1 << 20, // 1MB max headers
		}

		// TLS support (M5): if cert and key files are provided, use TLS directly.
		certFile := os.Getenv("CUBE_TLS_CERT")
		keyFile := os.Getenv("CUBE_TLS_KEY")
		if certFile != "" && keyFile != "" {
			fmt.Fprintf(os.Stderr, "[cube-mcp] TLS enabled: cert=%s key=%s\n", certFile, keyFile)
			if err := httpServer.ServeTLS(ln, certFile, keyFile); err != nil {
				fmt.Fprintf(os.Stderr, "[cube-mcp] error: %v\n", err)
				os.Exit(1)
			}
		} else {
			// R9-AUTH-06: Warn when running in plaintext HTTP mode — credentials traverse the network in cleartext.
			fmt.Fprintf(os.Stderr, "[cube-mcp] ⚠ WARNING: running in plaintext HTTP mode (no TLS). "+
				"API keys, secrets, and TOTP codes will be sent in cleartext. "+
				"Set CUBE_TLS_CERT and CUBE_TLS_KEY, or use the Caddy reverse proxy with TLS.\n")
			if err := httpServer.Serve(ln); err != nil {
				fmt.Fprintf(os.Stderr, "[cube-mcp] error: %v\n", err)
				os.Exit(1)
			}
		}
	default:
		fmt.Fprintf(os.Stderr, "unknown mode: %s (use stdio or http)\n", *mode)
		os.Exit(1)
	}
}

// extractToolFromRequest reads the JSON-RPC body to find the tool name.
// This is needed for RBAC checks before the tool executes.
func extractToolFromRequest(r *http.Request) string {
	if r.Body == nil {
		return ""
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		return ""
	}
	r.Body = io.NopCloser(bytes.NewReader(body))

	var payload struct {
		Method string `json:"method"`
		Params struct {
			Name      string `json:"name"`
			Arguments map[string]interface{} `json:"arguments"`
		} `json:"params"`
	}
	if err := json.Unmarshal(body, &payload); err != nil {
		return ""
	}
	if payload.Method == "tools/call" {
		metricsCollector.IncToolCall(payload.Params.Name)
		return payload.Params.Name
	}
	return ""
}

// ---- Tool registration and handlers are in separate files ----
// See: tools_registration.go, handlers_basic.go, handlers_phase2.go,
//       handlers_secure.go, tools_helpers.go

