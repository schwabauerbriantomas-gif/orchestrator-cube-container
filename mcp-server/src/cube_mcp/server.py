"""MCP server exposing Cube Container cluster operations as tools.

Two modes:
    1. stdio (default): Local agent control, no auth needed.
    2. HTTP: Remote agent control, auth handled by auth_gateway.

Usage:
    # stdio mode (local)
    export CUBE_API_URL="http://localhost:3000"
    cube-mcp

    # HTTP mode (remote, behind Caddy + auth_gateway)
    export CUBE_MCP_MODE=http
    cube-mcp  # listens on :8080
"""
from __future__ import annotations

import asyncio
import json
import os
import traceback

from mcp.server import Server
from mcp.server.stdio import stdio_server
from mcp.types import TextContent, Tool

from .client import CubeAPIError, CubeContainerClient
from .deploy import DeployManager
from .security import validate_command as _validate_command

server = Server("cube-container-mcp")
_client: CubeContainerClient | None = None
_deploy_mgr: DeployManager | None = None


def get_client() -> CubeContainerClient:
    global _client
    if _client is None:
        _client = CubeContainerClient(
            base_url=os.environ.get("CUBE_API_URL", "http://localhost:3000"),
            api_key=os.environ.get("CUBE_API_KEY", "e2b_000000"),
        )
    return _client


def get_deploy_manager() -> DeployManager:
    global _deploy_mgr
    if _deploy_mgr is None:
        _deploy_mgr = DeployManager(client=get_client())
    return _deploy_mgr


def _ok(data: object) -> list[TextContent]:
    return [TextContent(type="text", text=json.dumps(data, default=str, indent=2))]


def _err(msg: str) -> list[TextContent]:
    return [TextContent(type="text", text=f"Error: {msg}")]


