# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*
An autonomous code review tool built in Go. This tool uses `langchaingo` to interface with supported LLMs (such as Google's Gemini or Anthropic's Claude) and provides structured, actionable code reviews using a `Do / Try / Consider` feedback framework.

## Features

- **Local Git Diff Review**: Review your local uncommitted changes against a base branch before pushing.
- **Provider Agnostic**: Natively supports Anthropic and Google models through a unified abstraction.
- **Agentic Context Gathering**: The LLM agent operates in a ReAct loop and has access to repository tools (like reading files and glob matching) to autonomously gather surrounding context about your codebase before finalizing feedback.

## Requirements

- Go 1.24.4+
- Bazel 8.6.0 (if building with `bzlmod`)
- A valid API key for your chosen LLM provider (Google Gemini or Anthropic Claude).

## Installation

Build the binary using standard Go commands:
```bash
go build -o ai-review-agent main.go
```

*(Alternatively, you can build using Bazel: `bazel build //...`)*

## Usage

### Review Local Changes
To review local uncommitted changes against a specific branch:

```bash
./ai-review-agent \
  --diff origin/main \
  --provider google \
  --model gemini-1.5-pro \
  --provider-api-key "YOUR_API_KEY"
```

## CLI Options

| Flag | Description | Default | Required |
|---|---|---|---|
| `--cwd` | Working directory | | No |
| `--diff` | Review a git diff against the specified branch | `main` | **Yes** |
| `--provider` | LLM provider to use (`google`, `anthropic`) | | **Yes** |
| `--model` | LLM provider's specific model ID | | **Yes** |
| `--provider-api-key` | API key for the selected provider | | **Yes** |
| `--main_guidelines` | Path to a file overriding the built-in main guidelines | | No |

## Architecture

The project features a lean, custom native Go ReAct loop. Tools for codebase context gathering are injected securely through `langchaingo` using the specific model's native Function Calling capabilities.
