# llm/ Review Guidelines

Reviewer lens. Complements [AGENTS.md](AGENTS.md) and [DESIGN.md](../DESIGN.md); does not restate them.

## Feature parity across providers

Behavioral divergence between `llm/anthropic` and `llm/google` is a defect unless documented.

- A change to one provider requires the same change in the other, or a code comment naming the divergence and its rationale. `llm.Message.CacheBreakpoint` is the canonical graceful-degradation precedent.
- For a single-provider PR, the description must answer "why does the other provider not need this?". Absence is a review defect.

## Do not flag

- `RetryingModel` retries on all errors, including 4xx. Error-class filtering was rejected.
- `llm.UnknownUsage()` is value-typed with `-1/-1` sentinels. Callers depend on this; do not suggest pointers or an `Ok` field.
- `clampInt32` carries the package `//nolint:gosec`; it is the centralization point (root AGENTS.md §7).
- `llm/internal/util` is the intentional shared conversion layer.
- `llm.retry[T]` is tested via both `RetryingModel` methods; do not request direct unit tests.

## Paired edits (block if one is missing)

- `submitReviewToolName` ↔ `DESIGN.md §Technical Decisions 4`.
- `DefaultMaxTokens` ↔ CLI `--max-tokens` default in `cmd/ai_reviewer`.
- New provider ↔ `factory.providers` map + `README.md` §Supported Models.
- `llm.Model` signature change ↔ test doubles under `llm/`.
