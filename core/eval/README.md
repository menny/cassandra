# Cassandra Eval System

The Cassandra Eval system provides a data-driven, high-fidelity environment for evaluating the `ai-reviewer` agent using an **LLM-as-a-Judge** strategy.

## Architecture

The system consists of three main components:

1.  **High-Fidelity Sandbox**: A Git-backed environment that mirrors a real-world repository state.
2.  **LLM-as-a-Judge**: A structured evaluation pass where a "Judge" model scores the "Subject" agent's review.
3.  **Data-Driven Fixtures**: Test cases defined as filesystem fixtures, separating code from evaluation data.

## Sandbox Lifecycle

For each evaluation case, the system:
1.  Creates a temporary directory.
2.  Populates the "Base" state (from a directory or a `.tar.gz` file).
3.  Initializes a Git repository and commits the base state.
4.  Applies the `input.diff` using `git apply`.
5.  Commits the final state.
6.  Points the Agent's tool registry to this directory.

This process ensures that tools like `read_file`, `glob_files`, and `grep_files` see the repository exactly as it would appear in a real Pull Request.

## Creating Evaluation Cases

Cases are stored in `core/eval/testdata/cases/`. Each case is a directory containing:

-   `metadata.json`: Defines the case name, description, and the evaluation **rubric**.
-   `input.diff`: The Git diff that the agent will be asked to review.
-   `base.tar.gz` (Recommended): A tarball of the repository files *before* the diff is applied.
-   `base/` (Alternative): A directory containing the base files.

### Example `metadata.json`

```json
{
  "name": "Security: Hardcoded API Key",
  "description": "Tests if the agent identifies a hardcoded secret in a config file.",
  "rubric": "The agent MUST identify the hardcoded API key in config.go and recommend using environment variables.",
  "base_source": "base.tar.gz"
}
```

## Running Evaluations

Use the `eval` CLI to run batch evaluations:

```bash
bazel run //cmd/eval -- \
  --subject-provider google \
  --subject-model gemini-1.5-pro \
  --subject-api-key $GOOGLE_API_KEY \
  --judge-model claude-3-7-sonnet \
  --output results.json
```

### CLI Options

-   `--subject-*`: Configuration for the model being evaluated.
-   `--judge-*`: Configuration for the model doing the evaluation (defaults to the subject if not provided).
-   `--cases-dir`: Directory containing the fixtures (defaults to `core/eval/testdata/cases`).
-   `--output`: Path to write the detailed results in JSON format.

## Scoring Rubric

The Judge evaluates the agent's review on a scale of 1-5 based on:
-   **Accuracy**: Correct identification of issues without false positives.
-   **Completeness**: Addressing all points in the rubric.
-   **Constructiveness**: Providing helpful and professional feedback.
-   **Depth**: Effective use of tools to understand context.
