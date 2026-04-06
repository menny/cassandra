# Cassandra: AI Agent Guidelines

## LLM Providers & Models

Cassandra supports multiple LLM providers. For the most up-to-date list of available models, their IDs, and capabilities, refer to the official documentation:

- **Google Gemini**: [Gemini API Models](https://ai.google.dev/gemini-api/docs/models/gemini)
- **Anthropic Claude**: [Anthropic Claude Models](https://docs.anthropic.com/en/docs/about-claude/models)

When configuring Cassandra (via CLI or GitHub Action), ensure you use the correct Model ID as specified in these links.

## Repository Technical Details

- **Language**: Go (targeting `1.24.4+`).
- **Build System**: **Bazel 8.6.0** with **BzlMod**.
  - Standard `go.mod` and `go.sum` files are maintained, and Bazel resolves Go dependencies via Gazelle's `go_deps` extension in `MODULE.bazel`.
  - *Note:* Do not upgrade to Bazel 9 without verifying `rules_go` compatibility, as recent Bazel releases removed Xcode configuration targets required by the legacy CGo pipeline on macOS.
- **LLM Abstraction**: Provider-agnostic `llm.Model` interface defined in `llm/llm.go`. Implementations in `llm/anthropic` (using `github.com/anthropics/anthropic-sdk-go`) and `llm/google` (using `google.golang.org/genai`). Construct via `llm/factory.New()`.

## Formatting & Linting

This project uses `aspect_rules_lint` (v2.3.0) to integrate standard tooling directly into the Bazel graph:
- **Formatter**: You MUST ensure any code or configuration generated is properly formatted. Run `bazel run //:format` from the root of the workspace to auto-format all supported files (Go, Starlark/Bazel, YAML) before committing.
- **Linter (`golangci-lint`)**: A `.golangci.yml` is maintained at the root. You MUST rely exclusively on the hermetic Bazel Go toolchain to run the linter natively by executing: `bazel run @rules_go//go -- run github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.5 run ./...`. Do not assume the presence of external CLI wrappers like `aspect` or `golangci-lint` on the host machine.

## Architecture Reference

Before introducing major changes or restructuring the review loop, please read the **[DESIGN.md](file:///Users/mennyevendanan/dev/menny/cassandra/DESIGN.md)** document. It covers:
- The decision to use a native Go ReAct loop rather than complex Python graphs.
- The rationale behind the custom Tool Registry.
- The `Do / Try / Consider` feedback format.

## Output Contract

All **diagnostic and progress output** (configuration summary, ReAct iteration progress, tool invocations, warnings) MUST be written to **stderr**. Only the **final review text** goes to **stdout**. This allows callers to capture the review cleanly via redirection (e.g., `cassandra --diff main > review.md`) without interleaving noise.

- `os.Stderr` / `log.New(os.Stderr, "", 0)` — for progress, config, warnings
- `fmt.Println` / `fmt.Fprintf(os.Stdout, ...)` — for the final review result only

When adding new logging or output anywhere in the codebase, apply this rule strictly.

## Engineering Guidelines

### 1. Tool Implementation Pattern
To maintain consistency and type safety, all new tools MUST follow the standardized argument handling pattern:
- **Struct-based Arguments**: Define a local anonymous struct (or a named struct if reused) to represent tool parameters.
- **Explicit Unmarshaling**: Use `tc.UnmarshalArguments(&args)` within the tool handler. Do not perform manual type assertions or "missing key" checks on a map.
- **Error Propagation**: Return errors from the handler; the `Agent` is responsible for formatting these as "error: ..." strings for the LLM to process.

### 2. Diagnostic Reporting
Progress reporting is abstracted via the `core.Reporter` interface.
- **Interface Usage**: Do not write directly to `os.Stderr` within the ReAct loop logic. Use `a.reporter.ReportIteration(...)`, etc.
- **Default Phrasing**: Use distinct phrasing for different stages (e.g., "Iteration X..." vs "Formulating final review...") to provide high-signal feedback to the user.

### 3. Testing Standards
- **Mock Isolation**: When using `mockLLM` or similar history-tracking doubles, you MUST perform a deep copy of the message slice and its internal slices (`ToolCalls`, `ToolResults`). Shallow copies will lead to state contamination across iterations.
- **Safer JSON Construction**: Avoid manual string concatenation for JSON arguments in tests. Use `json.Marshal` or existing helpers to ensure paths (especially in `t.TempDir()`) containing special characters do not break the test payload.
- **Negative Testing**: Every new tool or core logic change SHOULD include error-handling test cases (e.g., malformed JSON, missing files, individual tool failures).

### 4. Performance Mindfulness
- **Redundant I/O**: When walking the directory tree for configuration or guidelines (like `REVIEWERS.md`), use directory-based caching to avoid redundant disk lookups. If a directory subtree has already been searched for a specific filename, terminate the walk-up early.

### 5. Token Efficiency
- **Mindful Generation**: When designing LLM interactions (prompts, schemas, or post-processing), prioritize strategies that minimize output tokens. Avoid asking the model to echo large amounts of existing text; instead, prefer manual assembly or reference-based extraction to reduce latency and API costs.

## Git Commit Guidelines

When committing changes on behalf of the user, strictly follow these commit message rules based on Conventional Commits:

1. **Type**: Prefix the commit with the type of change:
   - `feat:` for new features/tools.
   - `fix:` for bug fixes.
   - `docs:` for documentation updates.
   - `refactor:` for code changes that neither fix a bug nor add a feature.
   - `chore:` for build process or auxiliary tool changes.
2. **Subject Line**:
   - Keep it concise (under 50 characters).
   - Use the imperative mood (e.g., "Add tool" not "Added tool").
   - Do not capitalize the first letter.
   - Do not end with a period.
3. **Body** (if applicable):
   - Leave a blank line between the subject and the body.
   - Wrap lines at 72 characters.
   - Explain *what* was changed and *why*, rather than *how* (the diff already shows how).

**Example Commit:**
```text
feat: add local diff parsing tool

Introduce a new context tool via the registry to parse uncommitted git diffs.
This allows the ReAct loop to analyze isolated lines of code changes before formulating the final review.
```

NOTE:
- Never push to a remote git repository without explicit request from the user.
- Prefer working on a new branch for every new session - the branch name should be meaningful in the context of the approved plan
