# Cassandra - System Design

This document details the architecture, technical decisions, and layout of the Cassandra AI Review Agent. It is intended for both human maintainers and AI coding assistants working in this repository.

## High-Level Architecture

The system is designed as a CLI-driven, autonomous AI worker. It acts essentially as a ReAct (Reasoning and Acting) loop, leveraging the Function Calling (Tool Use) capabilities of modern LLMs to explore codebases locally or remotely before finalizing its code review.

### 1. CLI Entrypoint (`main.go`)
- Parses user intent. Supports reviewing local git changes (`--diff`).
- Dynamically accepts `--provider` (`google` or `anthropic`), `--model`, and `--provider-api-key` to abstract away the underlying LLM dependency.

### 2. LLM Abstraction (`llmutil/client.go`)
- Wraps the [`langchaingo`](https://github.com/tmc/langchaingo) framework.
- The system depends heavily on `langchaingo`'s `llms.Model` and `llms.Tool` interfaces. This abstraction guarantees we do not need to rewrite the ReAct loop logic or tool serialization if we switch from Gemini to Claude (or add OpenAI in the future).

### 3. Tool Registry (`tools/registry.go` & `tools/local_tools.go`)
- Acts as the available toolkit for the LLM. 
- Depending on the execution mode, the registry exposes underlying tool implementations:
  - `read_file`: Safely reads the contents of a requested file up to a character limit.
  - `glob_files`: Lists repository files matching a pattern to help the LLM discover where definitions might live.

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
   - *Note on Bazel 9:* We intentionally stick to Bazel 8 at this time to avoid known incompatibilities between stable releases of `rules_go` and the removal of macOS `current_xcode_config` targets in Bazel 9. We also explicitly pin the internal Go SDK to `1.24.4` to satisfy `langchaingo`.

4. **Structured Feedback Extraction**
   - Code reviews in this system follow a `Do / Try / Consider` framework. Rather than forcing the primary reasoning process to output JSON directly (which can degrade reasoning quality), the system allows the first "Agent" pass to output free-form markdown, followed by a secondary extraction LLM call dedicated entirely to formatting that markdown into structured JSON boundaries.

## Future work

- **Pull Request Support**: Re-introduce `--pr` support for reviewing remote GitHub Pull Requests. This will require a set of tools mirroring the local tools but drawing input from the PR's (remote) branch
