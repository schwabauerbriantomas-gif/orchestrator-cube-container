// Package main: MCP server exposing Cube Container cluster operations.
// Port of server.py — dual-mode stdio + HTTP, 22 tools.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/mark3labs/mcp-go/mcp"
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

// ---- Tool registration ----

func registerAllTools(s *server.MCPServer) {
	// --- Cluster (5) ---
	s.AddTool(tool("cluster_health", "Check if the Cube Container cluster API is reachable and healthy."), handleClusterHealth)
	s.AddTool(tool("cluster_overview", "Get cluster capacity overview: total nodes, running containers, CPU/RAM usage."), handleClusterOverview)
	s.AddTool(tool("cluster_versions", "Get version matrix of all cluster components (CubeAPI, CubeMaster, Cubelet)."), handleClusterVersions)
	s.AddTool(tool("list_nodes", "List all nodes in the cluster with their resource capacity and current load."), handleListNodes)
	s.AddTool(toolWithArgs("get_node", "Get detailed info for a specific node.",
		mcp.WithString("node_id", mcp.Required(), mcp.Description("Node ID")),
	), handleGetNode)
	s.AddTool(toolWithArgs("suggest_node", "Suggest the best node for a new container based on resource availability. Returns top-3 candidates with bin-packing scores. Pass required memory_mb and cpu_count to filter.",
		mcp.WithNumber("memory_mb", mcp.Description("Required memory in MB")),
		mcp.WithNumber("cpu_count", mcp.Description("Required CPU cores")),
	), handleSuggestNode)

	// --- Container lifecycle (7) ---
	s.AddTool(toolWithArgs("list_containers", "List running containers (sandboxes) with optional filters. Args: state (running/paused/stopped), limit (default 50).",
		mcp.WithString("state", mcp.Description("Filter: running, paused, or stopped")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
	), handleListContainers)
	s.AddTool(toolWithArgs("get_container", "Get detailed info for a specific container by ID.",
		mcp.WithString("container_id", mcp.Required()),
	), handleGetContainer)
	s.AddTool(toolWithArgs("create_container", "Create and start a new container from a template. Args: template_id (required), memory_mb (default 512), cpu_count (default 1.0), env_vars, metadata.",
		mcp.WithString("template_id", mcp.Required()),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit in MB (default 512)")),
		mcp.WithNumber("cpu_count", mcp.Description("CPU cores (default 1.0)")),
	), handleCreateContainer)
	s.AddTool(toolWithArgs("kill_container", "Stop and remove a container by ID.",
		mcp.WithString("container_id", mcp.Required()),
	), handleKillContainer)
	s.AddTool(toolWithArgs("pause_container", "Freeze a container (cgroup freezer). Uses ~0 CPU while paused. Resume with resume_container.",
		mcp.WithString("container_id", mcp.Required()),
	), handlePauseContainer)
	s.AddTool(toolWithArgs("resume_container", "Resume (un-freeze) a paused container. Typically resumes in ~15ms.",
		mcp.WithString("container_id", mcp.Required()),
	), handleResumeContainer)
	s.AddTool(toolWithArgs("get_container_logs", "Fetch recent logs from a container.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Max log lines (default 100)")),
	), handleGetContainerLogs)
	s.AddTool(toolWithArgs("tail_container_logs", "Get the last N log lines from a container (one-shot). For real-time streaming use the SSE endpoint at GET /streams/{container_id}/logs.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithNumber("limit", mcp.Description("Number of lines (default 50)")),
	), handleTailLogs)

	// --- Templates (3) ---
	s.AddTool(tool("list_templates", "List all available container templates."), handleListTemplates)
	s.AddTool(toolWithArgs("create_template", "Create a new template from an OCI image. Args: image (required, e.g. 'python:3.12-slim'), expose_ports, writable_layer_size_gb.",
		mcp.WithString("image", mcp.Required(), mcp.Description("OCI image reference")),
	), handleCreateTemplate)
	s.AddTool(toolWithArgs("get_template", "Get details of a specific template.",
		mcp.WithString("template_id", mcp.Required()),
	), handleGetTemplate)

	// --- Persistent deploy (4) ---
	s.AddTool(toolWithArgs("deploy_from_git", "Deploy from a git repo with persistent storage. Flow: clone → volume → template → container. Code survives restarts.",
		mcp.WithString("git_url", mcp.Required(), mcp.Description("Repository URL")),
		mcp.WithString("branch", mcp.Description("Git branch (default main)")),
		mcp.WithString("image", mcp.Description("Base OCI image (default python:3.12-slim)")),
		mcp.WithString("start_cmd", mcp.Description("Override auto-detected start command")),
		mcp.WithString("volume_name", mcp.Description("Existing volume name")),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit (default 256)")),
	), handleDeployFromGit)
	s.AddTool(toolWithArgs("deploy_from_code", "Deploy from inline code files (no git needed). Files written to persistent volume.",
		mcp.WithString("app_name", mcp.Required()),
	), handleDeployFromCode)
	s.AddTool(toolWithArgs("update_code", "Pull latest code from git and sync to the container's volume. Sends restart signal.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("git_url", mcp.Required()),
		mcp.WithString("branch", mcp.Description("Git branch (default main)")),
	), handleUpdateCode)
	s.AddTool(toolWithArgs("exec_in_container", "Execute a command inside a running container. Returns stdout, stderr, exit code.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("command", mcp.Required()),
		mcp.WithNumber("timeout", mcp.Description("Timeout in seconds (default 30)")),
	), handleExecInContainer)

	// --- Volumes (3) ---
	s.AddTool(tool("list_volumes", "List all persistent volumes with size and file count."), handleListVolumes)
	s.AddTool(toolWithArgs("create_volume", "Create a new persistent volume directory.",
		mcp.WithString("name", mcp.Required()),
	), handleCreateVolume)
	s.AddTool(toolWithArgs("delete_volume", "Delete a persistent volume. WARNING: destroys all data permanently.",
		mcp.WithString("name", mcp.Required()),
	), handleDeleteVolume)

	// --- Volume lifecycle: attach/detach/migrate/info (4) ---
	s.AddTool(toolWithArgs("volume_attach", "Attach a persistent volume to a running container at a specified mount path.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("volume_name", mcp.Required()),
		mcp.WithString("mount_path", mcp.Required()),
	), handleVolumeAttach)
	s.AddTool(toolWithArgs("volume_detach", "Detach a volume from a container. The volume data is preserved.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("volume_name", mcp.Required()),
	), handleVolumeDetach)
	s.AddTool(toolWithArgs("volume_migrate", "Migrate a volume from this node to a remote node via tar+scp. Requires SSH access to the target node.",
		mcp.WithString("volume_name", mcp.Required()),
		mcp.WithString("from_node", mcp.Description("Source node (default: this node)")),
		mcp.WithString("to_node", mcp.Required(), mcp.Description("Target node host:port")),
	), handleVolumeMigrate)
	s.AddTool(toolWithArgs("volume_info", "Get detailed volume info: size, file count, attached containers.",
		mcp.WithString("volume_name", mcp.Required()),
	), handleVolumeInfo)

	// --- Resource limits (4) ---
	s.AddTool(toolWithArgs("resource_set_limits", "Apply hard memory and CPU limits to a running container via docker update. Limits are persisted and re-applied on restart.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit in MB")),
		mcp.WithNumber("cpu_count", mcp.Description("CPU cores (e.g. 0.5, 1.0, 2.0)")),
	), handleResourceSetLimits)
	s.AddTool(toolWithArgs("resource_get_usage", "Get real-time CPU, memory, network, and disk I/O usage for a specific container.",
		mcp.WithString("container_id", mcp.Required()),
	), handleResourceGetUsage)
	s.AddTool(tool("resource_list_usage", "Get real-time resource usage for ALL running containers. Returns CPU%, memory, network I/O, and PID count per container."), handleResourceListUsage)
	s.AddTool(tool("resource_quota_summary", "Get aggregated resource allocation vs node capacity: total allocated memory/CPU, utilization percentage, and remaining headroom."), handleResourceQuotaSummary)

	// --- Garbage collection (3) ---
	s.AddTool(tool("gc_prune_images", "Remove unused and dangling Docker images older than 7 days. Frees disk space on edge nodes with limited storage."), handleGCPruneImages)
	s.AddTool(tool("gc_prune_volumes", "Remove orphaned Docker volumes not attached to any running container. Also identifies deploy-managed volumes with no active container."), handleGCPruneVolumes)
	s.AddTool(tool("gc_disk_usage", "Show disk usage breakdown: images, containers, volumes — total count, active, size, and reclaimable space."), handleGCDiskUsage)

	// --- Backup & Restore (5) ---
	s.AddTool(toolWithArgs("backup_volume", "Create a tar.gz backup of a volume with SHA256 integrity check. Backup is stored locally and can be restored later.",
		mcp.WithString("volume_name", mcp.Required(), mcp.Description("Name of the volume to backup")),
	), handleBackupVolume)
	s.AddTool(toolWithArgs("backup_container", "Create a full backup of a container: config manifest + all mounted volumes. Allows full recovery on restore.",
		mcp.WithString("container_id", mcp.Required()),
	), handleBackupContainer)
	s.AddTool(tool("list_backups", "List all available backups with size, checksum, and restorable status."), handleListBackups)
	s.AddTool(toolWithArgs("restore_backup", "Restore a backup by ID. Verifies SHA256 integrity before restoring. For containers, recreates the container from manifest.",
		mcp.WithString("backup_id", mcp.Required()),
	), handleRestoreBackup)
	s.AddTool(toolWithArgs("delete_backup", "Delete a backup file and its manifest permanently.",
		mcp.WithString("backup_id", mcp.Required()),
	), handleDeleteBackup)

	// --- Deploy versioning & rollback (2) ---
	s.AddTool(toolWithArgs("rollback_deploy", "Rollback a deployment to its previous version. Redeploys from the prior git commit.",
		mcp.WithString("app_name", mcp.Required()),
	), handleRollbackDeploy)
	s.AddTool(toolWithArgs("list_deploy_versions", "List all deployment versions for an app.",
		mcp.WithString("app_name", mcp.Required()),
	), handleListVersions)

	// --- Routing & automatic TLS (3) ---
	s.AddTool(toolWithArgs("create_route", "Create a domain route with automatic TLS. Caddy obtains Let's Encrypt certificates automatically. The domain must point to this server's IP.",
		mcp.WithString("domain", mcp.Required()),
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithNumber("target_port", mcp.Required()),
		mcp.WithString("path_prefix", mcp.Description("Optional path prefix, e.g. /api")),
	), handleCreateRoute)
	s.AddTool(toolWithArgs("delete_route", "Remove a domain route and its TLS certificate.",
		mcp.WithString("domain", mcp.Required()),
	), handleDeleteRoute)
	s.AddTool(tool("list_routes", "List all configured domain routes with TLS status."), handleListRoutes)
	s.AddTool(tool("reload_routes", "Force regenerate the Caddy route config and reload Caddy. Use after manual config changes or if routes appear stale."), handleReloadRoutes)

	// --- Networking (9) ---
	s.AddTool(toolWithArgs("add_port_mapping", "Map a host port to a container port. Allows external access to a container service.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithNumber("host_port", mcp.Required()),
		mcp.WithNumber("container_port", mcp.Required()),
		mcp.WithString("protocol", mcp.Description("tcp or udp (default tcp)")),
		mcp.WithString("host_ip", mcp.Description("Bind IP (default 0.0.0.0)")),
	), handleAddPortMapping)
	s.AddTool(toolWithArgs("remove_port_mapping", "Remove a port mapping by ID.",
		mcp.WithString("mapping_id", mcp.Required()),
	), handleRemovePortMapping)
	s.AddTool(tool("list_port_mappings", "List all port mappings."), handleListPortMappings)
	s.AddTool(toolWithArgs("add_dns_alias", "Add a DNS alias pointing to a container. Creates /etc/hosts entry.",
		mcp.WithString("alias", mcp.Required(), mcp.Description("FQDN e.g. myapp.cube.local")),
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("target", mcp.Required(), mcp.Description("Container IP address")),
	), handleAddDNSAlias)
	s.AddTool(toolWithArgs("remove_dns_alias", "Remove a DNS alias.",
		mcp.WithString("alias", mcp.Required()),
	), handleRemoveDNSAlias)
	s.AddTool(tool("list_dns_aliases", "List all DNS aliases."), handleListDNSAliases)
	s.AddTool(toolWithArgs("add_network_policy", "Add a network policy (allow/deny) between containers.",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("source_container", mcp.Description("Source container ID")),
		mcp.WithString("dest_container", mcp.Description("Destination container ID")),
		mcp.WithString("action", mcp.Required(), mcp.Description("allow or deny")),
		mcp.WithString("protocol", mcp.Description("tcp or udp (default tcp)")),
		mcp.WithNumber("port", mcp.Description("Port number (0 = all)")),
	), handleAddNetworkPolicy)
	s.AddTool(tool("list_network_policies", "List all network policies."), handleListNetworkPolicies)
	s.AddTool(toolWithArgs("remove_network_policy", "Remove a network policy by ID.",
		mcp.WithString("policy_id", mcp.Required()),
	), handleRemoveNetworkPolicy)

	// Backend introspection — lets the model know which runtime is active.
	s.AddTool(tool("backend_info", "Get information about the active container backend (docker or cube). Returns backend name, endpoint, and capabilities. Use this to understand which container runtime the MCP server is operating on."), handleBackendInfo)

	// --- Multi-node cluster management (6) ---
	s.AddTool(toolWithArgs("node_add", "Register a new cluster node. Args: id (required, unique identifier), address (required, host:port), backend (docker or cube), hostname, memory_mb, cpu_cores, disk_gb, state (active/draining/offline, default active).",
		mcp.WithString("id", mcp.Required(), mcp.Description("Unique node ID")),
		mcp.WithString("address", mcp.Required(), mcp.Description("host:port for remote API access")),
		mcp.WithString("backend", mcp.Description("docker or cube (default cube)")),
		mcp.WithString("hostname", mcp.Description("Human-readable hostname")),
		mcp.WithNumber("memory_mb", mcp.Description("Total RAM in MB")),
		mcp.WithNumber("cpu_cores", mcp.Description("Total CPU cores")),
		mcp.WithNumber("disk_gb", mcp.Description("Total disk in GB")),
	), handleNodeAdd)
	s.AddTool(toolWithArgs("node_update", "Update a registered node's properties (address, state, resources).",
		mcp.WithString("id", mcp.Required()),
	), handleNodeUpdate)
	s.AddTool(toolWithArgs("node_remove", "Remove a node from the cluster registry. Containers on the node are NOT affected.",
		mcp.WithString("id", mcp.Required()),
	), handleNodeRemove)
	s.AddTool(tool("node_list", "List all registered cluster nodes with state, resources, and backend type."), handleNodeList)
	s.AddTool(toolWithArgs("node_get", "Get detailed information about a specific node.",
		mcp.WithString("id", mcp.Required()),
	), handleNodeGet)
	s.AddTool(toolWithArgs("deploy_to_node", "Deploy a container to a specific remote node. Creates a remote backend connection to the node and runs the container there. Use suggest_node first to pick the best node.",
		mcp.WithString("node_id", mcp.Required()),
		mcp.WithString("template_id", mcp.Required()),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit in MB (default 512)")),
		mcp.WithNumber("cpu_count", mcp.Description("CPU cores (default 1.0)")),
	), handleDeployToNode)

	// --- Horizontal scaling (5) ---
	s.AddTool(toolWithArgs("service_create", "Define a new scalable service (group of replica containers). Does NOT create replicas yet — use scale_set to add them. Args: name (required), template_id (required), port (internal port, default 8000), memory_mb (default 256), cpu_count (default 1.0), domain (optional — enables load-balanced routing).",
		mcp.WithString("name", mcp.Required(), mcp.Description("Unique service name")),
		mcp.WithString("template_id", mcp.Required()),
		mcp.WithNumber("port", mcp.Description("Internal container port (default 8000)")),
		mcp.WithNumber("memory_mb", mcp.Description("Memory per replica in MB (default 256)")),
		mcp.WithNumber("cpu_count", mcp.Description("CPU cores per replica (default 1.0)")),
		mcp.WithString("domain", mcp.Description("Optional domain for load-balanced routing")),
	), handleServiceCreate)
	s.AddTool(toolWithArgs("scale_set", "Set the exact number of replicas for a service. Creates or removes containers to match the desired count. If the service has a domain, the load balancer is updated automatically.",
		mcp.WithString("name", mcp.Required()),
		mcp.WithNumber("replicas", mcp.Required(), mcp.Description("Desired replica count (0-20)")),
	), handleScaleSet)
	s.AddTool(tool("service_list", "List all scalable services with current replica counts and container IDs."), handleServiceList)
	s.AddTool(toolWithArgs("service_get", "Get detailed information about a scalable service.",
		mcp.WithString("name", mcp.Required()),
	), handleServiceGet)
	s.AddTool(toolWithArgs("service_remove", "Remove a service definition. Containers are NOT killed automatically.",
		mcp.WithString("name", mcp.Required()),
	), handleServiceRemove)

	// --- Service discovery (4) ---
	s.AddTool(toolWithArgs("service_register", "Register or update a service endpoint in the discovery registry. Maps a logical name to a container's host:port. Args: name (required), container_id (required), host (default localhost), port (required).",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("host", mcp.Description("Host (default localhost)")),
		mcp.WithNumber("port", mcp.Required()),
	), handleDiscoveryRegister)
	s.AddTool(toolWithArgs("service_deregister", "Remove a service from the discovery registry.",
		mcp.WithString("name", mcp.Required()),
	), handleDiscoveryDeregister)
	s.AddTool(toolWithArgs("service_resolve", "Look up a service by logical name. Returns host:port.",
		mcp.WithString("name", mcp.Required()),
	), handleDiscoveryResolve)
	s.AddTool(toolWithArgs("service_entries", "List all registered service discovery entries. Pass sync=true to first refresh from running container labels.",
		mcp.WithString("sync", mcp.Description("Set to 'true' to sync from container labels first")),
	), handleServiceListEntries)

	// --- High availability (1) ---
	s.AddTool(tool("ha_state", "Get the current high-availability state of this CubeMaster node: role (active/standby), active node ID, peer health, and failover timing."), handleHAState)

	// --- Health checks & auto-restart (4) ---
	s.AddTool(toolWithArgs("health_check_set", "Configure a health check probe for a container. Supports HTTP (GET 2xx/3xx), TCP (connection), and exec (exit 0) probes. When the probe fails consecutively beyond the threshold, the container is automatically restarted. Args: container_id (required), type (http|tcp|exec, required), interval_seconds (default 30), timeout_seconds (default 5), failure_threshold (default 3). For http: host, http_port, http_path (default /), http_scheme (default http). For tcp: host, tcp_port. For exec: exec_command.",
		mcp.WithString("container_id", mcp.Required()),
		mcp.WithString("type", mcp.Required(), mcp.Description("Probe type: http, tcp, or exec")),
		mcp.WithNumber("interval_seconds", mcp.Description("Check interval (default 30, min 5)")),
		mcp.WithNumber("timeout_seconds", mcp.Description("Probe timeout (default 5)")),
		mcp.WithNumber("failure_threshold", mcp.Description("Consecutive failures before restart (default 3)")),
		mcp.WithString("host", mcp.Description("Target host (default localhost)")),
		mcp.WithString("http_path", mcp.Description("HTTP probe path (default /)")),
		mcp.WithNumber("http_port", mcp.Description("HTTP probe port (required for type=http)")),
		mcp.WithString("http_scheme", mcp.Description("http or https (default http)")),
		mcp.WithNumber("tcp_port", mcp.Description("TCP probe port (required for type=tcp)")),
		mcp.WithString("exec_command", mcp.Description("Command to run inside container (required for type=exec)")),
	), handleHealthCheckSet)
	s.AddTool(toolWithArgs("health_check_remove", "Remove a health check probe from a container. Auto-restart will stop.",
		mcp.WithString("container_id", mcp.Required()),
	), handleHealthCheckRemove)
	s.AddTool(tool("health_check_list", "List all configured health checks with current status, failure counts, and restart counts."), handleHealthCheckList)
	s.AddTool(toolWithArgs("health_check_status", "Get detailed health check status for a specific container, including last error and probe configuration.",
		mcp.WithString("container_id", mcp.Required()),
	), handleHealthCheckStatus)

	// --- Secrets management (4) ---
	s.AddTool(toolWithArgs("secret_set", "Store an encrypted secret (API keys, passwords, tokens). Value is encrypted at rest with AES-256-GCM. RBAC: admin only.",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("value", mcp.Required()),
	), handleSecretSet)
	s.AddTool(toolWithArgs("secret_get", "Decrypt and retrieve a secret by name. RBAC: operator.",
		mcp.WithString("name", mcp.Required()),
	), handleSecretGet)
	s.AddTool(tool("secret_list", "List all stored secrets with metadata (name, version, timestamps). Does NOT reveal values. RBAC: viewer."), handleSecretList)
	s.AddTool(toolWithArgs("secret_delete", "Permanently delete a secret. RBAC: admin.",
		mcp.WithString("name", mcp.Required()),
	), handleSecretDelete)

	// --- Alerting (4) ---
	s.AddTool(toolWithArgs("alert_rule_add", "Create an alert rule. Types: container_down, cpu_high, disk_high, mem_high. Args: id (required), name (required), type (required), severity (critical/warning/info, default warning), threshold (e.g. 80 for 80%), container_id (for container_down), node_id (for cpu/disk/mem), webhook_url (optional override), cooldown_sec (default 300).",
		mcp.WithString("id", mcp.Required()),
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("type", mcp.Required(), mcp.Description("container_down, cpu_high, disk_high, or mem_high")),
		mcp.WithString("severity", mcp.Description("critical, warning, or info (default warning)")),
		mcp.WithNumber("threshold", mcp.Description("Threshold value (e.g. 80 for 80%)")),
		mcp.WithString("container_id", mcp.Description("Container ID for container_down rules")),
		mcp.WithString("node_id", mcp.Description("Node ID for cpu/disk/mem rules")),
		mcp.WithString("webhook_url", mcp.Description("Webhook URL override (default uses CUBE_ALERT_WEBHOOK)")),
		mcp.WithNumber("cooldown_sec", mcp.Description("Min seconds between alert repeats (default 300)")),
	), handleAlertRuleAdd)
	s.AddTool(toolWithArgs("alert_rule_remove", "Remove an alert rule.",
		mcp.WithString("id", mcp.Required()),
	), handleAlertRuleRemove)
	s.AddTool(tool("alert_list", "List all alert rules with severity, fire count, and last fired timestamp."), handleAlertList)
	s.AddTool(toolWithArgs("alert_test", "Fire a test alert to verify webhook configuration.",
		mcp.WithString("id", mcp.Required()),
	), handleAlertTest)

	// --- ConfigMaps (5) ---
	s.AddTool(toolWithArgs("configmap_create", "Create a ConfigMap with non-sensitive configuration data (env vars, feature flags). For sensitive data use secret_set instead.",
		mcp.WithString("name", mcp.Required()),
	), handleConfigMapCreate)
	s.AddTool(toolWithArgs("configmap_update", "Update a ConfigMap. By default merges new keys with existing data. Use mode=replace to overwrite entirely.",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("mode", mcp.Description("merge (default) or replace")),
	), handleConfigMapUpdate)
	s.AddTool(toolWithArgs("configmap_get", "Get a ConfigMap with all its data values.",
		mcp.WithString("name", mcp.Required()),
	), handleConfigMapGet)
	s.AddTool(tool("configmap_list", "List all ConfigMaps with key count and last updated timestamp."), handleConfigMapList)
	s.AddTool(toolWithArgs("configmap_remove", "Remove a ConfigMap permanently.",
		mcp.WithString("name", mcp.Required()),
	), handleConfigMapRemove)
}

