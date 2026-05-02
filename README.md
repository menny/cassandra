# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*

An autonomous code review tool built in Go. This tool provides structured, actionable code reviews using a configurable and adjustable feedback framework.

## Features

- **Local Git Diff Review**: Review your local uncommitted changes against a base branch before pushing.
- **Provider Agnostic**: Natively supports Anthropic and Google models through a unified abstraction.
- **Agentic Context Gathering**: The LLM agent operates in a ReAct loop and has access to repository tools (like reading files, glob matching, and pattern searching with `grep`) to autonomously gather surrounding context about your codebase before finalizing feedback.
- **Visual Status Indicators**: Automatically adds an "eyes" reaction to Pull Requests while the review is in progress, providing immediate feedback.
- **Inline PR Reviews**: Supports formal GitHub PR Reviews with line-level feedback, including automatic dismissal of stale reviews and deduplication of comments.
- **Model Context Protocol (MCP) Support**: Connect to custom local or remote tools through the Model Context Protocol to extend the reviewer's capabilities.
- **CI/CD Ready**: Supports outputting reviews directly to files, structured JSON, and detailed session metrics (token usage, tool calls, iterations), making it easy to integrate with GitHub Actions or other CI pipelines.

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

The following settings can be provided via CLI flags, environment variables, or a `cassandra.toml` file.

> **Note**: `provider`, `model`, and `provider-api-key` are **mandatory** and must be provided via one of these methods.

| Flag | Description | Default |
|---|---|---|
| `--cwd` | Working directory | |
| `--base` | Base commit/branch for diff | `main` |
| `--head` | Head commit/branch for diff | `HEAD` |
| `--provider` | LLM provider to use (`google`, `anthropic`, `openai`) | |
| `--model` | LLM provider's specific model ID | |
| `--provider-api-key` | API key for the selected provider | |
| `--provider-url` | Optional API endpoint URL override (useful for OpenAI-compatible providers like Ollama) | |
| `--config` | Path to a configuration file (toml) | `cassandra.toml` |
| `--main-guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` |
| `--supplemental-guidelines` | Additive paths or named library prompts for supplemental guidelines (can be used multiple times) | |
| `--approval-evaluation-prompt-file` | Path to a file containing custom approval evaluation guidelines | |
| `--review-output-file` | Path to a file where the final review will be written | |
| `--output-json` | Path to a file where the structured JSON review will be written | |
| `--metrics-json` | Path to a file where the session metrics (tokens, tool calls, iterations) will be written | |
| `--mcp-config` | Path to an `mcp.json` file configuring custom tools for the reviewer | |
| `--extraction-model` | Optional model override for the structured JSON extraction pass | |
| `--max-tokens` | Max tokens for the LLM response | `8192` |

## GitHub Action Inputs

| Input | Description | Default | Required |
|---|---|---|---|
| `provider` | LLM provider to use (`google`, `anthropic`, `openai`) | | No |
| `model_id` | LLM provider's specific model ID | | No |
| `provider_api_key` | API key for the selected provider | | **Yes** |
| `provider_url` | Optional API endpoint URL override (useful for OpenAI-compatible providers like Ollama) | | No |
| `config_file` | Path to a configuration file (toml) | `cassandra.toml` | No |
| `base` | Base commit/branch for diff | `main` | No |
| `head` | Head commit/branch for diff | `HEAD` | No |
| `max_tokens` | Max tokens for the LLM response | `8192` | No |
| `working_directory` | Working directory to review | `.` | No |
| `main_guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` | No |
| `supplemental_guidelines` | Additive guidelines to supplement the main guidelines. Multiline string where each line is a path or library prompt name. | | No |
| `approval_evaluation_prompt_file` | Path to a file containing custom approval evaluation guidelines | | No |
| `metadata_tag` | Tag to identify Cassandra comments (inner text only, will be wrapped in `<!-- ... -->`) | `cassandra-ai-review-${{ github.workflow }}` | No |
| `reaction_icon` | The reaction icon to add to the PR description while the review is in progress (e.g., `eyes`, `rocket`, `heart`) | `eyes` | No |
| `reviewer_github_token` | GitHub token for posting comments and reactions | `${{ github.token }}` | No |
| `use_inline_comments` | Whether to post inline comments to the PR (requires structured JSON output) | `true` | No |
| `submit_review_action` | Whether to allow formal "approve/reject" actions or force neutral "comment" | `false` | No |
| `delete_old_comments` | Whether to delete previous bot-authored inline comments before posting a new review | `true` | No |
| `mcp_config` | Path to an `mcp.json` file configuring custom tools for the reviewer | | No |