TOOLS = [
    # --- Cluster ---
    Tool(
        name="cluster_health",
        description="Check if the Cube Container cluster API is reachable and healthy.",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="cluster_overview",
        description="Get cluster capacity overview: total nodes, running containers, CPU/RAM usage.",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="cluster_versions",
        description="Get version matrix of all cluster components (CubeAPI, CubeMaster, Cubelet).",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="list_nodes",
        description="List all nodes in the cluster with their resource capacity and current load.",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="get_node",
        description="Get detailed info for a specific node.",
        inputSchema={
            "type": "object",
            "properties": {"node_id": {"type": "string"}},
            "required": ["node_id"],
        },
    ),
    # --- Container lifecycle ---
    Tool(
        name="list_containers",
        description="List running containers (sandboxes) with optional filters.\n\n"
        "Args:\n"
        "  state: Filter by state: 'running', 'paused', 'stopped'\n"
        "  limit: Max results (default 50)",
        inputSchema={
            "type": "object",
            "properties": {
                "state": {"type": "string", "enum": ["running", "paused", "stopped"]},
                "limit": {"type": "integer", "default": 50},
            },
        },
    ),
    Tool(
        name="get_container",
        description="Get detailed info for a specific container by ID.",
        inputSchema={
            "type": "object",
            "properties": {"container_id": {"type": "string"}},
            "required": ["container_id"],
        },
    ),
    Tool(
        name="create_container",
        description=(
            "Create and start a new container from a template.\n\n"
            "Args:\n"
            "  template_id: Template ID to deploy (use list_templates to see available)\n"
            "  memory_mb: Memory limit in MB (default 512)\n"
            "  cpu_count: CPU cores (default 1.0)\n"
            "  env_vars: Environment variables dict\n"
            "  metadata: Custom metadata dict for tagging"
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "template_id": {"type": "string"},
                "memory_mb": {"type": "integer", "default": 512},
                "cpu_count": {"type": "number", "default": 1.0},
                "env_vars": {"type": "object"},
                "metadata": {"type": "object"},
            },
            "required": ["template_id"],
        },
    ),
    Tool(
        name="kill_container",
        description="Stop and remove a container by ID.",
        inputSchema={
            "type": "object",
            "properties": {"container_id": {"type": "string"}},
            "required": ["container_id"],
        },
    ),
    Tool(
        name="pause_container",
        description="Freeze a container (cgroup freezer). Uses ~0 CPU while paused. "
        "The container's memory is preserved. Resume with resume_container.",
        inputSchema={
            "type": "object",
            "properties": {"container_id": {"type": "string"}},
            "required": ["container_id"],
        },
    ),
    Tool(
        name="resume_container",
        description="Resume (un-freeze) a paused container. Typically resumes in ~15ms.",
        inputSchema={
            "type": "object",
            "properties": {"container_id": {"type": "string"}},
            "required": ["container_id"],
        },
    ),
    Tool(
        name="get_container_logs",
        description="Fetch recent logs from a container.",
        inputSchema={
            "type": "object",
            "properties": {
                "container_id": {"type": "string"},
                "limit": {"type": "integer", "default": 100},
            },
            "required": ["container_id"],
        },
    ),
    # --- Templates ---
    Tool(
        name="list_templates",
        description="List all available container templates (pre-built images ready to deploy).",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="create_template",
        description=(
            "Create a new container template from an OCI image.\n\n"
            "Args:\n"
            "  image: OCI image reference, e.g. 'python:3.12-slim', 'nginx:alpine'\n"
            "  expose_ports: Ports to expose (e.g. [8000, 3000])\n"
            "  writable_layer_size_gb: Writable overlay size in GB (default 1)"
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "image": {"type": "string"},
                "expose_ports": {"type": "array", "items": {"type": "integer"}},
                "writable_layer_size_gb": {"type": "integer", "default": 1},
            },
            "required": ["image"],
        },
    ),
    Tool(
        name="get_template",
        description="Get details of a specific template.",
        inputSchema={
            "type": "object",
            "properties": {"template_id": {"type": "string"}},
            "required": ["template_id"],
        },
    ),
    # --- Persistent deploy ---
    Tool(
        name="list_volumes",
        description="List all persistent volumes with size and file count. Volumes survive container restarts.",
        inputSchema={"type": "object", "properties": {}, "required": []},
    ),
    Tool(
        name="create_volume",
        description="Create a new persistent volume directory on the node.\n\n"
        "The volume will be mounted into containers and persists across container restarts.",
        inputSchema={
            "type": "object",
            "properties": {
                "name": {"type": "string"},
                "size_hint_gb": {"type": "integer", "default": 1},
            },
            "required": ["name"],
        },
    ),
    Tool(
        name="delete_volume",
        description="Delete a persistent volume. WARNING: destroys all data permanently.",
        inputSchema={
            "type": "object",
            "properties": {"name": {"type": "string"}},
            "required": ["name"],
        },
    ),
    Tool(
        name="deploy_from_git",
        description=(
            "Deploy a service from a git repository with persistent storage.\n\n"
            "Flow: clone repo → create volume → copy code → create template → start container.\n"
            "Code survives container restarts via volume mount.\n\n"
            "Args:\n"
            "  git_url: Repository URL (e.g. https://github.com/user/repo)\n"
            "  branch: Git branch (default 'main')\n"
            "  image: Base OCI image (default 'python:3.12-slim')\n"
            "  expose_ports: Ports to expose (default [8000])\n"
            "  env_vars: Environment variables dict\n"
            "  start_cmd: Override auto-detected start command\n"
            "  volume_name: Existing volume name (auto-derived from repo if omitted)\n"
            "  memory_mb: Memory limit (default 256)"
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "git_url": {"type": "string"},
                "branch": {"type": "string", "default": "main"},
                "image": {"type": "string", "default": "python:3.12-slim"},
                "expose_ports": {"type": "array", "items": {"type": "integer"}},
                "env_vars": {"type": "object"},
                "start_cmd": {"type": "string"},
                "volume_name": {"type": "string"},
                "memory_mb": {"type": "integer", "default": 256},
            },
            "required": ["git_url"],
        },
    ),
    Tool(
        name="deploy_from_code",
        description=(
            "Deploy a service from inline code files (no git needed).\n\n"
            "Files are written to a persistent volume and a container is created.\n"
            "Code persists across container restarts.\n\n"
            "Args:\n"
            "  app_name: Name for the app (becomes volume name)\n"
            "  files: Dict of {filename: content}, e.g. {\"main.py\": \"...\", \"requirements.txt\": \"...\"}\n"
            "  image: Base OCI image (default 'python:3.12-slim')\n"
            "  expose_ports: Ports to expose (default [8000])\n"
            "  env_vars: Environment variables\n"
            "  start_cmd: Override auto-detected start command\n"
            "  memory_mb: Memory limit (default 256)"
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "app_name": {"type": "string"},
                "files": {"type": "object"},
                "image": {"type": "string", "default": "python:3.12-slim"},
                "expose_ports": {"type": "array", "items": {"type": "integer"}},
                "env_vars": {"type": "object"},
                "start_cmd": {"type": "string"},
                "memory_mb": {"type": "integer", "default": 256},
            },
            "required": ["app_name", "files"],
        },
    ),
    Tool(
        name="update_code",
        description=(
            "Pull latest code from git and sync to the container's volume.\n\n"
            "After updating, sends a restart signal to the app inside the container.\n"
            "Use this to update a running service without recreating the container."
        ),
        inputSchema={
            "type": "object",
            "properties": {
                "container_id": {"type": "string"},
                "git_url": {"type": "string"},
                "branch": {"type": "string", "default": "main"},
            },
            "required": ["container_id", "git_url"],
        },
    ),
    Tool(
        name="exec_in_container",
        description="Execute a command inside a running container. Returns stdout, stderr, exit code.",
        inputSchema={
            "type": "object",
            "properties": {
                "container_id": {"type": "string"},
                "command": {"type": "string"},
                "timeout": {"type": "integer", "default": 30},
            },
            "required": ["container_id", "command"],
        },
    ),
]


