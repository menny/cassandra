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
- **GitHub Utility**: A standalone tool for PR lifecycle management (reactions, comments) implemented in `cmd/github/main.go`.

## Project Structure

- `cmd/ai_reviewer/`: Main AI review agent entry point.
- `cmd/github/`: GitHub utility for PR interactions.
- `core/`: Core agent logic, ReAct loop, and prompts.
- `llm/`: LLM provider abstractions (see [llm/AGENTS.md](llm/AGENTS.md)).
- `tools/`: Tool registry and MCP servers (see [tools/AGENTS.md](tools/AGENTS.md)).

## Formatting & Linting

This project uses `aspect_rules_lint` (v2.3.0) to integrate standard tooling directly into the Bazel graph:
- **Formatter**: You MUST ensure any code or configuration generated is properly formatted. Run `bazel run //:format` from the root of the workspace to auto-format all supported files (Go, Starlark/Bazel, YAML) before committing.
- **Linter (`golangci-lint`)**: A `.golangci.yml` is maintained at the root. You MUST rely exclusively on the hermetic Bazel Go toolchain to run the linter natively by executing: `bazel run @rules_go//go -- run github.com/golangci/golangci-lint/cmd/golangci-lint@v1.64.5 run ./...`. Do not assume the presence of external CLI wrappers like `aspect` or `golangci-lint` on the host machine.

## Architecture Reference

Before introducing major changes or restructuring the review loop, please read the **[DESIGN.md](DESIGN.md)** document. It covers:
- The decision to use a native Go ReAct loop rather than complex Python graphs.
- The rationale behind the custom Tool Registry.

## Code Style Reference

**[CODE_STYLE.md](CODE_STYLE.md)** captures recurring coding patterns with rule + example + rationale for each: error handling idioms (`errors.New`, `%w`, `errors.As`), helper extraction, sentinel handling, registries over switches, typed accessors for untyped maps, centralized `//nolint`, stdout/stderr discipline, test-double `ctx` forwarding, paired-edit documentation, and stdlib-idiom preferences.

Consult it when writing new code or when a review comment cites an idiom you don't recognize. AGENTS.md states *what MUST be done*; CODE_STYLE.md shows *how to write it so it matches the rest of the codebase*.

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
  - *Exception*: Tools that proxy to external systems with dynamic schemas (e.g., Model Context Protocol tools) may use `map[string]any` since their arguments are discovered at runtime and cannot be statically typed.
- **Error Propagation**: Return errors from the handler; the `Agent` is responsible for formatting these as "error: ..." strings for the LLM to process.

### 2. Diagnostic Reporting
Progress reporting is abstracted via the `core.Reporter` interface.
- **Interface Usage**: Do not write directly to `os.Stderr` within the ReAct loop logic. Use `a.reporter.ReportIteration(...)`, etc.
- **Default Phrasing**: Use distinct phrasing for different stages (e.g., "Iteration X..." vs "Formulating final review...") to provide high-signal feedback to the user.

### 3. Testing Standards
- **Mock Isolation**: When using `mockLLM` or similar history-tracking doubles, you MUST perform a deep copy of the message slice and its internal slices (`ToolCalls`, `ToolResults`). Shallow copies will lead to state contamination across iterations.
- **Avoid Goroutine Leaks**: When starting background tasks or mock servers in tests, ALWAYS use a cancelable `context.Context` (via `context.WithCancel`) and ensure it is canceled (via `defer cancel()`) when the test completes.
- **Safer JSON Construction**: Avoid manual string concatenation for JSON arguments in tests. Use `json.Marshal` or existing helpers to ensure paths (especially in `t.TempDir()`) containing special characters do not break the test payload.
- **Negative Testing**: Every new tool or core logic change SHOULD include error-handling test cases (e.g., malformed JSON, missing files, individual tool failures).
- **Test Doubles Must Forward `ctx` and Arguments**: A stub or mock implementing a `context.Context`-accepting interface MUST forward the received `ctx` and arguments to any internal delegate. Substituting `context.Background()` silently breaks cancellation tests. Every context-accepting method on a test double needs at least one cancellation-propagation test.
- **Parallel Method Coverage**: When an interface has parallel methods that share a wrapper (e.g. `GenerateContent` / `GenerateStructuredContent` behind `RetryingModel`), behavioral tests (retries, cancellation, error propagation) MUST cover both methods. Generics or composition that unify them in production do not remove the need for independent test coverage.

