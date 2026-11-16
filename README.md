# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*

[![codecov](https://codecov.io/gh/menny/cassandra/graph/badge.svg?branch=main)](https://codecov.io/gh/menny/cassandra)

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
| `--provider` | LLM provider to use (`google`, `anthropic`, `anthropic-bedrock`, `openai`) | |
| `--model` | LLM provider's specific model ID | |
| `--provider-api-key` | API key for the selected provider (not used by `anthropic-bedrock`) | |
| `--provider-url` | Optional API endpoint URL override (useful for OpenAI-compatible providers like Ollama) | |
| `--config` | Path to a configuration file (toml) | `cassandra.toml` |
| `--main-guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` |
| `--supplemental-guidelines` | Additive paths or named library prompts for supplemental guidelines (can be used multiple times) | |
| `--approval-evaluation-prompt-file` | Path to a file containing custom approval evaluation guidelines | |
| `--review-pipeline-file` | Path to a TOML review pipeline defining subagents to run before the main review | |
| `--review-output-file` | Path to a file where the final review will be written | |
| `--output-json` | Path to a file where the structured JSON review will be written | |
| `--metrics-json` | Path to a file where the session metrics (tokens, tool calls, iterations) will be written | |
| `--mcp-config` | Path to an `mcp.json` file configuring custom tools for the reviewer | |
| `--allow-url-fetch` | Enable the mcp-server-fetch tool (requires `uvx` to be installed) | `false` |
| `--ignored-lock-files` | Comma-separated list of lock files to ignore in diffs (overrides default) | (many) |
| `--extraction-model` | Optional model override for the structured JSON extraction pass | |
| `--max-tokens` | Max tokens for the LLM response | `8192` |
| `--max-diff-bytes` | Maximum diff size in bytes before omitting full diff from prompt (0 to disable) | `10240` |

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
| `max_diff_bytes` | Maximum diff size in bytes before omitting full diff from the prompt (0 to disable) | `10240` | No |
| `working_directory` | Working directory to review | `.` | No |
| `main_guidelines` | Path to a file or a named prompt from the library (`general`, `asana-do-try-consider`, `google`, `conventional-comments`, `palantir`, `minimalist`, `security-first`) | `general` | No |
| `supplemental_guidelines` | Additive guidelines to supplement the main guidelines. Multiline string where each line is a path or library prompt name. | | No |
| `approval_evaluation_prompt_file` | Path to a file containing custom approval evaluation guidelines | | No |
| `review_pipeline_file` | Path to a TOML review pipeline; prompt paths are relative to that file | | No |
| `metadata_tag` | Tag to identify Cassandra comments (inner text only, will be wrapped in `<!-- ... -->`) | `cassandra-ai-review-${{ github.workflow }}` | No |
| `reaction_icon` | The reaction icon to add to the PR description while the review is in progress (e.g., `eyes`, `rocket`, `heart`) | `eyes` | No |
| `reviewer_github_token` | GitHub token for posting comments and reactions | `${{ github.token }}` | No |
| `use_inline_comments` | Whether to post inline comments to the PR (requires structured JSON output) | `true` | No |
| `submit_review_action` | Whether to allow formal "approve/reject" actions or force neutral "comment" | `false` | No |
| `delete_old_comments` | Whether to delete previous bot-authored inline comments before posting a new review | `true` | No |
| `mcp_config` | Path to an `mcp.json` file configuring custom tools for the reviewer | | No |
| `allow_url_fetch` | Whether to allow the AI to fetch external URLs using the mcp-server-fetch tool (requires `uvx` to be installed). | `false` | No |
| `ignored_lock_files` | Multiline string where each line is a lock file to ignore (overrides default) | | No |

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

## Review Pipeline

Before the main AI code review, Cassandra can run a **review pipeline**: a flat list of concurrent subagents, each with its own prompt and isolated conversation history. All subagents receive the same review request (diff, commits, and PR metadata) and share the same read/context tool environment, subject to the tool access policy below—not every tool available to the main reviewer. They do not consume one another's results.

### Configuration

Select a pipeline using `--review-pipeline-file`, the `review-pipeline-file` key in `cassandra.toml`, or the `review_pipeline_file` GitHub Action input:

```toml
# cassandra.toml
provider = "google"
model = "gemini-3.1-pro-preview"
main-guidelines = "security-first"
review-pipeline-file = ".cassandra/review-pipeline.toml"
```

```toml
# .cassandra/review-pipeline.toml
max_concurrency = 4

[[subagents]]
title = "PR summary"
description = "Summarize the pull request changes"
prompt_file = "pre-review-summary.md"
required = true
user_visible = true
reviewer_input = true
# MCP tools are disabled by default; opt in independently for this subagent.
# allow_mcps = true

# Optional field-by-field overrides; omitted fields inherit the main LLM settings.
# [subagents.llm]
# model = "YOUR_MODEL_ID"
# provider = "google"
# provider_api_key = "YOUR_API_KEY"
# provider_url = "https://your-provider.example.com"
# max_tokens = 4096
# provider_options = { temperature = 0.2 }
```

- `max_concurrency` limits simultaneous subagents (default: `4`). The pipeline is flat, not a dependency graph.
- Each `[[subagents]]` entry supplies a `title`, `description`, and `prompt_file`. Relative `prompt_file` paths resolve against the **pipeline file's directory**, not the working directory or `cassandra.toml`. Keep the pipeline and its prompts together when moving them.
- `required`, `user_visible`, and `reviewer_input` each default to **`false`**. Set them explicitly as above to retain a mandatory summary shown to both the user and main reviewer.
- `user_visible = true` reports successful output to the user; `reviewer_input = true` supplies successful output to the main reviewer. `user_visible` controls only the successful report, not progress, tool activity, usage, retries, or clarification questions. Visible reports are emitted in **completion order**; reviewer context is assembled in **declaration order**, independent of completion timing. These ordering rules also apply to optional-failure reports.
- **Tool access**: All normal built-in tools are allowed, but privileged T3 tools (including `link_pull_request`, `list_thread_pull_requests`, `orchestrator_capabilities`, and `t3_thread_launch`), Omnigent tools, `update_review_verdict`, and `post_pr_comment` are excluded. MCP tools are disabled by default for every subagent; set `allow_mcps = true` on an individual `[[subagents]]` entry to enable its configured MCP tools. This opt-in does not enable the excluded built-ins.
- A **required subagent failure** cancels the other subagents and aborts the review. An **optional subagent failure** does not block the main review, but is always shown to both the user and reviewer, including the error, **regardless of `user_visible` or `reviewer_input`**.
- The optional `[subagents.llm]` table supports `model`, `provider`, `provider_api_key`, `provider_url`, `max_tokens`, and `provider_options`. Each omitted field inherits the corresponding main LLM setting independently; overriding `model` alone does not discard the other main settings. Do not commit real API keys.

CLI equivalent:

```bash
cassandra --review-pipeline-file .cassandra/review-pipeline.toml
```

GitHub Action equivalent:

```yaml
with:
  review_pipeline_file: '.cassandra/review-pipeline.toml'
```

The Action passes the resolved pipeline file path directly; it does not copy the TOML contents to a temporary file. Relative inputs are checked under `github.workspace/working_directory` first, then under `github.action_path`; absolute paths are passed through. This preserves the pipeline's relative prompt paths with both downloaded binaries and the Bazel wrapper.

See [`examples/review-pipeline.toml`](examples/review-pipeline.toml) and [`examples/cassandra.toml`](examples/cassandra.toml) for a runnable configuration using the existing summary prompt. The example pipeline references `../.cassandra/pre-review-summary.md` because it lives in `examples/`.

### Migrating old configuration

The old `--pre-review-prompt` / `--pre-review-model` flags, `pre-review-prompt` / `pre-review-model` TOML keys, and `pre_review_prompt` / `pre_review_model` Action inputs are no longer supported. Old configuration is rejected, not silently translated. Remove those settings, create a pipeline entry with `prompt_file`, move any model override into `[subagents.llm].model`, and select the pipeline using the new setting. Recalculate the prompt path relative to the pipeline file.

### Supported Models

For a full list of available models and their IDs, refer to the official documentation:

- **Google Gemini**: [Gemini API Model Documentation](https://ai.google.dev/gemini-api/docs/models/gemini)
- **Anthropic Claude**: [Anthropic Claude Model Documentation](https://docs.anthropic.com/en/docs/about-claude/models)
- **OpenAI**: [OpenAI Models Documentation](https://platform.openai.com/docs/models)

### Claude on Amazon Bedrock

`--provider anthropic-bedrock` runs the same Claude provider against Amazon Bedrock's `bedrock-runtime` endpoint.

- `--model` is passed through verbatim, so use a Bedrock model or inference profile ID (for example `us.anthropic.claude-opus-5-5`), not an Anthropic API model name. The same applies to a pipeline's `[subagents.llm].model` override when using `anthropic-bedrock`, and to `--extraction-model`.
- No `--provider-api-key` is needed. Credentials come from the default AWS credential chain, or from `AWS_BEARER_TOKEN_BEDROCK` when set.
- The region comes from `AWS_REGION` / `AWS_DEFAULT_REGION` or your shared AWS config, and startup fails if none is set.
- `--provider-url` is ignored.

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

Create an `mcp.json` file to define your MCP servers. Cassandra supports both local `stdio` servers and remote `sse` (HTTP) servers.

Environment variables in the configuration are automatically expanded using `os.ExpandEnv`. Ensure you declare each required environment variable required by the MCP - only declared environments are passed to the MCP server to prevent leaking secrets.

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