async def handle_tool(name: str, args: dict) -> list[TextContent]:
    c = get_client()
    try:
        match name:
            # Cluster
            case "cluster_health":
                result = await c.health()
            case "cluster_overview":
                result = await c.cluster_overview()
            case "cluster_versions":
                result = await c.cluster_versions()
            case "list_nodes":
                result = await c.list_nodes()
            case "get_node":
                result = await c.get_node(args["node_id"])
            # Containers
            case "list_containers":
                result = await c.list_sandboxes(
                    state=args.get("state"),
                    limit=args.get("limit", 50),
                )
            case "get_container":
                result = await c.get_sandbox(args["container_id"])
            case "create_container":
                result = await c.create_sandbox(
                    template_id=args["template_id"],
                    memory_mb=args.get("memory_mb", 512),
                    cpu_count=args.get("cpu_count", 1.0),
                    env_vars=args.get("env_vars"),
                    metadata=args.get("metadata"),
                )
            case "kill_container":
                result = await c.kill_sandbox(args["container_id"])
            case "pause_container":
                result = await c.pause_sandbox(args["container_id"])
            case "resume_container":
                result = await c.resume_sandbox(args["container_id"])
            case "get_container_logs":
                result = await c.get_sandbox_logs(
                    args["container_id"],
                    args.get("limit", 100),
                )
            # Templates
            case "list_templates":
                result = await c.list_templates()
            case "create_template":
                result = await c.create_template_from_image(
                    image=args["image"],
                    expose_ports=args.get("expose_ports"),
                    writable_layer_size_gb=args.get("writable_layer_size_gb", 1),
                )
            case "get_template":
                result = await c.get_template(args["template_id"])
            # Persistent deploy
            case "list_volumes":
                result = get_deploy_manager().list_volumes()
            case "create_volume":
                result = get_deploy_manager().create_volume(
                    name=args["name"],
                    size_hint_gb=args.get("size_hint_gb", 1),
                )
            case "delete_volume":
                result = get_deploy_manager().delete_volume(args["name"])
            case "deploy_from_git":
                result = await get_deploy_manager().deploy_from_git(
                    git_url=args["git_url"],
                    branch=args.get("branch", "main"),
                    image=args.get("image", "python:3.12-slim"),
                    expose_ports=args.get("expose_ports"),
                    env_vars=args.get("env_vars"),
                    start_cmd=args.get("start_cmd"),
                    volume_name=args.get("volume_name"),
                    memory_mb=args.get("memory_mb", 256),
                )
            case "deploy_from_code":
                result = await get_deploy_manager().deploy_from_code(
                    app_name=args["app_name"],
                    files=args["files"],
                    image=args.get("image", "python:3.12-slim"),
                    expose_ports=args.get("expose_ports"),
                    env_vars=args.get("env_vars"),
                    start_cmd=args.get("start_cmd"),
                    memory_mb=args.get("memory_mb", 256),
                )
            case "update_code":
                result = await get_deploy_manager().update_code(
                    container_id=args["container_id"],
                    git_url=args["git_url"],
                    branch=args.get("branch", "main"),
                )
            case "exec_in_container":
                _validate_command(args["command"])
                result = await c.exec_in_sandbox(
                    args["container_id"],
                    args["command"],
                    args.get("timeout", 30),
                )
            case _:
                return _err(f"Unknown tool: {name}")
    except CubeAPIError as e:
        return _err(f"API error {e.status}: {e.detail}")
    except Exception as e:
        return _err(f"{e}\n{traceback.format_exc()}")

    return _ok(result)


