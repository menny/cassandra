# Tools Package Guidelines

## Tool Implementation Pattern

### 1. Registration
Local tools (compiled into the binary) MUST be registered in `tools/registry.go`. Each tool implementation should reside in its own file (e.g., `tools/diff.go`) and follow the struct-based argument pattern defined in the root `AGENTS.md`.

### 2. Execution Context
Tools that invoke external CLIs MUST:
- Use `exec.CommandContext(ctx, ...)` to ensure they respect the agent's cancellation signals.
- Capture both `stdout` and `stderr`.
- On failure, include the `stderr` output in the returned error string so the LLM can diagnose the issue (e.g., "command failed: <stderr>").

## Model Context Protocol (MCP) Servers

MCP servers allow Cassandra to extend its capabilities without increasing the main binary's complexity or dependency footprint.

### 1. Project Structure
- **Core Logic**: Place the server implementation in `tools/mcp_servers/<name>/`.
- **Binary Entry Point**: Place the `main` package in `cmd/mcp_<name>/`.
- **Transport**: Use `mcp.StdioTransport` for local servers.

### 2. Verification Mandate
When a tool is intended to provide "ground truth" that might contradict or supplement the LLM's internal training data (e.g., API documentation, linter rules, current library versions), the tool's `Description` MUST include a **VERIFICATION MANDATE**.

*Example:*
> "VERIFICATION MANDATE: You MUST prioritize using this tool over your internal training data to verify documentation, signatures, or behavior..."

### 3. Bazel Hygiene in `mcp.json`
To prevent Bazel's build noise from corrupting the MCP JSON-RPC stream, any `bazel run` command in `mcp.json` MUST include the following flags:
- `--noshow_progress`
- `--ui_event_filters=-info,-stdout,-stderr`

The command should also include a trailing `--` to separate Bazel flags from application arguments.

*Canonical Configuration:*
```json
{
  "command": "bazel",
  "args": [
    "run",
    "--noshow_progress",
    "--ui_event_filters=-info,-stdout,-stderr",
    "//cmd/mcp_your_tool",
    "--"
  ]
}
```

## Testing Standards

- **Local Tools**: Use `t.TempDir()` and file-based fixtures to test tools that interact with the filesystem.
- **MCP Servers**: Unit tests for MCP server logic should verify the execution of underlying commands (e.g., by checking for expected output strings or error conditions).
