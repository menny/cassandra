# Cassandra - System Design

This document details the architecture, technical decisions, and layout of the Cassandra AI Review Agent. It is intended for both human maintainers and AI coding assistants working in this repository.

## High-Level Architecture

The system is designed as a CLI-driven, autonomous AI worker. It acts essentially as a ReAct (Reasoning and Acting) loop, leveraging the Function Calling (Tool Use) capabilities of modern LLMs to explore codebases locally or remotely before finalizing its code review.

### 1. AI Review Entrypoint (`cmd/ai_reviewer/main.go`)
- Parses user intent. Supports reviewing changes between commits/branches (`--base` and `--head`).
- Dynamically accepts `--provider` (`google` or `anthropic`), `--model`, and `--provider-api-key` to abstract away the underlying LLM dependency.
- **Main Guidelines**: Requires a `--main-guidelines` argument. This can be:
  - A path to a local Markdown file (absolute or relative to the current directory).
  - The name of a pre-defined prompt from the `reviewer_prompts/` library (e.g., `asana-do-try-consider`, `google`, `palantir`).
- Coordinates the flow from git diff extraction to system prompt building, and finally running the review agent.

### 2. GitHub Utility (`cmd/github/main.go`)
- A standalone utility used by the GitHub Action to manage the lifecycle of a review on a PR.
- **Actions**:
  - `add-reaction`: Adds a visual status indicator (e.g., 'eyes') to the PR body.
  - `remove-reaction`: Cleans up reactions after the review completes.
  - `post-comment`: Manages a "persistent" comment by searching for a unique tag (e.g., `<!-- cassandra-ai-review -->`) and either creating a new comment or updating an existing one.
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

### 3. Tool Registry (`tools/`)
- **Interface**: The `ToolDispatcher` interface (defined in `core/agent.go`) is the minimal set of methods the Agent needs:
  ```go
  type ToolDispatcher interface {
      ToTools() []llm.ToolDef
      HandleCall(tc llm.ToolCall) (string, error)
  }
  ```
- **Implementation**: `tools.Registry` (in `tools/registry.go`) implements this interface. It stores a list of `llm.ToolDef` and a map of `ToolHandler` functions.
- **Local Tools**: High-level tools implemented in `tools/local_tools.go`:
  - `read_file`: Reads file content from the local disk.
  - `glob_files`: Finds files matching a pattern or extension.
  - `grep_files`: Searches for patterns in the repository using `git grep`.

### 4. LLM Abstraction (`llm/`)
- **Interface**: `llm.Model` (in `llm/llm.go`) is the provider-agnostic interface:
  ```go
  type Model interface {
      GenerateContent(ctx context.Context, messages []Message, tools []ToolDef, maxTokens int) (*Response, error)
      GenerateStructuredContent(ctx context.Context, messages []Message, schema map[string]any, config StructuredConfig) (*Response, error)
  }
  ```
- **Shared Types**: Standardizes `Message`, `ToolDef`, `ToolCall`, `ToolResult`, and `Response` across all providers.
- **Provider Implementations**:
  - `llm/anthropic`: Uses `github.com/anthropics/anthropic-sdk-go`.
  - `llm/google`: Uses `google.golang.org/genai`.
- **Factory**: `llm/factory/factory.go` provides a single `New` function to construct the appropriate `Model` implementation.

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
     - **Anthropic**: Instructs the model to use a `submit_review` tool with the target schema.
     - **Google**: Uses `ResponseMIMEType = "application/json"` and `ResponseSchema`.

## Output Contract

- **Stderr**: All diagnostic and progress output (configuration summary, iteration progress, tool calls).
- **Stdout**: The final review text (markdown). This allows for easy redirection: `cassandra ... > review.md`.

## Structured Output JSON (`--output-json`)

When the `--output-json` flag is provided, the system performs a post-processing step to convert the markdown review into a structured JSON file.

### JSON Schema:
```json
{
  "raw_free_text": "...",
  "approval": {
    "approved": true,
    "rationale": "..."
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
