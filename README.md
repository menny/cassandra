# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*

An autonomous code review tool built in Go. This tool provides structured, actionable code reviews using a `Do / Try / Consider` feedback framework.

## Features

- **Local Git Diff Review**: Review your local uncommitted changes against a base branch before pushing.
- **Provider Agnostic**: Natively supports Anthropic and Google models through a unified abstraction.
- **Agentic Context Gathering**: The LLM agent operates in a ReAct loop and has access to repository tools (like reading files, glob matching, and pattern searching with `grep`) to autonomously gather surrounding context about your codebase before finalizing feedback.
- **Visual Status Indicators**: Automatically adds an "eyes" reaction to Pull Requests while the review is in progress, providing immediate feedback.
- **CI/CD Ready**: Supports outputting reviews directly to files or as structured JSON, making it easy to integrate with GitHub Actions or other CI pipelines.

## Requirements

- Go 1.24.4+
- Bazel 8.6.0 (if building with `bzlmod`)
- A valid API key for your chosen LLM provider (Google Gemini or Anthropic Claude).

## Installation

Build the binary using standard Go commands:
```bash
go build -o ai-review-agent ./cmd/ai_reviewer
```

*(Alternatively, you can build using Bazel: `bazel run //cmd/ai_reviewer:ai_reviewer`)*

## Usage

### Review Changes
To review changes between a base and a head commit/branch:

```bash
./ai-review-agent \
  --base main \
  --head feature-branch \
  --provider google \
  --model gemini-3.1-pro-preview \
  --provider-api-key "YOUR_API_KEY"
```

## CLI Options

| Flag | Description | Default | Required |
|---|---|---|---|
| `--cwd` | Working directory | | No |
| `--base` | Base commit/branch for diff | `main` | No |
| `--head` | Head commit/branch for diff | `HEAD` | No |
| `--provider` | LLM provider to use (`google`, `anthropic`) | | **Yes** |
| `--model` | LLM provider's specific model ID | | **Yes** |
| `--provider-api-key` | API key for the selected provider | | **Yes** |
| `--main-guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` | No |
| `--review-output-file` | Path to a file where the final review will be written | | No |
| `--output-json` | Path to a file where the structured JSON review will be written | | No |
| `--extraction-model` | Optional model override for the structured JSON extraction pass | | No |
| `--max-tokens` | Max tokens for the LLM response | `8192` | No |

## GitHub Action Inputs

| Input | Description | Default | Required |
|---|---|---|---|
| `provider` | LLM provider to use (`google`, `anthropic`) | `google` | **Yes** |
| `model_id` | LLM provider's specific model ID | `gemini-3-flash-preview` | **Yes** |
| `provider_api_key` | API key for the selected provider | | **Yes** |
| `base` | Base commit/branch for diff | `main` | No |
| `head` | Head commit/branch for diff | `HEAD` | No |
| `working_directory` | Working directory to review | `.` | No |
| `main_guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` | No |
| `metadata_tag` | Tag to identify Cassandra comments (inner text only, will be wrapped in `<!-- ... -->`) | `cassandra-ai-review-${{ github.workflow }}` | No |
| `reviewer_github_token` | GitHub token for posting comments and reactions | `${{ github.token }}` | No |


### Supported Models

For a full list of available models and their IDs, refer to the official documentation:

- **Google Gemini**: [Gemini API Model Documentation](https://ai.google.dev/gemini-api/docs/models/gemini)
- **Anthropic Claude**: [Anthropic Claude Model Documentation](https://docs.anthropic.com/en/docs/about-claude/models)

## GitHub Actions Integration

Cassandra can be integrated into your GitHub Actions workflow to automatically review Pull Requests.

### Key Benefits
- **Persistent Comments**: Manages a single "persistent comment" on the Pull Request, updating it as new changes are pushed to keep the conversation history clean.
- **Visual Feedback**: Automatically adds an "eyes" reaction to the PR description when the review starts and removes it when finished.
- **Secure Token Handling**: Uses a dedicated token preparation step with masking for secure and robust interactions.

### Usage Example

Add the following step to your workflow (e.g., `.github/workflows/review.yml`):

```yaml
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4
        with:
          fetch-depth: 0 # Important: fetch all history for diffing

      - name: Run Cassandra AI Review
        uses: menny/cassandra@main
        with:
          # You must set up the following 3 arguments
          provider: 'google'
          model_id: 'gemini-3-flash-preview'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          # The base branch to compare against (defaults to main)
          base: ${{ github.event.pull_request.base.sha }}
          # The head branch/commit (defaults to HEAD)
          head: ${{ github.event.pull_request.head.sha }}
          # The GitHub token to use for review comments (defaults to GITHUB_TOKEN)
          reviewer_github_token: ${{ secrets.REVIEWER_GITHUB_TOKEN }}
```

If you are using `GITHUB_TOKEN`, you should also ensure the correct permissions:
```yaml
permissions:
  contents: read
  pull-requests: write
```
## Architecture

The project features a lean, custom native Go ReAct loop. Provider-specific interactions are handled via native SDKs (not `langchaingo`). Tools for codebase context gathering are injected securely through model-native Function Calling capabilities.

