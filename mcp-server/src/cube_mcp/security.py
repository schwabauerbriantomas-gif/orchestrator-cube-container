"""Security utilities for input validation and sanitization."""
from __future__ import annotations

import re
from pathlib import Path

# Allowed protocols for git clone
ALLOWED_GIT_PROTOCOLS = ("https://", "http://", "git://")

# Safe name pattern: alphanumeric, dash, underscore, dot (no path separators)
SAFE_NAME_RE = re.compile(r"^[a-zA-Z0-9][a-zA-Z0-9._-]{0,63}$")

# Dangerous patterns in shell commands
SHELL_BLACKLIST = [
    "rm -rf /",
    "mkfs",
    "dd if=",
    ":(){ :|:& };:",
    "chmod 777 /",
    "curl.*|.*sh",  # pipe to shell
    "wget.*|.*sh",
]


def validate_safe_name(name: str) -> str:
    """Validate that a name is safe to use as directory/volume name.

    Rejects:
    - Path traversal (../)
    - Path separators (/ \\)
    - Null bytes
    - Names starting with dot (hidden files)
    - Names longer than 64 chars
    """
    if not name:
        raise ValueError("Name cannot be empty")
    if not SAFE_NAME_RE.match(name):
        raise ValueError(
            f"Invalid name '{name}': must be alphanumeric with dots, dashes, or underscores, "
            "1-64 chars, cannot start with dot"
        )
    return name


def validate_path_safe(path: Path, root: Path) -> Path:
    """Ensure a resolved path is contained within root.

    Prevents path traversal attacks by checking that the final resolved
    path doesn't escape the root directory.
    """
    resolved = path.resolve()
    root_resolved = root.resolve()
    try:
        resolved.relative_to(root_resolved)
    except ValueError:
        raise ValueError(f"Path '{path}' escapes allowed root '{root}'")
    return resolved


def validate_git_url(url: str) -> str:
    """Validate that a git URL uses an allowed protocol.

    Only https://, http://, and git:// are allowed.
    Blocks file://, ssh:// with credentials, and protocol-level exploits.
    """
    url = url.strip()
    if not any(url.startswith(p) for p in ALLOWED_GIT_PROTOCOLS):
        raise ValueError(
            f"Invalid git URL '{url}': only {', '.join(ALLOWED_GIT_PROTOCOLS)} are allowed"
        )

    # Reject URLs with embedded credentials (user:pass@host)
    # https://user:pass@host/repo → block
    if "@" in url.split("://")[1].split("/")[0]:
        raise ValueError("Git URL with embedded credentials is not allowed")

    return url


def validate_command(cmd: str) -> str:
    """Basic command validation for exec_in_container.

    This is intentionally lightweight — containers are isolated, but we
    still block obvious catastrophic patterns as a defense-in-depth measure.
    """
    cmd = cmd.strip()
    if not cmd:
        raise ValueError("Command cannot be empty")

    for pattern in SHELL_BLACKLIST:
        if re.search(pattern, cmd, re.IGNORECASE):
            raise ValueError("Command contains blacklisted pattern")

    return cmd


def sanitize_git_url_for_name(url: str) -> str:
    """Extract a safe directory name from a git URL."""
    # Get the repo name from URL
    name = url.rstrip("/").split("/")[-1].replace(".git", "")
    # Sanitize: replace any non-safe char with dash
    name = re.sub(r"[^a-zA-Z0-9._-]", "-", name)
    # Ensure it doesn't start with dot or dash
    if name and name[0] in ".-":
        name = "app-" + name
    if not name:
        name = "unnamed-app"
    return name
