# Cassandra - System Design

This document details the architecture, technical decisions, and layout of the Cassandra AI Review Agent. It is intended for both human maintainers and AI coding assistants working in this repository.

## High-Level Architecture

The system is designed as a CLI-driven, autonomous AI worker. It acts essentially as a ReAct (Reasoning and Acting) loop, leveraging the Function Calling (Tool Use) capabilities of modern LLMs to explore codebases locally or remotely before finalizing its code review.

### 1. AI Review Entrypoint (`cmd/ai_reviewer/main.go`)
- Parses user intent. Supports reviewing changes between commits/branches (`--base` and `--head`).
- Dynamically accepts `--provider` (`google` or `anthropic`), `--model`, and `--provider-api-key` to abstract away the underlying LLM dependency.
- **Main Guidelines**: Defaults to `general`. This can be:
  - A path to a local Markdown file (absolute or relative to the current directory).
  - The name of a pre-defined prompt from the internal library (e.g., `asana-do-try-consider`, `google`, `palantir`).
- **Approval Evaluation Guidelines**: Defines the criteria for `APPROVE`, `REQUEST_CHANGES`, and `COMMENT` verdicts.
  - Defaults to an internal "velocity-first" prompt.
  - Can be overridden via `--approval-evaluation-prompt-file`.
