# Codify Repeating Patterns

Watch for two signals that a rule is missing from the relevant AGENTS.md:

1. **Recurrence** — the same root-cause mistake appears in two or more locations in this diff.
2. **Surprise** — a problem that an agent following the current AGENTS.md files would not have been warned about.

When either signal fires, use `read_file` to read the AGENTS.md in the affected directory. If that file has no match, walk up to the repo root. If the rule is absent, append a **"Codification Opportunities"** section to your review. Each entry must specify:

- **Target**: the AGENTS.md path closest to where the pattern lives.
- **Rule**: one imperative sentence stating what MUST or MUST NOT be done.
- **Rationale**: one sentence naming the failure mode it prevents.

If a short `// bad` / `// good` example would materially clarify the rule, include it — following the style of CODE_STYLE.md.

Omit the section entirely if every issue you raised is already covered by existing AGENTS.md content.
