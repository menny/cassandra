# Tools Package Review Guidelines

Reviewer lens. Complements [AGENTS.md](AGENTS.md) and [../AGENTS.md](../AGENTS.md); does not restate them.

## Do not flag

- Truncation at the 40 KB output limit is intentional — tools return a truncation notice rather than failing.
- The 40 KB LLM-output cap and the 1 MB buffer cap are separate limits (wire vs. in-process); both are required.
- `util.ValidatePathInRoot` re-resolving symlinks on every call is intentional (TOCTOU prevention, not redundancy).
- `exec.CommandContext` stderr included in the returned error string is intentional — it gives the LLM diagnostic context.

## Safety (block if missing)

- Tools accepting file or directory paths MUST call `util.ValidatePathInRoot`; lexical path cleaning (`filepath.Clean`) alone is not sufficient.
- Long-running loops (`WalkDir`, streaming reads) MUST check `ctx.Err()` in each iteration.
- Tools invoking external CLIs MUST use `exec.CommandContext(ctx, ...)`.
- Streaming tools MUST use `io.LimitReader` — do not rely on `os.Stat().Size()` for OOM prevention.

## Paired edits (block if one is missing)

- New local tool ↔ registered in `tools/registry.go` via `RegisterLocalTools`.
- New local tool ↔ security negative tests: directory traversal (`../`), valid symlink, broken symlink, trampoline symlink.
- New MCP server ↔ `VERIFICATION MANDATE` in tool `Description` + noise-suppression flags (`--noshow_progress`, `--ui_event_filters`) in `mcp.json`.
- New MCP server ↔ binary entry point under `cmd/mcp_<name>/`.
