# LLM Package Guidelines

Scoped guidance for code under `llm/`. These rules complement the repo-level
[AGENTS.md](../AGENTS.md); follow both. See also the architectural contract
in [DESIGN.md §4 LLM Abstraction](../DESIGN.md#4-llm-abstraction-llm).

## Provider Implementation Pattern

When adding or modifying an `llm.Model` implementation, follow these
conventions so the provider stays consistent with the shared abstraction.

### 1. Registration
Register new providers by adding one entry to the `providers` map in
`llm/factory/factory.go`. Do **not** extend `factory.New` with a new
`switch` branch. The "unsupported provider" error message is derived from
the map, so a single map entry wires both construction and error reporting.

### 2. Structured Output Defaults
Every `GenerateStructuredContent` implementation MUST open with:
```go
modelName, maxTokens := config.Resolve(p.modelName)
```
Do not inline model-override or max-tokens defaulting.
`llm.DefaultMaxTokens` is the single source of truth and is kept
in lockstep with the CLI `--max-tokens` default.

### 3. Usage Sentinel
Seed `llm.Response.Usage` with `llm.UnknownUsage()` and overwrite on
success. Do not hand-roll `Usage{PromptTokens: -1, ...}` literals.

When a provider reports token usage across multiple counter fields (e.g.
Anthropic's `InputTokens`, `CacheReadInputTokens`,
`CacheCreationInputTokens`), the guard that decides "do we have real usage
data?" MUST cover **every** counter path. A guard that only checks the
non-cache counters will silently leave `CachedTokens` at its sentinel for
cache-only responses — the exact bug fixed in `da5f0f3`.

Callers aggregating per-iteration `Usage` into a session total MUST use
`(*Usage).Add(other)`, which ignores sentinel fields (values ≤ 0) so
`UnknownUsage()` responses do not corrupt the aggregate. Do not hand-roll
field-by-field accumulation.

### 4. Tool-Name Contracts
Tool names referenced by downstream consumers or documented in
`DESIGN.md` (e.g. `submitReviewToolName`) MUST be package-level `const`s
with a doc comment pointing at the DESIGN.md contract. Never inline the
literal at use sites — both the tool definition and the tool-choice must
reference the same symbol so they cannot silently desync.

### 5. ProviderMetadata Access
Provider-specific keys in `llm.Message.ProviderMetadata` are typed-unsafe
map indexes. For every key:
- Define a package-level `const` for the key string.
- Define a typed accessor (e.g. `func thoughtSignature(m llm.Message) []byte`)
  that performs the type assertion once.

Both the write site and every read site MUST reference the const and go
through the accessor. Hardcoding the key string in multiple places (as was
done for `google_thought_signature` before `899d74d`) makes typos
undetectable until runtime.

### 6. JSON Schema → Native Schema Conversion
When translating a JSON Schema type string to a provider-native enum, use
a `map[string]T` lookup, not a `switch`. Unknown types MUST emit a stderr
diagnostic naming the offending type (per the repo-level Output Contract).
Silent fallthroughs are banned — they surface as opaque provider errors
well after the malformed schema has left our process. See
`jsonSchemaTypes` in `llm/google/provider.go` as the canonical example.

### 7. Cross-Cutting Model Wrappers
`llm.retry[T]` (in `llm/retry.go`) encapsulates the attempt loop,
exponential back-off, and ctx-cancellation policy used by `RetryingModel`.
Any new cross-cutting `llm.Model` wrapper (caching, telemetry,
rate-limiting) SHOULD compose via the `llm.Model` interface and reuse
this helper where applicable rather than re-implementing the loop.
Parallel methods on the interface (`GenerateContent` and
`GenerateStructuredContent`) share one helper; do not duplicate the
control flow per method.
