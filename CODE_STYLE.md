# Cassandra Code Style

Coding patterns settled across the refactor work. Complements
[AGENTS.md](AGENTS.md) (rules you MUST follow) and [DESIGN.md](DESIGN.md)
(architecture). This file is *how to write code that fits the codebase*.

## Error handling

Use `errors.New` for literal errors. Use `%w` to wrap. Use `errors.As` to
inspect typed errors from stdlib.

```go
// bad
return fmt.Errorf("no match found")
return fmt.Errorf("read: %v", err)
if cmd.ProcessState != nil && cmd.ProcessState.ExitCode() == 1 { ... }

// good
return errors.New("no match found")
return fmt.Errorf("read: %w", err)
var exitErr *exec.ExitError
if errors.As(err, &exitErr) && exitErr.ExitCode() == 1 { ... }
```

**Why:** `%w` preserves the error chain so callers can `errors.Is/As`.
`errors.As` works off the returned value and doesn't rely on `ProcessState`
being populated (it's nil on spawn failures).

## Extract on the third copy

Two identical blocks can live. Three is a pattern that will drift. Extract.

```go
// Three sites built `:(exclude)*<lockfile>` args → one helper
func appendLockFileExcludes(args []string) []string { ... }
```

**Why:** Three copies signal a convention; the fourth reader will add an
inconsistent fifth.

## Sentinels belong to their type

When a value uses a sentinel (e.g. `-1` for "unknown"), ship a
constructor for the sentinel and put aggregation/validation on the type.
Don't make every caller re-encode the convention.

```go
func UnknownUsage() Usage { return Usage{PromptTokens: -1, OutputTokens: -1} }

func (u *Usage) Add(other Usage) {
    if other.PromptTokens > 0 { u.PromptTokens += other.PromptTokens }
    // ... ignores sentinel/zero fields
}
```

**Why:** A caller writing `if x.PromptTokens > 0 { total += x.PromptTokens }`
for each field is a bug waiting for a new field to be added.

## Registries beat switches for variants

When "adding a variant" should be a one-line change, use a map.

```go
var providers = map[Provider]providerFactory{
    ProviderAnthropic: func(...) (llm.Model, error) { ... },
    ProviderGoogle:    func(...) (llm.Model, error) { ... },
}
// The "unsupported provider" error list is derived from the map.
```

**Why:** A switch forces the error message and every `case` arm to be
kept in sync by hand. A map derives both.

## Typed accessors for untyped maps

When storing values in a `map[string]any`, define a package-level const
for the key and a typed accessor for the value.

```go
const thoughtSignatureKey = "google_thought_signature"

func thoughtSignature(m llm.Message) []byte {
    sig, _ := m.ProviderMetadata[thoughtSignatureKey].([]byte)
    return sig
}
```

**Why:** Hardcoding `"google_thought_signature"` at the write site and
every read site is silent typo-bait. The const + accessor makes typos
compile errors.

## Fail loud on unknown inputs

When mapping one enum to another, handle the unknown case explicitly. No
silent fallthrough.

```go
if mapped, known := jsonSchemaTypes[t]; known {
    s.Type = mapped
} else {
    fmt.Fprintf(os.Stderr, "unknown JSON Schema type %q\n", t)
}
```

**Why:** A silent fallthrough surfaces far from the origin as an opaque
provider error. A stderr warning names the root cause at the site.

## Centralize `//nolint` pragmas in helpers

Wrap the pragma in one named helper with a doc comment. Don't sprinkle
pragmas at call sites.

```go
// clampInt32 saturates n into int32 range. Centralizes the //nolint:gosec
// with an explicit bounds check so call sites stay clean.
func clampInt32(n int) int32 {
    if n > math.MaxInt32 { return math.MaxInt32 }
    if n < math.MinInt32 { return math.MinInt32 }
    return int32(n) //nolint:gosec
}
```

**Why:** Call-site pragmas hide the invariant and accumulate copy-paste
drift. One helper = one invariant.

## Output contract: stdout vs stderr

Primary output (the "product" of the command) goes to stdout. Everything
else — progress, warnings, config — goes to stderr through a
prefix-free logger. Never use the default `log.Printf` for diagnostics:
it adds timestamps and breaks stdout redirection for callers.

```go
var stderr = log.New(os.Stderr, "", 0)

stderr.Printf("Warning: %v", err) // diagnostic
fmt.Println(result)               // product
```

**Why:** Callers doing `cassandra > review.md` must get clean product
output. Timestamped log noise on stderr also breaks downstream parsers.

## Test doubles: forward ctx

A mock/stub of a `context.Context`-accepting interface MUST forward ctx.
Check `ctx.Err()` at entry and return it — matching real SDK behavior.
Add at least one cancellation-propagation test at the double's boundary.

```go
// bad — silently ignores ctx
func (m *mockLLM) GenerateContent(_ context.Context, ...) { ... }

// good — behaves like a real provider under cancellation
func (m *mockLLM) GenerateContent(ctx context.Context, ...) {
    if err := ctx.Err(); err != nil { return nil, err }
    ...
}
```

**Why:** A mock that ignores ctx makes every cancellation test pass even
after a regression in ctx forwarding — the tests are verifying the wrong
thing.

## Document paired edits in REVIEWERS.md

When two symbols must change together (a const and a CLI default, a
tool name and a DESIGN.md claim), list the pair in a `REVIEWERS.md`
under the relevant scope.

```
# llm/REVIEWERS.md
- `submitReviewToolName` ↔ `DESIGN.md §Technical Decisions 4`.
- `DefaultMaxTokens` ↔ CLI `--max-tokens` default in `cmd/ai_reviewer`.
```

**Why:** The compiler can't enforce these pairings; reviewers must.
Making the pair explicit means no reader has to reconstruct it from
memory.

## Prefer stdlib idioms

Use the standard library primitives — they're tested, they tell the
reader what the code is doing, and they usually avoid an allocation.

```go
// bad
for k, v := range src { dst[k] = v }            // → maps.Copy(dst, src)
sb.WriteString(fmt.Sprintf("- %s\n", name))     // → fmt.Fprintf(&sb, "- %s\n", name)

// good
maps.Copy(dst, src)
fmt.Fprintf(&sb, "- %s\n", name)
```

**Why:** Idiomatic code reads faster and passes the golangci-lint
analyzers (`mapsloop`, `QF1012`) that catch drift in new code.
