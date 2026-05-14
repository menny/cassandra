# Review Guidelines

Reviewer lens for the whole repo. Assumes [AGENTS.md](AGENTS.md) and [DESIGN.md](DESIGN.md) have been read; does not restate them. Per-directory `REVIEWERS.md` files (e.g. [llm/REVIEWERS.md](llm/REVIEWERS.md)) scope further.

## Paired edits (block if one is missing)

- New `os.Stdout` write ↔ justification that it is final review output.
- `BuildSystemPrompt` addition ↔ placement between Zone 2 and Zone 3 (AGENTS.md §6).
- GitHub Action input added ↔ `env:` mapping per AGENTS.md §Security 1.

## Safety & Liveness (block if missing)

- **Infinite Loops**: Any loop reading until a delimiter (e.g. `\n`) or EOF MUST have a mechanism to handle missing delimiters in infinite streams.
- **Context Holes**: Tools performing non-trivial I/O MUST be audited for `ctx.Err()` propagation.
- **Buffer Wrap-around**: Circular buffers MUST be verified for index desync during eviction, especially when memory limits and count limits are enforced simultaneously.
- **Truncation Clarity**: Verify that truncation notices are appended in a way that remains visible even if the output is further truncated by downstream systems.