@server.list_tools()
async def list_tools() -> list[Tool]:
    return TOOLS


@server.call_tool()
async def call_tool(name: str, arguments: dict | None) -> list[TextContent]:
    return await handle_tool(name, arguments or {})


# ---- Dual mode: stdio or HTTP ----


async def run_stdio():
    """Local mode: stdio transport, no auth (agent runs locally)."""
    async with stdio_server() as (read_stream, write_stream):
        await server.run(read_stream, write_stream, server.create_initialization_options())


async def run_http(port: int = 8080):
    """Remote mode: HTTP transport, auth handled by auth_gateway in front.

    Uses Starlette + StreamableHTTPServerTransport from the MCP SDK.
    """
    from starlette.applications import Starlette
    from starlette.routing import Mount
    from starlette.middleware import Middleware
    from mcp.server.streamable_http import StreamableHTTPServerTransport
    import contextlib

    def create_transport() -> StreamableHTTPServerTransport:
        return StreamableHTTPServerTransport(
            mcp_session_id=None,
            is_json_response_enabled=True,
        )

    async def handle_http(scope, receive, send):
        transport = create_transport()
        async with transport.connect() as (read_stream, write_stream):
            # Run MCP server on this transport
            server_task = asyncio.create_task(
                server.run(read_stream, write_stream, server.create_initialization_options())
            )
            await transport.handle_request(scope, receive, send)
            server_task.cancel()
            with contextlib.suppress(asyncio.CancelledError):
                await server_task

    app = Starlette(
        routes=[Mount("/mcp", app=handle_http)],
    )

    import uvicorn
    config = uvicorn.Config(app, host="0.0.0.0", port=port, log_level="info")
    uv_server = uvicorn.Server(config)
    await uv_server.serve()


def main():
    mode = os.environ.get("CUBE_MCP_MODE", "stdio")
    if mode == "http":
        port = int(os.environ.get("CUBE_MCP_PORT", "8080"))
        print(f"[cube-mcp] HTTP mode on :{port} (auth via auth_gateway)")
        asyncio.run(run_http(port))
    else:
        asyncio.run(run_stdio())


if __name__ == "__main__":
    main()
