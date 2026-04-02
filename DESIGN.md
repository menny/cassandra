# Cassandra - System Design

This document details the architecture, technical decisions, and layout of the Cassandra AI Review Agent. It is intended for both human maintainers and AI coding assistants working in this repository.

## High-Level Architecture

The system is designed as a CLI-driven, autonomous AI worker. It acts essentially as a ReAct (Reasoning and Acting) loop, leveraging the Function Calling (Tool Use) capabilities of modern LLMs to explore codebases locally or remotely before finalizing its code review.

### 1. CLI Entrypoint (`main.go`)
- Parses user intent. Supports reviewing changes between commits/branches (`--base` and `--head`).
- Dynamically accepts `--provider` (`google` or `anthropic`), `--model`, and `--provider-api-key` to abstract away the underlying LLM dependency.

### 2. LLM Abstraction (`llm/`)
- Defines a provider-agnostic `llm.Model` interface and shared types (`Message`, `ToolDef`, `ToolCall`, `ToolResult`, `Response`) in `llm/llm.go`.
- Provider implementations live in sub-packages (`llm/anthropic`, `llm/google`), each backed by its respective first-party SDK (`github.com/anthropics/anthropic-sdk-go`, `google.golang.org/genai`).
- `llm/factory` is the single entry point for constructing a `Model`; no package outside `llm/factory` imports a provider sub-package directly.
- This design guarantees the ReAct loop (`core/agent.go`) and tool registry (`tools/`) are completely decoupled from provider-specific types.

### 3. Tool Registry (`tools/registry.go` & `tools/local_tools.go`)
- Acts as the available toolkit for the LLM. 
- Depending on the execution mode, the registry exposes underlying tool implementations:
  - `read_file`: Safely reads the contents of a requested file up to a character limit.
  - `glob_files`: Lists repository files matching a pattern to help the LLM discover where definitions might live.
  - `grep_files`: Searches for patterns across the repository using `git grep`, including unstaged changes. Supports optional case-insensitive search.

### 4. Core AI Engine (`core/agent.go`)
- Replaces heavy Python-based graph execution loops (like LangGraph).
- Implements a simple, typed Go loop:
  1. Sends the system prompt, guidelines, and diffs to the LLM.
  2. If the LLM responds with a `ToolCall`, the Agent executes the tool locally, appends the result to the message history, and loops.
  3. If the LLM responds with text containing the final review, the loop terminates.

## Technical Decisions

1. **Go**
   - We utilize Go to ensure the tool is a fast, easily distributable binary. Go's standard library provides robust primitives for concurrent tool execution (e.g., executing multiple file-reads concurrently via goroutines).

2. **Native Loop over LangGraph**
   - We intentionally decided against bringing in complex agent frameworks that obfuscate the state machine. The "Agent" is ultimately just a `for` loop communicating with an LLM and formatting text. Keeping this loop native inside Go ensures that the state and termination conditions strictly adhere to statically typed assertions rather than magic framework states.

3. **Bazel 8 with BzlMod**
   - The repository uses Bazel `8.6.0` alongside standard `go.mod` resolution (using Gazelle's `go_deps` extension).
   - *Note on Bazel 9:* We intentionally stick to Bazel 8 at this time to avoid known incompatibilities between stable releases of `rules_go` and the removal of macOS `current_xcode_config` targets in Bazel 9. The internal Go SDK is pinned to `1.24.4`.

4. **Structured Feedback Extraction (Target Architecture)**
   - Code reviews in this system are designed to follow a `Do / Try / Consider` framework. To ensure high reasoning quality, the system's target architecture separates reasoning from formatting:
     1. A primary "Agent" pass outputs free-form markdown (optimizing for LLM reasoning).
     2. A secondary extraction LLM call (using the `llm/` abstraction) formats that markdown into structured JSON findings.
   - *Note:* While the foundation for this is present in the `llm/` package, the secondary extraction pass is currently being integrated into the main execution flow.

## Output Contract

All diagnostic and progress output (configuration summary, ReAct iteration progress, tool invocations) is written to **stderr**. Only the final review text is written to **stdout**. This separation allows callers to cleanly capture the review output via shell redirection (e.g., `cassandra --base main --head my-branch > review.md`) without interleaving progress noise.

## Future work

- **Pull Request Support**: Re-introduce `--pr` support for reviewing remote GitHub Pull Requests. This will require a set of tools mirroring the local tools but drawing input from the PR's (remote) branch.

- **Concurrent Tool Execution**: When the LLM responds with multiple tool calls in a single turn, those calls are currently executed sequentially. A future improvement is to fan them out concurrently using goroutines — each tool handler is already stateless and side-effect-free, so a `sync.WaitGroup`-based approach is straightforward. This will noticeably reduce wall-clock latency on large diffs where the LLM explores several files simultaneously.

- **Structured Output Extraction**: The current system returns the final review as free-form markdown. A future enhancement is to add a second LLM pass that converts the markdown review into a structured JSON representation (e.g. a list of findings, each tagged by severity and category). The recommended approach is provider-specific:
  - **Anthropic**: Define a single `submit_review` tool whose JSON Schema matches the desired output shape. Instruct the model to call it unconditionally in the final turn. The tool's `arguments` field carries the structured payload.
  - **Google Gemini**: Set `GenerateContentConfig.ResponseMIMEType = "application/json"` and `ResponseSchema` to the target schema. The model returns JSON directly in the text content.
  - The structured output call should be a separate, optional step invoked after `RunReview` returns, keeping the primary reasoning pass free-form (forcing JSON output early degrades reasoning quality). The `llm/` abstraction provides the foundation for this by ensuring that both providers can be driven to produce structured results through the same unified `llm.Model` interface.
