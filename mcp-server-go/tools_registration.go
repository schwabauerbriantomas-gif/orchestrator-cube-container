// Package main: tool registration — maps each MCP tool to its handler.
// Extracted from server.go for maintainability (AUDIT FIX L-02).
package main

import (
	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

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

	// --- Image lifecycle (5) — CI/CD pipeline: build → push → pull → tag ---
	s.AddTool(toolWithArgs("image_build", "Build a Docker image from a Dockerfile in a context directory. Sends the context as a tarball to the Docker build API. Args: context_dir (required, path to directory containing Dockerfile), tag (required, e.g. myapp:v1), dockerfile (default Dockerfile).",
		mcp.WithString("context_dir", mcp.Required(), mcp.Description("Path to directory containing the Dockerfile")),
		mcp.WithString("tag", mcp.Required(), mcp.Description("Image tag, e.g. myapp:v1")),
		mcp.WithString("dockerfile", mcp.Description("Dockerfile filename (default: Dockerfile)")),
	), handleImageBuild)
	s.AddTool(toolWithArgs("image_push", "Push an image to a registry. The image is tagged with the registry prefix first. Args: tag (required), registry (optional, e.g. registry.example.com:5000).",
		mcp.WithString("tag", mcp.Required()),
		mcp.WithString("registry", mcp.Description("Registry URL (default: Docker Hub)")),
	), handleImagePush)
	s.AddTool(toolWithArgs("image_pull", "Pull an image from a registry. Args: tag (required, e.g. postgres:16-alpine).",
		mcp.WithString("tag", mcp.Required()),
	), handleImagePull)
	s.AddTool(toolWithArgs("image_list", "List all Docker images on the node with tags, size, and creation date.",
		mcp.WithString("filter", mcp.Description("Filter images by name pattern")),
	), handleImageList)
	s.AddTool(toolWithArgs("image_tag", "Tag an existing image with a new name:tag. Args: source_tag (required), target_tag (required).",
		mcp.WithString("source_tag", mcp.Required()),
		mcp.WithString("target_tag", mcp.Required()),
	), handleImageTag)

	// --- Rolling deployment (1) — zero-downtime image updates ---
	s.AddTool(toolWithArgs("deploy_rollout", "Perform a rolling update of a service to a new image. Replaces replicas one-by-one (rolling) or all-at-once (blue-green), waiting for health checks between each. Aborts on failure if abort_on_failure=true. Args: service_name (required), new_image (required), strategy (rolling|blue-green, default rolling), health_wait_seconds (default 60), abort_on_failure (default true).",
		mcp.WithString("service_name", mcp.Required()),
		mcp.WithString("new_image", mcp.Required()),
		mcp.WithString("strategy", mcp.Description("rolling or blue-green (default: rolling)")),
		mcp.WithNumber("health_wait_seconds", mcp.Description("Seconds to wait for health check per replica (default: 60)")),
	), handleDeployRollout)

	// --- Log aggregation (2) — multi-container log search ---
	s.AddTool(toolWithArgs("logs_search", "Search logs across ALL (or specified) containers by text pattern, severity level, or time range. Returns matching log lines sorted by recency. Args: pattern (text to search), containers (list of IDs, empty=all), level (error|warn|info|debug), since_minutes, max_results (default 100).",
		mcp.WithString("pattern", mcp.Description("Text pattern to search for")),
		mcp.WithString("level", mcp.Description("Filter by level: error, warn, info, debug")),
		mcp.WithNumber("since_minutes", mcp.Description("Only search logs from last N minutes")),
		mcp.WithNumber("max_results", mcp.Description("Max results (default 100)")),
	), handleLogsSearch)
	s.AddTool(toolWithArgs("logs_aggregate", "Aggregate log statistics across containers. Returns error/warn/info counts per container, sorted by error count. Args: containers (list of IDs, empty=all), since_lines (lines to analyze per container, default 200).",
		mcp.WithString("level", mcp.Description("Aggregate specific level only")),
	), handleLogsAggregate)

	// --- Audit query (1) — investigate what happened ---
	s.AddTool(toolWithArgs("audit_query", "Search the tamper-evident audit log. Every tool invocation is recorded with a hash chain. Query by time range, tool name, role, or success/failure. Args: since_hours (default 24), tool_name, role, success (true/false), limit (default 100).",
		mcp.WithNumber("since_hours", mcp.Description("Hours of history to search (default: 24)")),
		mcp.WithString("tool_name", mcp.Description("Filter by tool name")),
		mcp.WithString("role", mcp.Description("Filter by caller role")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 100, max 1000)")),
	), handleAuditQuery)

	// --- Environments (4) — namespace isolation + promotion ---
	s.AddTool(toolWithArgs("env_create", "Create an isolated environment (namespace) for managing deployments. Default environments (dev, staging, prod) are auto-created. Args: name (required), description, protected (bool).",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("description", mcp.Description("Environment description")),
	), handleEnvCreate)
	s.AddTool(tool("env_list", "List all environments with container counts."), handleEnvList)
	s.AddTool(toolWithArgs("env_get", "Get details of a specific environment including its containers.",
		mcp.WithString("name", mcp.Required()),
	), handleEnvGet)
	s.AddTool(toolWithArgs("env_promote", "Promote a container from one environment to the next (e.g. dev → staging → prod). Creates a new container in the target with the same image, then removes the source. Args: source_env (required), target_env (required), container_id (required).",
		mcp.WithString("source_env", mcp.Required()),
		mcp.WithString("target_env", mcp.Required()),
		mcp.WithString("container_id", mcp.Required()),
	), handleEnvPromote)

	// --- Notifications (4) — alert delivery to external channels ---
	s.AddTool(toolWithArgs("notify_channel_add", "Register a notification channel for alert delivery. Supports Slack, Discord, Telegram, and Email. Args: name (required), type (slack|discord|telegram|email, required), webhook_url (for slack/discord), bot_token + chat_id (for telegram), email_to + smtp_host (for email).",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("type", mcp.Required(), mcp.Description("slack, discord, telegram, or email")),
		mcp.WithString("webhook_url", mcp.Description("Webhook URL for Slack/Discord")),
		mcp.WithString("bot_token", mcp.Description("Telegram bot token")),
		mcp.WithString("chat_id", mcp.Description("Telegram chat ID")),
	), handleNotifyChannelAdd)
	s.AddTool(tool("notify_channel_list", "List all configured notification channels."), handleNotifyChannelList)
	s.AddTool(toolWithArgs("notify_channel_remove", "Remove a notification channel.",
		mcp.WithString("channel_id", mcp.Required()),
	), handleNotifyChannelRemove)
	s.AddTool(toolWithArgs("notify_send", "Send a message to a notification channel. Args: channel_id (required), title (required), body (required), level (info|warning|critical).",
		mcp.WithString("channel_id", mcp.Required()),
		mcp.WithString("title", mcp.Required()),
		mcp.WithString("body", mcp.Required()),
		mcp.WithString("level", mcp.Description("info, warning, or critical")),
	), handleNotifySend)

	// --- API token management (3) — programmatic key rotation ---
	s.AddTool(toolWithArgs("auth_create_token", "Create a new API token with a specified role. The secret is returned ONLY once — store it securely. Args: role (required: viewer|operator|admin), label.",
		mcp.WithString("role", mcp.Required(), mcp.Description("viewer, operator, or admin")),
		mcp.WithString("label", mcp.Description("Label for this token")),
	), handleAuthCreateToken)
	s.AddTool(tool("auth_list_tokens", "List all API tokens with metadata (no secrets shown)."), handleAuthListTokens)
	s.AddTool(toolWithArgs("auth_revoke_token", "Revoke (disable) an API token by its key ID.",
		mcp.WithString("key", mcp.Required()),
	), handleAuthRevokeToken)

	// --- Scheduled jobs (4) — periodic task automation ---
	s.AddTool(toolWithArgs("job_create", "Create a scheduled job that runs an MCP tool periodically. Args: name (required), schedule (required, e.g. 'every 5m', 'hourly', 'daily', 'weekly'), tool (required, MCP tool name), args (object of tool arguments).",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("schedule", mcp.Required(), mcp.Description("every 5m, hourly, daily, weekly")),
		mcp.WithString("tool", mcp.Required(), mcp.Description("MCP tool to call")),
	), handleJobCreate)
	s.AddTool(tool("job_list", "List all scheduled jobs with next run time and status."), handleJobList)
	s.AddTool(toolWithArgs("job_remove", "Remove a scheduled job.",
		mcp.WithString("job_id", mcp.Required()),
	), handleJobRemove)
	s.AddTool(toolWithArgs("job_run", "Run a scheduled job immediately (outside its schedule).",
		mcp.WithString("job_id", mcp.Required()),
	), handleJobRun)

	// --- Metrics query (1) — programmatic metrics access ---
	s.AddTool(toolWithArgs("metrics_query", "Query current cluster metrics (CPU, memory, requests, tool calls). Filter by metric name or container. Args: metric (name pattern), container (filter), limit (default 50).",
		mcp.WithString("metric", mcp.Description("Metric name pattern to filter")),
		mcp.WithString("container", mcp.Description("Filter by container ID")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
	), handleMetricsQuery)

	// --- Database provisioning (3) — managed DB lifecycle ---
	s.AddTool(toolWithArgs("database_create", "Provision a managed database (Postgres, MySQL, Redis, MongoDB). Creates container, persistent volume, health check, and stores credentials as a secret. Args: name (required), type (required: postgres|mysql|redis|mongodb), version (e.g. 16), memory_mb (default 512).",
		mcp.WithString("name", mcp.Required()),
		mcp.WithString("type", mcp.Required(), mcp.Description("postgres, mysql, redis, mongodb")),
		mcp.WithString("version", mcp.Description("Image version tag (e.g. 16)")),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit in MB (default 512)")),
	), handleDatabaseCreate)
	s.AddTool(toolWithArgs("database_backup", "Create a backup of a managed database's volume.",
		mcp.WithString("database_id", mcp.Required()),
	), handleDatabaseBackup)
	s.AddTool(toolWithArgs("database_restore", "Restore a managed database from a backup.",
		mcp.WithString("database_id", mcp.Required()),
		mcp.WithString("backup_id", mcp.Required()),
	), handleDatabaseRestore)

	// --- Certificates (2) — TLS visibility ---
	s.AddTool(tool("cert_list", "List all TLS certificates with expiry dates and renewal status. Scans Caddy certificate store and CUBE_TLS_CERT."), handleCertList)
	s.AddTool(toolWithArgs("cert_renew", "Trigger certificate renewal check by reloading Caddy. Caddy auto-renews certs expiring within 30 days.",
		mcp.WithString("domain", mcp.Required()),
	), handleCertRenew)

	// --- Events (2) — cluster event stream ---
	s.AddTool(toolWithArgs("events_list", "List recent cluster events (container starts/stops, deploys, scaling, health failures, alerts). Filter by type, severity, or time range. Args: event_type, severity (info|warning|critical), since_minutes, limit (default 50).",
		mcp.WithString("event_type", mcp.Description("Filter by event type")),
		mcp.WithString("severity", mcp.Description("Filter: info, warning, critical")),
		mcp.WithNumber("since_minutes", mcp.Description("Events from last N minutes")),
		mcp.WithNumber("limit", mcp.Description("Max results (default 50)")),
	), handleEventsList)
	s.AddTool(tool("events_recent", "Get the 20 most recent cluster events across all types."), handleEventsRecent)

	// --- Secure sandbox (8) — KVM-isolated untrusted code execution ---
	// These tools only work with Cube backend (CUBE_BACKEND=cube).
	// Docker containers share the host kernel and cannot provide KVM isolation.
	s.AddTool(toolWithArgs("secure_sandbox_create", "Create a KVM-isolated sandbox for untrusted code. Each sandbox runs its own guest kernel — kernel escape is impossible. Supports egress filtering (domain allowlist/blocklist) and credential vault (API keys injected by proxy, never visible to sandbox code). Requires Cube backend. Args: template_id (required), memory_mb (default 512), cpu_count (default 1.0), egress_allowlist (domains the sandbox can reach), egress_blocklist (domains to block), credential_vault (map of domain→API key, injected on egress), max_lifetime_seconds (auto-pause after N seconds), network_disabled (bool, most secure).",
		mcp.WithString("template_id", mcp.Required()),
		mcp.WithNumber("memory_mb", mcp.Description("Memory limit in MB (default 512)")),
		mcp.WithNumber("cpu_count", mcp.Description("CPU cores (default 1.0)")),
	), handleSecureSandboxCreate)
	s.AddTool(toolWithArgs("secure_sandbox_exec", "Execute a command inside a secure sandbox. The command runs in the KVM-isolated environment with egress filtering enforced. Max timeout 300s for untrusted code. Args: sandbox_id (required), command (required), timeout_seconds (default 30, max 300).",
		mcp.WithString("sandbox_id", mcp.Required()),
		mcp.WithString("command", mcp.Required()),
		mcp.WithNumber("timeout_seconds", mcp.Description("Max execution time (default 30, max 300)")),
	), handleSecureSandboxExec)
	s.AddTool(toolWithArgs("secure_sandbox_egress_add", "Add a network egress rule to a secure sandbox. Only domains in the allowlist can be reached from inside the sandbox. Args: sandbox_id (required), domain (required), action (allow|block, required).",
		mcp.WithString("sandbox_id", mcp.Required()),
		mcp.WithString("domain", mcp.Required()),
		mcp.WithString("action", mcp.Required(), mcp.Description("allow or block")),
	), handleSecureSandboxEgressAdd)
	s.AddTool(toolWithArgs("secure_sandbox_egress_list", "List all egress rules for a secure sandbox.",
		mcp.WithString("sandbox_id", mcp.Required()),
	), handleSecureSandboxEgressList)
	s.AddTool(toolWithArgs("secure_sandbox_egress_remove", "Remove an egress rule from a secure sandbox.",
		mcp.WithString("rule_id", mcp.Required()),
	), handleSecureSandboxEgressRemove)
	s.AddTool(toolWithArgs("secure_sandbox_snapshot", "Create a CubeCoW snapshot of a secure sandbox. Allows instant rollback to a known-good state. Copy-on-Write means snapshots are near-instant and minimal storage. Args: sandbox_id (required).",
		mcp.WithString("sandbox_id", mcp.Required()),
	), handleSecureSandboxSnapshot)
	s.AddTool(toolWithArgs("secure_sandbox_restore", "Restore a secure sandbox to a previous snapshot. Rolls back the entire sandbox state (filesystem + memory) to the snapshot point. Args: sandbox_id (required), snapshot_id (required).",
		mcp.WithString("sandbox_id", mcp.Required()),
		mcp.WithString("snapshot_id", mcp.Required()),
	), handleSecureSandboxRestore)
	s.AddTool(toolWithArgs("secure_sandbox_list", "List all secure sandboxes with status, egress policy, and expiry.",
		mcp.WithString("state", mcp.Description("Filter: running, paused, stopped")),
	), handleSecureSandboxList)
}
