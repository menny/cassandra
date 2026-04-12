# Cassandra - AI Review Agent

> *The truth about your code. Seeing bugs before the fall of production. Ignore at your own peril.*

An autonomous code review tool built in Go. This tool provides structured, actionable code reviews using a `Do / Try / Consider` feedback framework.

## Features

- **Local Git Diff Review**: Review your local uncommitted changes against a base branch before pushing.
- **Provider Agnostic**: Natively supports Anthropic and Google models through a unified abstraction.
- **Agentic Context Gathering**: The LLM agent operates in a ReAct loop and has access to repository tools (like reading files, glob matching, and pattern searching with `grep`) to autonomously gather surrounding context about your codebase before finalizing feedback.
- **Visual Status Indicators**: Automatically adds an "eyes" reaction to Pull Requests while the review is in progress, providing immediate feedback.
- **Inline PR Reviews**: Supports formal GitHub PR Reviews with line-level feedback, including automatic dismissal of stale reviews and deduplication of comments.
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
| `--approval-evaluation-prompt-file` | Path to a file containing custom approval evaluation guidelines | | No |
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
| `approval_evaluation_prompt_file` | Path to a file containing custom approval evaluation guidelines | | No |
| `metadata_tag` | Tag to identify Cassandra comments (inner text only, will be wrapped in `<!-- ... -->`) | `cassandra-ai-review-${{ github.workflow }}` | No |
| `reaction_icon` | The reaction icon to add to the PR description while the review is in progress (e.g., `eyes`, `rocket`, `heart`) | `eyes` | No |
| `reviewer_github_token` | GitHub token for posting comments and reactions | `${{ github.token }}` | No |
| `use_inline_comments` | Whether to post inline comments to the PR (requires structured JSON output) | `true` | No |
| `submit_review_action` | Whether to allow formal "approve/reject" actions or force neutral "comment" | `false` | No |
| `delete_old_comments` | Whether to delete previous bot-authored inline comments before posting a new review | `true` | No |

## GitHub Action Outputs

After the review completes, the action exposes the following outputs that downstream steps can consume:

| Output | Description |
|---|---|
| `review_file` | Absolute path to the generated markdown review file on the runner. |
| `json_file` | Absolute path to the structured JSON review file (only set when the JSON was successfully written). |
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
        run: |
          echo "Cassandra requested changes: ${{ steps.cassandra.outputs.review_rationale }}"
          exit 1
```


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
        with:
          fetch-depth: 0 # Required: fetch full history for accurate diffing

      - name: Run Cassandra AI Review
        uses: menny/cassandra@v1
        with:
          provider: 'google'
          model_id: 'gemini-2.5-flash'
          provider_api_key: ${{ secrets.GEMINI_API_KEY }}
          # The exact base and head SHAs give the most accurate diff
          base: ${{ github.event.pull_request.base.sha }}
          head: ${{ github.event.pull_request.head.sha }}
```

### REVIEWERS.md — Per-Directory Review Guidelines

Cassandra automatically discovers `REVIEWERS.md` files in your repository and incorporates their contents into the review prompt. This lets teams provide **targeted, directory-specific guidance** to the AI reviewer.

**How it works:**

For each file that changed in the PR, Cassandra walks up the directory tree from that file's location to the repository root, collecting every `REVIEWERS.md` file it finds. All discovered files are injected into the system prompt, scoped by directory.

**Example:**

```
my-repo/
├── REVIEWERS.md              # Root-level guidelines (apply to all files)
├── backend/
│   ├── REVIEWERS.md          # Backend-specific guidelines
│   └── api/
│       └── handlers.go       # If this file changes, both backend/ and root REVIEWERS.md are loaded
└── frontend/
    └── REVIEWERS.md          # Frontend-specific guidelines
```

A `REVIEWERS.md` file can contain anything relevant to that area of the codebase:

```markdown
# Backend Review Guidelines

- All public functions must have godoc comments.
- Database queries must use parameterized statements — never string interpolation.
- Error values must be wrapped with `fmt.Errorf("...: %w", err)`.
- New HTTP endpoints must have a corresponding integration test.
```

> **Tip:** `AGENTS.md` files work the same way and are also discovered automatically, allowing you to provide instructions to AI coding assistants alongside review guidelines.

### Troubleshooting

#### The review is empty or shows "No changes found"

**Cause:** The git history is shallow — GitHub Actions checks out only the last commit by default.

**Fix:** Add `fetch-depth: 0` to your checkout step:
```yaml
- uses: actions/checkout@v4
  with:
    fetch-depth: 0
```
Also make sure `base` and `head` point to the correct commits (use `github.event.pull_request.base.sha` and `github.event.pull_request.head.sha`).

---

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

## Architecture

The project features a lean, custom native Go ReAct loop. Provider-specific interactions are handled via native SDKs (not `langchaingo`). Tools for codebase context gathering are injected securely through model-native Function Calling capabilities.

