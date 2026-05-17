# Tools Package Guidelines

## Tool Implementation Pattern

### 1. Registration
Local tools (compiled into the binary) MUST be registered in `tools/registry.go`. Each tool implementation should reside in its own file (e.g., `tools/diff.go`) and follow the struct-based argument pattern defined in the root `AGENTS.md`.

### 2. Execution Context
Tools that invoke external CLIs MUST:
- Use `exec.CommandContext(ctx, ...)` to ensure they respect the agent's cancellation signals.
- Capture both `stdout` and `stderr`.
- On failure, include the `stderr` output in the returned error string so the LLM can diagnose the issue (e.g., "command failed: <stderr>").

### 3. Resource Management
- **Bounded Buffers**: If a tool uses a buffer to collect data (e.g., a circular buffer for `tail_lines`), it MUST have both an entry count limit AND a strict byte-based memory cap (e.g., 1MB).
- **Graceful Truncation**: When a tool hits an output limit, it should return as much valid data as possible followed by a clear truncation notice (e.g., `... (truncated)`), rather than failing with an error.

### 4. Path Validation & Security
Tools that accept file or directory paths as arguments MUST:
- **Use `util.ValidatePathInRoot(root, path)`**: This helper ensures the path is physically within the root by resolving all intermediate symlinks.
- **Check for Broken Symlinks**: Broken symlinks committed to a repo can be used to bypass lexical checks. `ValidatePathInRoot` handles this by rejecting paths that cannot be safely resolved.
- **Consistent Relative Output**: Tools (like `grep` or `glob`) SHOULD return paths relative to the workspace root. If a tool resolves a path to an absolute location during validation, it MUST convert it back to a relative path before passing it to external CLIs or returning it to the LLM.
- **Context Awareness**: Long-running filesystem operations (e.g., `WalkDir`) MUST propagate the tool's context and check `ctx.Err()` to prevent "context holes" during large repository scans.

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
- **Security Negative Tests**: Any tool interacting with the filesystem MUST include test cases for:
  - Directory traversal attempts (`../etc/passwd`).
  - Symlinks pointing outside the workspace (valid, broken, and "trampoline" symlinks).
  - TOCTOU bypasses (relative targets with hidden physical traversals).
- **MCP Servers**: Unit tests for MCP server logic should verify the execution of underlying commands (e.g., by checking for expected output strings or error conditions).