// ---- Tool builders ----

func tool(name, desc string) mcp.Tool {
	return mcp.NewTool(name, mcp.WithDescription(desc))
}

func toolWithArgs(name, desc string, opts ...mcp.ToolOption) mcp.Tool {
	allOpts := append([]mcp.ToolOption{mcp.WithDescription(desc)}, opts...)
	return mcp.NewTool(name, allOpts...)
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

// ---- Tool handlers: Backend introspection ----

func handleBackendInfo(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	info := map[string]interface{}{
		"backend":  client.BackendName(),
		"endpoint": client.Endpoint(),
		"features": []string{
			"container_lifecycle", "templates", "deploy_from_git", "deploy_from_code",
			"volumes", "backup", "rollback", "routing_tls", "networking", "exec",
		},
		"tool_count": 44,
	}
	return okResult(info), nil
}

// ---- Tool handlers: Cluster ----

func handleClusterHealth(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.Health()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleClusterOverview(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ClusterOverview()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleClusterVersions(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ClusterVersions()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListNodes(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ListNodes()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetNode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	nodeID := argString(args, "node_id")
	if nodeID == "" {
		return errResult("node_id is required"), nil
	}
	data, err := client.GetNode(nodeID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Containers ----

func handleListContainers(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	state := argString(args, "state")
	limit := argInt(args, "limit", 50)
	data, err := client.ListSandboxes(state, limit)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.GetSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	tmplID := argString(args, "template_id")
	if tmplID == "" {
		return errResult("template_id is required"), nil
	}
	memMB := argInt(args, "memory_mb", 512)
	cpuCount := argFloat(args, "cpu_count", 1.0)
	envVars := argMap(args, "env_vars")
	metadata := argMap(args, "metadata")
	data, err := client.CreateSandbox(tmplID, memMB, cpuCount, envVars, metadata)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleKillContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.KillSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handlePauseContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.PauseSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleResumeContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	data, err := client.ResumeSandbox(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetContainerLogs(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "container_id")
	if id == "" {
		return errResult("container_id is required"), nil
	}
	limit := argInt(args, "limit", 100)
	data, err := client.GetSandboxLogs(id, limit)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Templates ----

func handleListTemplates(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := client.ListTemplates()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateTemplate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	image := argString(args, "image")
	if image == "" {
		return errResult("image is required"), nil
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	layerGB := argInt(args, "writable_layer_size_gb", 1)
	data, err := client.CreateTemplateFromImage(image, ports, layerGB, nil, nil, "")
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleGetTemplate(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	id := argString(args, "template_id")
	if id == "" {
		return errResult("template_id is required"), nil
	}
	data, err := client.GetTemplate(id)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Deploy ----

func handleDeployFromGit(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	gitURL := argString(args, "git_url")
	if gitURL == "" {
		return errResult("git_url is required"), nil
	}
	branch := argString(args, "branch")
	if branch == "" {
		branch = "main"
	}
	image := argString(args, "image")
	if image == "" {
		image = "python:3.12-slim"
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	envVars := argMap(args, "env_vars")
	startCmd := argString(args, "start_cmd")
	volumeName := argString(args, "volume_name")
	memMB := argInt(args, "memory_mb", 256)

	data, err := deploy.DeployFromGit(gitURL, branch, image, ports, envVars, startCmd, volumeName, memMB, 1.0)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeployFromCode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	appName := argString(args, "app_name")
	if appName == "" {
		return errResult("app_name is required"), nil
	}
	// files is a map of filename→content
	files := make(map[string]string)
	if rawFiles, ok := args["files"].(map[string]interface{}); ok {
		for k, v := range rawFiles {
			files[k] = fmt.Sprintf("%v", v)
		}
	}
	if len(files) == 0 {
		return errResult("files is required (and must not be empty)"), nil
	}
	image := argString(args, "image")
	if image == "" {
		image = "python:3.12-slim"
	}
	ports := argIntSlice(args, "expose_ports")
	if len(ports) == 0 {
		ports = []int{8000}
	}
	envVars := argMap(args, "env_vars")
	startCmd := argString(args, "start_cmd")
	memMB := argInt(args, "memory_mb", 256)

	data, err := deploy.DeployFromCode(appName, files, image, ports, envVars, startCmd, memMB)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleUpdateCode(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	gitURL := argString(args, "git_url")
	if gitURL == "" {
		return errResult("git_url is required"), nil
	}
	branch := argString(args, "branch")
	if branch == "" {
		branch = "main"
	}

	data, err := deploy.UpdateCode(containerID, gitURL, branch)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleExecInContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	command := argString(args, "command")
	if command == "" {
		return errResult("command is required"), nil
	}
	// Validate command
	if _, err := validateCommand(command); err != nil {
		return errResult(err.Error()), nil
	}
	timeout := argInt(args, "timeout", 30)
	data, err := client.ExecInSandbox(containerID, command, timeout)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Volumes ----

func handleListVolumes(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := deploy.ListVolumes()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleCreateVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	data, err := deploy.CreateVolume(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeleteVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	name := argString(args, "name")
	if name == "" {
		return errResult("name is required"), nil
	}
	data, err := deploy.DeleteVolume(name)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

// ---- Tool handlers: Backup & Restore ----

func handleBackupVolume(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	volName := argString(args, "volume_name")
	if volName == "" {
		return errResult("volume_name is required"), nil
	}
	data, err := backupMgr.BackupVolume(volName)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleBackupContainer(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	containerID := argString(args, "container_id")
	if containerID == "" {
		return errResult("container_id is required"), nil
	}
	data, err := backupMgr.BackupContainer(containerID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleListBackups(_ context.Context, _ mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	data, err := backupMgr.ListBackups()
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleRestoreBackup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	backupID := argString(args, "backup_id")
	if backupID == "" {
		return errResult("backup_id is required"), nil
	}
	data, err := backupMgr.RestoreBackup(backupID)
	if err != nil {
		return unwrapError(err), nil
	}
	return okResult(data), nil
}

func handleDeleteBackup(_ context.Context, req mcp.CallToolRequest) (*mcp.CallToolResult, error) {
	args := parseArgs(req)
	backupID := argString(args, "backup_id")
	if backupID == "" {
		return errResult("backup_id is required"), nil
	}
	if err := backupMgr.DeleteBackup(backupID); err != nil {
		return unwrapError(err), nil
	}
	return okResult(map[string]interface{}{"backup_id": backupID, "status": "deleted"}), nil
}

// ---- Misc ----

// Ensure strings package is used (for future expansion)
var _ = strings.TrimSpace
var _ = json.Marshal
