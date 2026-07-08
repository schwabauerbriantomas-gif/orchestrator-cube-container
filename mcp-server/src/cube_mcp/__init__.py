"""Cube Container MCP Server.

Exposes Cube Container cluster operations as MCP tools, so any AI agent
(Claude, GPT, local LLM) can manage containers across the cluster via
natural language or programmatic tool calls.

Usage:
    export CUBE_API_URL="http://localhost:3000"
    export CUBE_API_KEY="e2b_000000"
    cube-mcp  # starts stdio MCP server

Or import directly:
    from cube_mcp.server import CubeContainerClient
    client = CubeContainerClient("http://localhost:3000")
"""

__version__ = "0.1.0"
