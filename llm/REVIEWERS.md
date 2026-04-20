# LLM Package Review Guidelines

Reviewer lens for code under `llm/`. Assumes [`AGENTS.md`](AGENTS.md) and
[`DESIGN.md`](../DESIGN.md) have been read — this file does not restate
their rules.

## Cross-Provider Feature Parity

The `llm.Model` interface is provider-agnostic by design; behavioral
divergence between concrete implementations is a defect, not a stylistic
choice. For every change to one provider, the review MUST verify the
other.

- If a feature, field, counter, or code path is added to
  `llm/google/provider.go`, the reviewer MUST verify that
  `llm/anthropic/provider.go` either implements it or explicitly ignores
  it with a documented rationale (and vice versa).
- "Explicitly ignores" means a code comment at the relevant site naming
  the divergence and why — not silent absence. Silent divergence between
  providers is the single most common way callers leak provider-specific
  assumptions into provider-agnostic code.
- When reviewing a PR that touches only one provider, the expected
  reviewer question is **"why does the other provider not need this
  change?"** — and the PR description (or a code comment) must answer
  it. Absence of the question is a review defect.
- The `llm.Message.CacheBreakpoint` field is the canonical precedent for
  documented graceful degradation: one provider acts on it, the other
  ignores it, and the field's doc comment names both behaviors. New
  provider-specific fields or metadata keys must follow the same pattern.

## Noise Filter — Do Not Flag

Patterns that look like issues but are settled decisions. Raising them
costs tokens and trains the model toward re-suggesting them.

- **`RetryingModel` retries on every error**, including 4xx and context
  deadlines other than cancellation. Provider SDKs sometimes map
  transient upstream conditions to 4xx, and filtering by error class has
  been rejected. Do not suggest error-class filtering or retry-budget
  refinements unless the PR itself is about retry policy.
- **`llm.UnknownUsage()` returns a value, not a pointer, and uses
  `-1/-1` sentinels.** Callers depend on value semantics and the
  sentinel convention. Do not suggest `*Usage`, `Optional[Usage]`, or an
  `Ok bool` companion field.
- **`clampInt32` in `llm/google/provider.go` carries
  `//nolint:gosec`.** It is the centralization point for the pragma
  (root AGENTS.md §7). Do not suggest moving, duplicating, or removing
  the pragma.
- **Provider packages import `llm/internal/util`.** That is the
  intentional shared conversion layer; do not suggest inlining
  `ParseRequired` into each provider.
- **`llm.retry[T]` is package-private and untested directly.** It is
  exercised through both `RetryingModel` methods (per root AGENTS.md §3
  "Parallel Method Coverage"). Do not request direct unit tests for the
  generic helper.

## Cross-File Invariants

Paired edits that must land together. If the reviewer sees one without
the other, that is a blocking finding.

- A change to `llm/anthropic.submitReviewToolName` (value or symbol)
  MUST be accompanied by a corresponding update in
  [`DESIGN.md §Technical Decisions 4`](../DESIGN.md#technical-decisions).
  The name is a documented contract.
- A change to `llm.DefaultStructuredMaxTokens` MUST be accompanied by
  the matching change to the CLI `--max-tokens` default in
  `cmd/ai_reviewer`. The two are paired by `DESIGN.md §4 Shared
  Contracts`.
- A new provider added under `llm/<name>/` MUST register in
  `llm/factory/factory.go`'s `providers` map *and* be listed in
  `README.md` §Supported Models in the same PR.
- A change to any `llm.Model` interface method signature MUST update
  both provider implementations, `RetryingModel`, and any test doubles
  under `llm/` — the compiler enforces the first three; test doubles
  are the common miss.
