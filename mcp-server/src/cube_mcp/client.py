"""HTTP client for the CubeAPI REST surface.

Wraps all endpoints from openapi.yml in a typed async client.
"""
from __future__ import annotations

import os
from dataclasses import dataclass, field
from typing import Any

import httpx


class CubeAPIError(Exception):
    """Raised when the CubeAPI returns an error."""

    def __init__(self, status: int, detail: str):
        self.status = status
        self.detail = detail
        super().__init__(f"CubeAPI {status}: {detail}")


@dataclass
class CubeContainerClient:
    """Async client for CubeAPI dashboard + E2B-compatible endpoints."""

    base_url: str = field(default_factory=lambda: os.environ.get("CUBE_API_URL", "http://localhost:3000"))
    api_key: str = field(default_factory=lambda: os.environ.get("CUBE_API_KEY", "e2b_000000"))
    _client: httpx.AsyncClient | None = field(default=None, repr=False)

    async def _get_client(self) -> httpx.AsyncClient:
        if self._client is None or self._client.is_closed:
            self._client = httpx.AsyncClient(
                base_url=self.base_url,
                headers={
                    "Authorization": f"Bearer {self.api_key}",
                    "Content-Type": "application/json",
                },
                timeout=30.0,
            )
        return self._client

    async def _request(self, method: str, path: str, **kwargs) -> Any:
        client = await self._get_client()
        resp = await client.request(method, path, **kwargs)
        if resp.status_code >= 400:
            raise CubeAPIError(resp.status_code, resp.text)
        if resp.status_code == 204 or not resp.content:
            return {}
        return resp.json()

    async def close(self):
        if self._client and not self._client.is_closed:
            await self._client.aclose()

    # ---- Cluster endpoints ----

    async def health(self) -> dict:
        """GET /cubeapi/v1/health"""
        return await self._request("GET", "/cubeapi/v1/health")

    async def cluster_overview(self) -> dict:
        """GET /cubeapi/v1/cluster/overview — capacity, node count, running sandboxes"""
        return await self._request("GET", "/cubeapi/v1/cluster/overview")

    async def cluster_versions(self) -> dict:
        """GET /cubeapi/v1/cluster/versions — component version matrix"""
        return await self._request("GET", "/cubeapi/v1/cluster/versions")

    async def list_nodes(self) -> list[dict]:
        """GET /cubeapi/v1/nodes — all nodes in the cluster"""
        return await self._request("GET", "/cubeapi/v1/nodes")

    async def get_node(self, node_id: str) -> dict:
        """GET /cubeapi/v1/nodes/{nodeID}"""
        return await self._request("GET", f"/cubeapi/v1/nodes/{node_id}")

    # ---- Sandbox (container) lifecycle ----

    async def list_sandboxes(
        self,
        metadata: str | None = None,
        state: str | None = None,
        limit: int = 50,
        next_token: str | None = None,
    ) -> list[dict]:
        """GET /cubeapi/v1/v2/sandboxes — list running containers with optional filters"""
        params: dict[str, Any] = {"limit": limit}
        if metadata:
            params["metadata"] = metadata
        if state:
            params["state"] = state
        if next_token:
            params["nextToken"] = next_token
        return await self._request("GET", "/cubeapi/v1/v2/sandboxes", params=params)

    async def get_sandbox(self, sandbox_id: str) -> dict:
        """GET /cubeapi/v1/sandboxes/{sandboxID}"""
        return await self._request("GET", f"/cubeapi/v1/sandboxes/{sandbox_id}")

    async def kill_sandbox(self, sandbox_id: str) -> dict:
        """DELETE /cubeapi/v1/sandboxes/{sandboxID}"""
        return await self._request("DELETE", f"/cubeapi/v1/sandboxes/{sandbox_id}")

    async def pause_sandbox(self, sandbox_id: str) -> dict:
        """POST /cubeapi/v1/sandboxes/{sandboxID}/pause — freeze cgroup (auto-pause)"""
        return await self._request("POST", f"/cubeapi/v1/sandboxes/{sandbox_id}/pause")

    async def resume_sandbox(self, sandbox_id: str) -> dict:
        """POST /cubeapi/v1/sandboxes/{sandboxID}/resume — thaw cgroup"""
        return await self._request("POST", f"/cubeapi/v1/sandboxes/{sandbox_id}/resume")

    async def get_sandbox_logs(self, sandbox_id: str, limit: int = 100) -> dict:
        """GET /cubeapi/v1/v2/sandboxes/{sandboxID}/logs"""
        return await self._request(
            "GET", f"/cubeapi/v1/v2/sandboxes/{sandbox_id}/logs", params={"limit": limit}
        )

    # ---- E2B-compatible sandbox creation ----

    async def create_sandbox(
        self,
        template_id: str,
        metadata: dict | None = None,
        memory_mb: int = 512,
        cpu_count: float = 1.0,
        env_vars: dict | None = None,
    ) -> dict:
        """POST /v1/sandboxes (E2B-compatible) — create a new container.

        In container mode, this spawns a container from the template image
        with the specified resource limits.
        """
        body: dict[str, Any] = {
            "templateID": template_id,
            "memoryMB": memory_mb,
            "cpuCount": cpu_count,
        }
        if metadata:
            body["metadata"] = metadata
        if env_vars:
            body["envVars"] = env_vars
        return await self._request("POST", "/v1/sandboxes", json=body)

    # ---- Templates ----

    async def list_templates(self) -> list[dict]:
        """GET /cubeapi/v1/templates — available container templates"""
        return await self._request("GET", "/cubeapi/v1/templates")

    async def get_template(self, template_id: str) -> dict:
        """GET /cubeapi/v1/templates/{templateID}"""
        return await self._request("GET", f"/cubeapi/v1/templates/{template_id}")

    async def create_template_from_image(
        self,
        image: str,
        expose_ports: list[int] | None = None,
        writable_layer_size_gb: int = 1,
        mounts: list[dict] | None = None,
        env_vars: dict | None = None,
        start_cmd: str | None = None,
    ) -> dict:
        """POST /cubeapi/v1/templates — create template from OCI image.

        Args:
            image: OCI image reference, e.g. "python:3.12-slim", "nginx:alpine"
            expose_ports: Ports to expose (e.g. [8000, 3000])
            writable_layer_size_gb: Writable overlay size in GB (default 1)
            mounts: Persistent volume mounts, e.g.
                [{"source": "/volumes/myapp", "destination": "/app", "readonly": false}]
            env_vars: Default environment variables for the template
            start_cmd: Override the container start command
        """
        body: dict[str, Any] = {
            "image": image,
            "writableLayerSizeGB": writable_layer_size_gb,
        }
        if expose_ports:
            body["exposePorts"] = expose_ports
        if mounts:
            body["mounts"] = mounts
        if env_vars:
            body["envVars"] = env_vars
        if start_cmd:
            body["startCmd"] = start_cmd
        return await self._request("POST", "/cubeapi/v1/templates", json=body)

    async def exec_in_sandbox(self, sandbox_id: str, command: str, timeout: int = 30) -> dict:
        """POST /cubeapi/v1/v2/sandboxes/{sandboxID}/exec — run command inside container.

        Returns stdout, stderr, and exit code.
        """
        body = {"command": command, "timeout": timeout}
        return await self._request(
            "POST", f"/cubeapi/v1/v2/sandboxes/{sandbox_id}/exec", json=body
        )
