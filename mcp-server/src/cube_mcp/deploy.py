"""Persistent deploy operations: git-based deployment and volume management.

These tools allow an AI agent to deploy services that survive container
restarts by writing code to git and mounting persistent volumes.
"""
from __future__ import annotations

import os
import uuid
from dataclasses import dataclass, field
from pathlib import Path
from typing import Any

from .client import CubeAPIError, CubeContainerClient
from .security import (
    sanitize_git_url_for_name,
    validate_command,
    validate_git_url,
    validate_path_safe,
    validate_safe_name,
)


@dataclass
class DeployManager:
    """Manages git-based persistent deploys and volumes.

    Directory layout on the node:
        /volumes/
            myapp/           ← persistent volume (survives restarts)
        /deploy-workspaces/   ← temporary git clone dirs
    """

    client: CubeContainerClient
    volumes_root: str = field(default_factory=lambda: os.environ.get("CUBE_VOLUMES_ROOT", "/volumes"))
    workspaces_root: str = field(
        default_factory=lambda: os.environ.get("CUBE_WORKSPACES_ROOT", "/deploy-workspaces")
    )

    def __post_init__(self):
        Path(self.volumes_root).mkdir(parents=True, exist_ok=True)
        Path(self.workspaces_root).mkdir(parents=True, exist_ok=True)

    # ---- Volume management ----

    def list_volumes(self) -> list[dict]:
        """List all persistent volumes with size and usage."""
        volumes = []
        for entry in sorted(Path(self.volumes_root).iterdir()):
            if entry.is_dir():
                # Calculate size
                total_size = sum(f.stat().st_size for f in entry.rglob("*") if f.is_file())
                file_count = sum(1 for f in entry.rglob("*") if f.is_file())
                volumes.append({
                    "name": entry.name,
                    "path": str(entry),
                    "size_bytes": total_size,
                    "size_mb": round(total_size / (1024 * 1024), 2),
                    "file_count": file_count,
                })
        return volumes

    def create_volume(self, name: str, size_hint_gb: int = 1) -> dict:
        """Create a new persistent volume directory."""
        validate_safe_name(name)
        path = Path(self.volumes_root) / name
        validate_path_safe(path, Path(self.volumes_root))
        if path.exists():
            return {"name": name, "path": str(path), "status": "already_exists"}
        path.mkdir(parents=True)
        return {
            "name": name,
            "path": str(path),
            "size_hint_gb": size_hint_gb,
            "status": "created",
        }

    def delete_volume(self, name: str) -> dict:
        """Delete a persistent volume. WARNING: destroys all data."""
        import shutil
        validate_safe_name(name)
        path = Path(self.volumes_root) / name
        validate_path_safe(path, Path(self.volumes_root))
        if not path.exists():
            return {"name": name, "status": "not_found"}
        shutil.rmtree(path)
        return {"name": name, "status": "deleted"}

    # ---- Git-based deploy ----

    async def deploy_from_git(
        self,
        git_url: str,
        branch: str = "main",
        image: str = "python:3.12-slim",
        expose_ports: list[int] | None = None,
        env_vars: dict | None = None,
        start_cmd: str | None = None,
        volume_name: str | None = None,
        memory_mb: int = 256,
        cpu_count: float = 1.0,
    ) -> dict:
        """Deploy a service from a git repository with persistent storage.

        Flow:
        1. Clone/pull git repo to workspace
        2. Create volume (if not provided)
        3. Copy code to volume
        4. Create template with volume mount
        5. Create container from template

        The container mounts the volume at /app, so code survives restarts.
        On restart, the container re-pulls from git to get latest code.
        """
        import asyncio

        # Validate git URL
        git_url = validate_git_url(git_url)

        # Derive safe app name from URL
        app_name = sanitize_git_url_for_name(git_url)
        if not volume_name:
            volume_name = app_name
        else:
            volume_name = validate_safe_name(volume_name)

        # Step 1: Clone repo locally to inspect it
        workspace = Path(self.workspaces_root) / app_name
        validate_path_safe(workspace, Path(self.workspaces_root))
        clone_result = await self._git_clone_or_pull(git_url, branch, workspace)

        # Step 2: Create volume
        volume = self.create_volume(volume_name)

        # Step 3: Copy code to volume
        self._sync_code(workspace, Path(self.volumes_root) / volume_name)

        # Step 4: Detect start command if not provided
        if not start_cmd:
            start_cmd = self._detect_start_cmd(workspace)

        # Step 5: Create template with volume mount
        template = await self.client.create_template_from_image(
            image=image,
            expose_ports=expose_ports or [8000],
            mounts=[{
                "source": f"{self.volumes_root}/{volume_name}",
                "destination": "/app",
                "readonly": False,
            }],
            env_vars={
                "DEPLOY_SOURCE": "git",
                "GIT_URL": git_url,
                "GIT_BRANCH": branch,
                **(env_vars or {}),
            },
            start_cmd=start_cmd,
        )

        template_id = template.get("templateID") or template.get("id", "")

        # Step 6: Create container
        container = await self.client.create_sandbox(
            template_id=template_id,
            memory_mb=memory_mb,
            cpu_count=cpu_count,
            metadata={
                "app": app_name,
                "source": "git",
                "git_url": git_url,
                "branch": branch,
                "volume": volume_name,
            },
        )

        return {
            "app_name": app_name,
            "volume": volume,
            "clone": clone_result,
            "template_id": template_id,
            "container": container,
            "start_cmd": start_cmd,
        }

    async def update_code(self, container_id: str, git_url: str, branch: str = "main") -> dict:
        """Pull latest code from git and sync to the container's volume.

        After updating code on disk, the container needs a restart to
        pick up changes (or can use exec_in_sandbox to restart just the app).
        """
        import asyncio

        # Validate
        git_url = validate_git_url(git_url)
        app_name = sanitize_git_url_for_name(git_url)
        workspace = Path(self.workspaces_root) / app_name
        volume_path = Path(self.volumes_root) / app_name

        # Pull latest
        clone_result = await self._git_clone_or_pull(git_url, branch, workspace)

        # Sync to volume
        self._sync_code(workspace, volume_path)

        # Restart the app inside the container via exec
        restart_result = None
        try:
            restart_result = await self.client.exec_in_sandbox(
                container_id,
                "cd /app && kill -HUP 1 2>/dev/null || true"
            )
        except CubeAPIError:
            pass  # exec may not be available on all containers

        return {
            "container_id": container_id,
            "git_pull": clone_result,
            "synced_to": str(volume_path),
            "restart_signal": restart_result,
        }

    async def deploy_from_code(
        self,
        app_name: str,
        files: dict[str, str],
        image: str = "python:3.12-slim",
        expose_ports: list[int] | None = None,
        env_vars: dict | None = None,
        start_cmd: str | None = None,
        memory_mb: int = 256,
    ) -> dict:
        """Deploy a service from inline code files (no git needed).

        Args:
            app_name: Name for the app (becomes volume name)
            files: Dict of {filename: content}, e.g. {"main.py": "...", "requirements.txt": "..."}
            image: Base OCI image
            expose_ports: Ports to expose
            env_vars: Environment variables
            start_cmd: How to start the app (auto-detected if omitted)
            memory_mb: Memory limit

        This writes files to a persistent volume and creates a container
        that mounts it. The code persists across container restarts.
        """
        # Validate app name
        app_name = validate_safe_name(app_name)

        # Create volume
        volume = self.create_volume(app_name)
        volume_path = Path(self.volumes_root) / app_name

        # Write files
        for filename, content in files.items():
            # Validate filename: no path traversal
            safe_name = validate_safe_name(filename) if "/" not in filename else None
            if safe_name is None:
                # Allow subdirectories but validate each component
                parts = filename.split("/")
                if not all(validate_safe_name(p) for p in parts if p):
                    raise ValueError(f"Invalid filename: {filename}")
            filepath = volume_path / filename
            validate_path_safe(filepath, volume_path)
            filepath.parent.mkdir(parents=True, exist_ok=True)
            filepath.write_text(content)

        # Detect start command
        if not start_cmd:
            start_cmd = self._detect_start_cmd(volume_path)

        # Create template
        template = await self.client.create_template_from_image(
            image=image,
            expose_ports=expose_ports or [8000],
            mounts=[{
                "source": str(volume_path),
                "destination": "/app",
                "readonly": False,
            }],
            env_vars=env_vars,
            start_cmd=start_cmd,
        )

        template_id = template.get("templateID") or template.get("id", "")

        # Create container
        container = await self.client.create_sandbox(
            template_id=template_id,
            memory_mb=memory_mb,
            metadata={
                "app": app_name,
                "source": "inline_code",
                "volume": app_name,
                "files": list(files.keys()),
            },
        )

        return {
            "app_name": app_name,
            "volume": volume,
            "files_written": list(files.keys()),
            "template_id": template_id,
            "container": container,
            "start_cmd": start_cmd,
        }

    # ---- Internal helpers ----

    async def _git_clone_or_pull(self, git_url: str, branch: str, workspace: Path) -> dict:
        import asyncio

        git_env = {
            **os.environ,
            "GIT_TERMINAL_PROMPT": "0",  # No interactive prompts
        }

        if workspace.exists() and (workspace / ".git").exists():
            # Pull
            proc = await asyncio.create_subprocess_exec(
                "git", "fetch", "origin", branch,
                cwd=str(workspace),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                env=git_env,
            )
            _, fetch_err = await proc.communicate()

            proc = await asyncio.create_subprocess_exec(
                "git", "reset", "--hard", f"origin/{branch}",
                cwd=str(workspace),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                env=git_env,
            )
            stdout, stderr = await proc.communicate()
            return {
                "action": "pulled",
                "branch": branch,
                "output": stdout.decode()[:500],
            }
        else:
            # Clone
            workspace.mkdir(parents=True, exist_ok=True)
            proc = await asyncio.create_subprocess_exec(
                "git", "clone", "--depth", "1", "-b", branch, git_url, str(workspace),
                stdout=asyncio.subprocess.PIPE,
                stderr=asyncio.subprocess.PIPE,
                env=git_env,
            )
            stdout, stderr = await proc.communicate()
            if proc.returncode != 0:
                return {
                    "action": "clone_failed",
                    "error": stderr.decode()[:500],
                }
            return {
                "action": "cloned",
                "branch": branch,
                "output": stdout.decode()[:500],
            }

    def _sync_code(self, source: Path, dest: Path) -> dict:
        """Copy code from git workspace to persistent volume, excluding .git."""
        import shutil

        dest.mkdir(parents=True, exist_ok=True)

        copied = 0
        for item in source.iterdir():
            if item.name == ".git":
                continue
            target = dest / item.name
            if target.exists():
                if target.is_dir():
                    shutil.rmtree(target)
                else:
                    target.unlink()
            if item.is_dir():
                shutil.copytree(item, target)
            else:
                shutil.copy2(item, target)
            copied += 1

        return {"files_synced": copied, "dest": str(dest)}

    def _detect_start_cmd(self, path: Path) -> str:
        """Auto-detect how to start the app based on files present."""
        # Python: uvicorn (FastAPI/Starlette)
        if (path / "requirements.txt").exists():
            reqs = (path / "requirements.txt").read_text().lower()
            if "fastapi" in reqs or "starlette" in reqs:
                return "pip install -r requirements.txt && cd /app && uvicorn main:app --host 0.0.0.0 --port 8000"
            if "flask" in reqs:
                return "pip install -r requirements.txt && cd /app && python main.py"

        # Python: pyproject.toml
        if (path / "pyproject.toml").exists():
            return "cd /app && pip install -e . && uvicorn main:app --host 0.0.0.0 --port 8000"

        # Node.js
        if (path / "package.json").exists():
            return "cd /app && npm install && npm start"

        # Go binary (pre-compiled)
        if (path / "app").exists() or (path / "server").exists():
            binary = "app" if (path / "app").exists() else "server"
            return f"cd /app && ./{binary}"

        # Static files: serve with python
        if (path / "index.html").exists():
            return "cd /app && python3 -m http.server 8000"

        # Fallback
        return "cd /app && python main.py 2>/dev/null || python3 -m http.server 8000"