## GitHub Action Outputs

After the review completes, the action exposes the following outputs that downstream steps can consume:

| Output | Description |
|---|---|
| `review_file` | Absolute path to the generated markdown review file on the runner. |
| `json_file` | Absolute path to the structured JSON review file (only set when the JSON was successfully written). |
| `metrics_file` | Absolute path to the session metrics JSON file (tokens, iterations, tool calls). |
| `approved` | The approval decision: `APPROVE`, `REQUEST_CHANGES`, or `COMMENT`. |
| `review_rationale` | The high-level rationale for the approval decision (may be multi-line). |

**Example — gating a subsequent step on the review outcome:**
```yaml
      - name: Run Cassandra AI Review
        id: cassandra
        uses: menny/cassandra@v1
        with:
          provider: 'google'
          model_id: 'gemini-2.5-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          base: ${{ github.event.pull_request.base.sha }}
          head: ${{ github.event.pull_request.head.sha }}

      - name: Fail if changes requested
        if: steps.cassandra.outputs.approved == 'REQUEST_CHANGES'
        env:
          REVIEW_RATIONALE: ${{ steps.cassandra.outputs.review_rationale }}
        run: |
          echo "Cassandra requested changes: $REVIEW_RATIONALE"
          exit 1
```

### Session Metrics JSON

When using `--metrics-json` or consuming the `metrics_file` action output, the JSON contains detailed usage statistics:

```json
{
  "metrics": {
    "tokens": {
      "input": 1240,       // "Fresh" input tokens
      "output": 850,      // Generated response tokens
      "thinking": 128,    // Model reasoning tokens (e.g. Gemini Thinking, OpenAI o1)
      "cached": 4096,     // Tokens served from cache (Input only)
      "total_input": 5336,
      "total_output": 978
    },
    "iterations": 5,      // Number of agent loop turns
    "tool_calls": {
      "total": 12,
      "by_tool": {
        "read_file": 8,
        "glob_files": 3,
        "grep_search": 1
      }
    }
  }
}
```

## Configuration File (`cassandra.toml`)

Cassandra automatically looks for a `cassandra.toml` file in your repository's root. This allows you to centralize settings and avoid redundant CLI flags or GitHub Action inputs.

```toml
# Example cassandra.toml
provider = "google"
model = "gemini-3.1-pro-preview"
main-guidelines = "security-first"
supplemental-guidelines = [
  ".cassandra/haiku-praise.md"
]
```

### Supported Models

For a full list of available models and their IDs, refer to the official documentation:

