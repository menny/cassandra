# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*

An autonomous code review tool built in Go. This tool provides structured, actionable code reviews using a `Do / Try / Consider` feedback framework.

## Features

- **Local Git Diff Review**: Review your local uncommitted changes against a base branch before pushing.
- **Provider Agnostic**: Natively supports Anthropic and Google models through a unified abstraction.
- **Agentic Context Gathering**: The LLM agent operates in a ReAct loop and has access to repository tools (like reading files, glob matching, and pattern searching with `grep`) to autonomously gather surrounding context about your codebase before finalizing feedback.
- **CI/CD Ready**: Supports outputting reviews directly to files, making it easy to integrate with GitHub Actions or other CI pipelines.

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
| `--main_guidelines` | Path to a file overriding the built-in main guidelines | | No |
| `--review-output-file` | Path to a file where the final review will be written | | No |
| `--max-tokens` | Max tokens for the LLM response | `8192` | No |

### Supported Models

For a full list of available models and their IDs, refer to the official documentation:

- **Google Gemini**: [Gemini API Model Documentation](https://ai.google.dev/gemini-api/docs/models/gemini)
- **Anthropic Claude**: [Anthropic Claude Model Documentation](https://docs.anthropic.com/en/docs/about-claude/models)

## GitHub Actions Integration

Cassandra can be integrated into your GitHub Actions workflow to automatically review Pull Requests.

### Simple Usage

Add the following step to your workflow (e.g., `.github/workflows/review.yml`):

```yaml
      - name: Run Cassandra AI Review
        uses: menny/cassandra@main
        with:
          provider: 'google'
          model_id: 'gemini-3.1-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          # The base branch to compare against (defaults to main)
          base: ${{ github.event.pull_request.base.ref }}
          # The head branch/commit (defaults to HEAD)
          head: ${{ github.event.pull_request.head.sha }}
          # Optional: capture the review in a file
          review_output_file: 'review.md'
```

### Persistent PR Comment

To keep the PR history clean, we recommend using a "persistent comment" strategy that updates a single comment as new changes are pushed.

```yaml
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4
        with:
          fetch-depth: 0 # Important: fetch all history for diffing

      - name: Run Cassandra Review
        uses: menny/cassandra@main
        with:
          provider: 'google'
          model_id: 'gemini-3.1-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          base: ${{ github.event.pull_request.base.sha }}
          head: ${{ github.event.pull_request.head.sha }}
          review_output_file: 'review.md'

      - name: Post AI Review Comment
        env:
          GITHUB_TOKEN: ${{ secrets.GITHUB_TOKEN }}
        run: |
          TAG="<!-- cassandra-ai-review -->"
          echo -e "\n\n$TAG" >> review.md
          
          # Find existing comment
          COMMENT_ID=$(gh pr view ${{ github.event.pull_request.number }} --json comments --jq ".comments[] | select(.body | contains(\"$TAG\")) | .id" | head -n 1)

          if [ -n "$COMMENT_ID" ]; then
            gh pr comment ${{ github.event.pull_request.number }} --edit "$COMMENT_ID" --body-file review.md
          else
            gh pr comment ${{ github.event.pull_request.number }} --body-file review.md
          fi
```

## Architecture

The project features a lean, custom native Go ReAct loop. Provider-specific interactions are handled via native SDKs (not `langchaingo`). Tools for codebase context gathering are injected securely through model-native Function Calling capabilities.