- **Review Pipeline**: `--review-pipeline-file` (TOML key `review-pipeline-file`, Action input `review_pipeline_file`) selects a flat set of concurrent subagents to run before the main review. See [Review Pipeline](#review-pipeline).
- Coordinates the flow from git diff extraction to system prompt building, the optional review pipeline, and finally running the review agent.

### 2. GitHub Utility (`cmd/github/main.go`)
- A standalone utility used by the GitHub Action to manage the lifecycle of a review on a PR.
- **Actions**:
  - `add-reaction`: Adds a visual status indicator (e.g., 'eyes') to the PR body.
  - `remove-reaction`: Cleans up reactions after the review completes.
  - `post-comment`: Manages a "persistent" architectural comment. It ensures a single source of truth by updating the latest comment matching a unique tag (e.g., `<!-- cassandra-main-... -->`) and deleting any redundant duplicates.
  - `post-structured-review`: Posts a formal GitHub Pull Request Review. It supports inline line-level comments, handles review dismissals, and implements multi-level fallback logic for API resilience.
- Built as a separate binary to minimize the footprint and dependencies required for basic GitHub interactions.

### 3. Core AI Engine (`core/agent.go`)
- Implements a simple, typed Go ReAct loop in `RunReview`.
- **The ReAct Loop Flow**:
  1. **Initialization**: Starts with a system prompt and the user request (containing the git diff).
  2. **Model Call**: Invokes the `llm.Model.GenerateContent` method with the current message history and available tools.
  3. **Response Analysis**:
     - **No Tool Calls**: If the model returns text without tool calls, it's considered the final review. The loop terminates.
     - **Tool Calls**: If the model requests tool invocations, the agent executes them using the `ToolDispatcher`.
  4. **Tool Execution**: The `executeToolCalls` method handles calling the registered handlers, collects results, and appends them to the message history.
  5. **Iteration**: The loop repeats until a final answer is produced or the `maxIterations` cap is reached.
  6. **Cap Reached**: If the cap is reached, the agent forces a final review call by appending a system message and stripping tool definitions from the next LLM call.

### 4. Tool Registry (`tools/`)
- **Interface**: The `ToolDispatcher` interface (defined in `core/agent.go`) is the minimal set of methods the Agent needs: one to enumerate available tools and one to dispatch a tool call by name with its context and arguments. Read `core/agent.go` for the authoritative signature.
- **Implementation**: `tools.Registry` (in `tools/registry.go`) implements this interface. It stores a list of `llm.ToolDef` and a map of `ToolHandler` functions.
- **Local Tools**: High-level tools implemented under `tools/` and registered via `tools.RegisterLocalTools`:
  - `read_file`: Reads file content from the local disk.
  - `get_file_diff`: Retrieves the git diff for a specific file between base and head branches.
  - `glob_files`: Finds files matching a pattern or extension.
  - `grep_files`: Searches for patterns in the repository using `git grep`.
  - `wishlist_tool`: Records a capability gap the LLM encountered during a review into a configurable directory for future tool development.

### 5. LLM Abstraction (`llm/`)
- **Interface**: `llm.Model` (in `llm/llm.go`) is the provider-agnostic interface. It exposes two generation methods: `GenerateContent` for free-form responses with optional tool use, and `GenerateStructuredContent` for schema-constrained output. Read `llm/llm.go` for the authoritative signatures.
- **Shared Types**: Standardizes `Message`, `ToolDef`, `ToolCall`, `ToolResult`, and `Response` across all providers.
- **Provider Implementations**:
  - `llm/anthropic`: Uses `github.com/anthropics/anthropic-sdk-go`.
  - `llm/google`: Uses `google.golang.org/genai`.
- **Factory**: `llm/factory/factory.go` provides a single `New` function to construct the appropriate `Model` implementation. Providers are registered via the package-level `providers` map; adding a provider is a one-line map entry.

#### Shared Contracts

The following symbols in `llm/llm.go` are the load-bearing contracts that every
provider implementation depends on. See [`llm/AGENTS.md`](llm/AGENTS.md) for
the companion implementation rules.

- **`llm.UnknownUsage()`** — sentinel `Usage` (`PromptTokens: -1`,
  `OutputTokens: -1`) meaning "provider reported no token data". Providers
  seed `Response.Usage` with this and overwrite on success. When a provider
  exposes multiple counters (e.g. cache vs. non-cache), every counter path
  must be covered by the usage-presence guard.
- **`llm.StructuredConfig.Resolve(defaultModel)`** — returns
  `(model, maxTokens)` with `ModelOverride` and `DefaultMaxTokens`
  applied. `GenerateStructuredContent` implementations call this to
  eliminate per-provider defaulting boilerplate.
- **`llm.DefaultMaxTokens = 8192`** — fallback token budget for both
  `GenerateContent` and `GenerateStructuredContent` when the caller
  does not specify one. Kept in lockstep with the CLI `--max-tokens`
  default in `cmd/ai_reviewer`.
- **`llm.retry[T]`** (package-private) — the canonical exponential-back-off
  + ctx-cancellation loop. `RetryingModel` wraps the interface via this
  helper; any new cross-cutting `llm.Model` wrapper (caching, telemetry,
  rate-limiting) should follow the same composition rather than
  re-implementing the loop.

## Review Pipeline

A separate TOML file defines the pipeline. It has a `max_concurrency` setting (default `4`) and an ordered array of `[[subagents]]` entries:

```toml
max_concurrency = 4

[[subagents]]
title = "PR summary"
description = "Summarize the pull request changes"
prompt_file = "pre-review-summary.md"
required = true
user_visible = true
reviewer_input = true
# MCP tools are disabled by default; opt in for this subagent only.
# allow_mcps = true

# Optional per-field overrides; other fields inherit the main LLM configuration.
# [subagents.llm]
# model = "YOUR_MODEL_ID"
```

- **Flat concurrency**: Subagents receive the same review request and read/context tool environment but use isolated conversation histories; they do not receive every tool available to the main reviewer. They run concurrently up to `max_concurrency`, without dependencies or result chaining; the main reviewer starts only after the pipeline succeeds.
- **Tool boundary**: All normal built-in tools are allowed except privileged T3 tools (including `link_pull_request`, `list_thread_pull_requests`, `orchestrator_capabilities`, and `t3_thread_launch`), Omnigent tools, `update_review_verdict`, and `post_pr_comment`. MCP tools default to disabled independently for each subagent. Setting `allow_mcps = true` on that subagent enables configured MCP tools, not the excluded built-ins.
- **Paths and LLM inheritance**: Resolve `prompt_file` relative to the pipeline file, not the review working directory. `[subagents.llm]` can override `model`, `provider`, `provider_api_key`, `provider_url`, `max_tokens`, and `provider_options`; omitted fields inherit the corresponding main LLM settings field-by-field. A model-only override preserves all other main settings.
- **Explicit output policy**: `required`, `user_visible`, and `reviewer_input` all default to `false`. `user_visible` gates only a successful report, not progress, tool activity, usage, retries, or clarification questions. User-visible reports are emitted in completion order. Reviewer inputs are assembled in declaration order for deterministic review context, regardless of execution timing. These ordering rules include optional-failure reports, which bypass the output flags.
- **ACP reporting**: The plan contains one aggregate pipeline entry, not a checklist entry per subagent. Progress and tool calls are attributed to the originating stage, with stage-scoped tool-call identities to avoid collisions between concurrent subagents. Usage and retry updates retain stage attribution. Clarification questions are serialized and attributed to their subagent. `session/cancel` cancels the active initial review and all pipeline subagents as well as in-flight post-review turns.
- **Failure policy**: A required subagent failure cancels other subagents and aborts the review. An optional failure allows the review to continue, but must be reported to both the user and reviewer, including the error, even when `user_visible` and `reviewer_input` are false.
- **Action path preservation**: The Action resolves the pipeline file as a whole (workspace/working-directory first, then action-directory fallback, with absolute paths passed through). It passes that original path rather than copying TOML contents into a temporary file, preserving the base directory for relative prompts.
- **Migration boundary**: Old pre-review flags, TOML keys, and Action inputs are removed; old configuration is rejected rather than accepted as aliases. See [migration instructions](README.md#migrating-old-configuration) and the [example pipeline](examples/review-pipeline.toml).

## Technical Decisions

1. **Go for Speed and Distribution**
   - We utilize Go to ensure the tool is a fast, easily distributable binary.

2. **Native Loop over Frameworks**
   - We intentionally decided against complex agent frameworks. A native Go `for` loop ensures the state and termination conditions are transparent and strictly typed.

3. **Bazel 8 with BzlMod**
   - The repository uses Bazel `8.6.0` alongside standard `go.mod` resolution.

4. **Structured Feedback Extraction (Target Architecture)**
   - The system separates reasoning from formatting. The primary review pass is free-form markdown to optimize for reasoning quality.
   - **Secondary Extraction**: A second, optional LLM call converts the markdown review into a structured JSON representation.
   - **Implementation strategy**:
     - **Anthropic**: Forces the model to call a synthetic tool whose name is pinned by the package-level const `llm/anthropic.submitReviewToolName` (`"submit_review"`). This name is a stable contract — downstream consumers may match on it, so changes must be coordinated with any code that introspects the structured response.
     - **Google**: Uses `ResponseMIMEType = "application/json"` and `ResponseSchema`.

## Output Contract

See [AGENTS.md — Output Contract](AGENTS.md#output-contract). All diagnostic and progress output goes to stderr; the final review text goes to stdout only.

## Structured Output JSON (`--output-json`)

When the `--output-json` flag is provided, the system performs a post-processing step to convert the markdown review into a structured JSON file.

### JSON Schema:
```json
{
  "raw_free_text": "...",
  "approval": {
    "approved": true,
    "rationale": "...",
    "action": "APPROVE | REQUEST_CHANGES | COMMENT"
  },
  "non_specific_review": "...",
  "files_review": [
    {
      "path": "path/to/file",
      "lines": "optional line range",
      "review": "review for this chunk"
    }
  ]
}
```

This post-processing ensures the main reasoning pass remains unconstrained, while providing a machine-readable format for integration with other tools (e.g., CI/CD pipelines, GitHub Actions).

## Review Resilience & State Management

The GitHub interaction layer is designed to be highly resilient against common CI failure modes:

1. **API Permission Fallbacks**: If the `GITHUB_TOKEN` is not permitted to submit formal "Approve" actions (a common default setting), the utility automatically downgrades the review to a neutral `COMMENT` while preserving all feedback.
2. **Line Hallucination Recovery**: If the LLM suggests a comment on a line that is not part of the PR diff (a 422 error), the system automatically retries the submission without inline comments, appending the feedback to the main review body instead.
3. **Clean PR State**:
   - **Type-Specific Tagging**: Uses distinct tag prefixes (`cassandra-main-` and `cassandra-inline-`) to unambiguously identify different comment types.
   - **Automatic Cleanup**: Before posting a new review, the system dismisses previous bot reviews and can optionally delete stale inline comments to keep the conversation tab focused.
   - **Identity Verification**: When possible, it verifies both the `Tag` and the `Author` identity (handling `403 Forbidden` on `/user` gracefully) to ensure it only modifies its own comments.
