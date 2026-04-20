# Review Guidelines

Reviewer lens for the whole repo. Assumes [AGENTS.md](AGENTS.md) and [DESIGN.md](DESIGN.md) have been read; does not restate them. Per-directory `REVIEWERS.md` files (e.g. [llm/REVIEWERS.md](llm/REVIEWERS.md)) scope further.

## Severity mapping

Map findings to Do / Try / Consider:

- **Do (blocking)**: output-contract violations (stdout/stderr split), broken contracts named in `DESIGN.md`, security findings (AGENTS.md §Security), Zone 1/2 prompt edits that inject dynamic data.
- **Try (recommended)**: AGENTS.md deviations that are rules but not contracts.
- **Consider (optional)**: code shape, naming, comment clarity.

Not every AGENTS.md deviation is blocking. Over-blocking drowns real defects.

## Paired edits (block if one is missing)

- New `os.Stdout` write ↔ justification that it is final review output.
- `BuildSystemPrompt` addition ↔ placement between Zone 2 and Zone 3 (AGENTS.md §6).
- GitHub Action input added ↔ `env:` mapping per AGENTS.md §Security 1.