- **Google Gemini**: [Gemini API Model Documentation](https://ai.google.dev/gemini-api/docs/models/gemini)
- **Anthropic Claude**: [Anthropic Claude Model Documentation](https://docs.anthropic.com/en/docs/about-claude/models)
- **OpenAI**: [OpenAI Models Documentation](https://platform.openai.com/docs/models)

## GitHub Actions Integration

Cassandra can be integrated into your GitHub Actions workflow to automatically review Pull Requests.

### Key Benefits
- **Persistent Comments**: Manages a single "persistent comment" on the Pull Request, updating it as new changes are pushed to keep the conversation history clean.
- **Visual Feedback**: Automatically adds an "eyes" reaction to the PR description when the review starts and removes it when finished.
- **Secure Token Handling**: Uses a dedicated token preparation step with masking for secure and robust interactions.

### Required Permissions

The workflow job running Cassandra needs the following permissions:

| Permission | Why it's needed |
|---|---|
| `contents: read` | To check out the repository and compute the git diff. |
| `pull-requests: write` | To post reviews, inline comments, and manage PR reactions. |
| `issues: write` | To post general (non-inline) PR comments via the GitHub Issues API. |

> **Approving PRs** (`submit_review_action: 'true'`): `pull-requests: write` covers submitting formal `APPROVE` / `REQUEST_CHANGES` reviews. Note that the default `GITHUB_TOKEN` **cannot approve a PR opened by the same user/actor** — this is a GitHub restriction, not a Cassandra limitation. Use a dedicated bot token via `reviewer_github_token` if self-approval is required.

### Complete Workflow Example

Create `.github/workflows/cassandra-review.yml` in your repository:

```yaml
name: Cassandra AI Review
on:
  pull_request:
    types: [opened, synchronize, reopened]

permissions:
  contents: read
  pull-requests: write
  issues: write

jobs:
  review:
    name: AI Code Review
    runs-on: ubuntu-latest
    steps:
      - name: Checkout Code
        uses: actions/checkout@v4

      - name: Run Cassandra AI Review
        uses: menny/cassandra@v1
        with:
          provider: 'google'
          model_id: 'gemini-2.5-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
```

### Per-Directory Review Guidelines — `REVIEWERS.md` and `AGENTS.md`

Cassandra automatically discovers `REVIEWERS.md` **and** `AGENTS.md` files in your repository and incorporates their contents into the review prompt. Both file types use the same discovery logic, letting teams provide **targeted, directory-specific guidance** to the AI reviewer and to AI coding assistants alike.

**How it works:**

For each file that changed in the PR, Cassandra walks up the directory tree from that file's location to the repository root, collecting every `REVIEWERS.md` and `AGENTS.md` file it finds. All discovered files are injected into the system prompt, scoped by directory.

**Example:**

```
my-repo/
├── REVIEWERS.md              # Root-level review guidelines (apply to all files)
├── AGENTS.md                 # Root-level instructions for AI coding assistants
├── backend/
│   ├── REVIEWERS.md          # Backend-specific review guidelines
│   ├── AGENTS.md             # Backend-specific assistant instructions
│   └── api/
│       └── handlers.go       # If this file changes, both backend/ and root files are loaded
└── frontend/
    └── REVIEWERS.md          # Frontend-specific review guidelines
```

**`REVIEWERS.md`** — guidance aimed at the AI reviewer:

```markdown
# Backend Review Guidelines

- All public functions must have godoc comments.
- Database queries must use parameterized statements — never string interpolation.
- Error values must be wrapped with `fmt.Errorf("...: %w", err)`.
- New HTTP endpoints must have a corresponding integration test.
```

**`AGENTS.md`** — instructions shared with AI coding assistants (e.g. GitHub Copilot, Claude):

```markdown
# Backend Agent Instructions

- Follow the repository style guide in docs/style.md.
- Prefer table-driven tests over individual test functions.
- Do not modify generated files under gen/.
```

Both file types can coexist in the same directory and are loaded independently.

### Custom Tools with Model Context Protocol (MCP)

Cassandra supports the [Model Context Protocol (MCP)](https://modelcontextprotocol.io), allowing you to extend the reviewer's capabilities with custom tools. This is useful for integrating with internal APIs, specialized linters, or documentation search.

#### Configuration (`mcp.json`)

Create an `mcp.json` file to define your MCP servers. Cassandra supports both local `stdio` servers and remote `sse` (HTTP) servers. Environment variables in the configuration are automatically expanded using `os.ExpandEnv`.

```json
{
  "mcpServers": {
    "my-local-tool": {
      "command": "node",
      "args": ["/path/to/server.js"],
      "env": {
        "DEBUG": "true"
      }
    },
    "my-remote-tool": {
      "url": "https://mcp.example.com/sse",
      "headers": {
        "Authorization": "Bearer ${MY_API_KEY}"
      }
    }
  }
}
```

#### Usage in GitHub Actions

Pass the path to your `mcp.json` via the `mcp_config` input. Ensure any environment variables required by your MCP configuration are available in the step's environment.

```yaml
      - name: Run Cassandra AI Review
        uses: menny/cassandra@v1
        with:
          provider: 'google'
          model_id: 'gemini-2.5-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          mcp_config: '.github/mcp.json'
        env:
          MY_API_KEY: ${{ secrets.MY_INTERNAL_TOOL_KEY }}
```

### Troubleshooting

#### Cassandra posts a comment but no inline annotations appear

**Cause:** Inline comments require `use_inline_comments: 'true'` (the default) and a valid structured JSON output. This can fail if:
1. The LLM returned a line number outside the PR diff (a "line hallucination"). Cassandra automatically retries without inline comments in this case, appending all feedback to the main review body.
2. The `pull-requests: write` permission is missing.

---

#### `Error: GITHUB_TOKEN is not permitted to approve`

**Cause:** The default `GITHUB_TOKEN` cannot approve a PR opened by the same user or actor.

**Fix:** Use a dedicated Personal Access Token (PAT) or a GitHub App token:
```yaml
reviewer_github_token: ${{ secrets.REVIEWER_GITHUB_TOKEN }}
```
Alternatively, set `submit_review_action: 'false'` to use comment-only mode.

---

#### The Bazel build step takes a long time

**Cause:** Cassandra builds its Go binaries with Bazel on first run. Subsequent runs use the Bazel disk cache.

**Fix:** The `setup-bazel` step in the action configures a persistent disk cache keyed to `cassandra-ai-review`. This cache is shared across all workflow runs in your repository. The first run for a new runner may still take 1–2 minutes; subsequent runs are significantly faster.

---

#### `Error: No GitHub token available`

**Cause:** The `reviewer_github_token` input was set to a secret that doesn't exist or is empty.

**Fix:** Check that the secret name is correct in your repository settings. The action falls back to `github.token` automatically, but if that is also unavailable the step will fail.

### Choosing the Right GitHub Token

Cassandra can work with any GitHub token passed via the `reviewer_github_token` input. Choosing the right one depends on your needs for cross-repo access and PR approval.

| Feature | `GITHUB_TOKEN` (Default) | Classic PAT (`repo` scope) | GitHub App Token |
|---|---|---|---|
| **Setup Complexity** | None (Automatic) | Low (Create token) | Medium (Create & Install App) |
| **Identity** | `github-actions[bot]` | Your User Account | Your App Name |
| **Self-Approval** | No | **Yes** | **Yes** |
| **Cross-Repo Access** | Limited | **Full** (Everything you see) | Scoped (Where installed) |
| **Security** | Highest (Ephemeral) | Lower (Long-lived/Global) | High (Scoped/Short-lived) |

## Architecture

The project features a lean, custom native Go ReAct loop. Provider-specific interactions are handled via native SDKs (not `langchaingo`). Tools for codebase context gathering are injected securely through model-native Function Calling capabilities.

## Token Efficiency

Cassandra structures every system prompt with static content first and dynamic, per-PR content last. This "stable-prefix" ordering means the large, unchanging portion of the prompt — reviewer instructions, the chosen review guideline, and approval rules — is byte-for-byte identical across all reviews of the same repository. Both Anthropic (via `cache_control` breakpoints) and Google Gemini 2.5 (via implicit prefix caching) can reuse cached intermediate states for this prefix, reducing input token costs by up to 75–90% and cutting the time-to-first-token on repeated reviews of the same repository.