### 4. Performance Mindfulness
- **Redundant I/O**: When walking the directory tree for configuration or guidelines (like `REVIEWERS.md`), use directory-based caching to avoid redundant disk lookups. If a directory subtree has already been searched for a specific filename, terminate the walk-up early.

### 5. Token Efficiency
- **Mindful Generation**: When designing LLM interactions (prompts, schemas, or post-processing), prioritize strategies that minimize output tokens. Avoid asking the model to echo large amounts of existing text; instead, prefer manual assembly or reference-based extraction to reduce latency and API costs.

### 6. Prompt Engineering & Prefix Caching
- **Zone ordering**: The system prompt is divided into three zones ordered from most- to least-stable (see `core/prompts/library/README.md` for the full Prompt Developer Guide). When editing `BuildSystemPrompt` or any prompt file, **never inject dynamic or per-request data (file paths, PR metadata, commit SHAs) into Zone 1 or Zone 2**. All such content belongs in Zone 3 (the dynamic suffix).
- **Byte-for-byte stability**: Any change to `reviewer_prompt.md`, a library guideline file, or the `approval_evaluation_prompt.md` will invalidate the prefix cache for every subsequent review using that configuration. Review such changes carefully and keep them minimal.
- **New sections**: If you add a new semi-static section to `BuildSystemPrompt`, insert it between the existing Zone 2 entries and before the Zone 3 block (AGENTS.md / REVIEWERS.md), maintaining the stable-prefix ordering described in the design.

### 7. Lint Exceptions
- **Centralize `//nolint` pragmas**: If a construct legitimately needs a lint exception (e.g. `int32` narrowing after a bounds check, an intentionally-unused receiver), wrap it in a named helper so the pragma lives in one place with a comment explaining the invariant. Do not sprinkle `//nolint:gosec` or similar pragmas at multiple call sites — the invariant becomes invisible and copy-paste drift accumulates. See `clampInt32` in `llm/google/provider.go` as the canonical example.

### 8. Defensive Tool Implementation
Tools that interact with external data (files, network, pipes) MUST be resilient to resource exhaustion and hangs.
- **Hard Safety Limits**: Every tool MUST enforce an output size limit (e.g., 40KB) and a per-operation memory cap.
- **Streaming over Loading**: Do NOT use `os.ReadFile` or `io.ReadAll` on potentially large sources. Use `io.LimitReader` and `bufio.Reader` to process data in chunks.
- **Pseudo-file Protection**: Never trust `os.Stat().Size()` for OOM prevention (it returns 0 for pseudo-files like `/dev/zero`). Always use `io.LimitReader` with a hard cap.
- **Cancellation Checks**: Every loop that performs I/O or intensive computation MUST check `ctx.Err()` in each iteration to ensure the tool can be interrupted by the ReAct loop timeout.

## Security Standards

### 1. GitHub Action Input Safety
To prevent command injection vulnerabilities, you MUST NOT interpolate user-controlled GitHub Action inputs (e.g., `${{ inputs.val }}`) or potentially unsafe GitHub context variables (e.g., `${{ github.event.pull_request.title }}`) directly into shell scripts.
- **Use Environment Variables**: Map user-controlled inputs to environment variables in the `env:` block of the step.
- **Reference Safely**: Use the environment variable within the script (e.g., `run: my-tool --arg "$VAL"`). This ensures the shell treats the input as literal data.
- **Exceptions**: System-provided variables that are not user-controlled and follow a strict format (e.g., `${{ github.repository }}`, `${{ github.action_path }}`, `${{ github.workspace }}`, `${{ runner.temp }}`) can be used directly if it improves readability and doesn't introduce risk.

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
