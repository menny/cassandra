# Cassandra Eval System

The Cassandra Eval system provides a data-driven, high-fidelity environment for evaluating the `ai-reviewer` agent using an **LLM-as-a-Judge** strategy. It is designed to ensure strict parity between production behavior and evaluation scenarios.

## Architecture

The system consists of three main components:

1.  **High-Fidelity Sandbox**: A Git-backed environment that mirrors a real-world repository state using `git apply` to ensure tools see exactly what they would in a real PR.
2.  **LLM-as-a-Judge**: A structured evaluation pass where a "Judge" model (e.g., Claude 3.7 or Gemini 1.5 Pro) scores the "Subject" agent's review against a specific rubric.
3.  **Data-Driven Fixtures**: Test cases defined as filesystem fixtures (metadata, diffs, and base states), keeping evaluation data isolated from code.

## Sandbox Lifecycle

For each evaluation case, the system:
1.  Creates a unique temporary directory.
2.  Populates the "Base" state from a `.tar.gz` archive or a directory.
3.  Initializes a Git repository (`--initial-branch=main`) and commits the base state.
4.  Applies the `input.diff` using `git apply`.
5.  Commits the final state.
6.  Instantiates a `core.Reviewer` (Subject) rooted in this directory.

This ensures that tools like `read_file`, `glob_files`, and `grep_files` operate with absolute parity to a production environment.

## Creating Evaluation Cases

Cases are stored in `core/eval/testdata/cases/`. Each case is a directory containing:

-   **`metadata.json`**: Defines the case name, description, and the evaluation **rubric**.
-   **`input.diff`**: The Git diff that the agent will be asked to review.
-   **`base.tar.gz`** (Recommended): A tarball of the repository files *before* the diff is applied. Using tarballs prevents Bazel from indexing evaluation data.
-   **`base/`** (Alternative): A directory containing the base files, useful for local development.

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

The `eval` CLI is the primary entry point for batch processing. It leverages the unified `core.Reviewer` factory to ensure the Subject uses your production configuration.

```bash
bazel run //cmd/eval -- \
  --subject-config "$PWD/cassandra.toml" \
  --subject-api-key $GOOGLE_API_KEY \
  --judge-model gemini-1.5-pro \
  --output results.json
```

### CLI Options

-   **`--subject-config`**: Path to a Cassandra TOML file. The Subject will inherit all guidelines, tool settings, and model parameters from this file.
-   **`--subject-api-key`**: API key for the Subject (overrides the config if provided).
-   **`--judge-*`**: Configuration for the model performing the evaluation (provider, model, url, api-key). Defaults to the Subject's settings if not specified.
-   **`--cases-dir`**: Directory containing the fixtures (defaults to `core/eval/testdata/cases`).
-   **`--output`**: Path to write the detailed results and metrics in JSON format.

## Scoring Rubric

The Judge evaluates the agent's review on a scale of 1-5 based on:
-   **Accuracy**: Correct identification of issues without false positives.
-   **Completeness**: Addressing all points in the rubric.
-   **Constructiveness**: Providing helpful and professional feedback.
-   **Depth**: Effective use of tools to understand context.

The CLI outputs the Score, Rationale, and a list of specific **Findings** for each case.
