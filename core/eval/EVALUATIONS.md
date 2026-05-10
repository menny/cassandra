# Cassandra Evaluation Results

This document tracks the performance of various Cassandra configurations against our evaluation suite. It provides transparency into the agent's reasoning capabilities, tool usage, and adherence to security standards.

## Baseline Evaluation

This configuration uses the standard Cassandra settings without any supplemental guidelines or MCP servers. It represents the "out-of-the-box" experience.

<!-- EVAL_RESULTS_START:baseline -->
**Config**: `cassandra.toml`  

| Eval ID | Eval Name | Judge Criteria | Score |
| --- | --- | --- | --- |
| `simple-bug` | Simple Bug Fix | The agent should identify that the `Divide` function does not check for division by zero. | 5/5 |
| `interface-contract` | Interface Contract Violation | The agent MUST identify that `CreateAndSave` in `service.go` passes a potentially nil user to `SaveUser`, which explicitly requires a non-nil pointer in `repository.go`. | 5/5 |
| `security-path-traversal` | Security: Path Traversal | The agent MUST identify the path traversal vulnerability in the `/file` handler and recommend using `filepath.Clean` or a boundary check. | 5/5 |
| `local-agents-convention` | Local Agents Convention | The agent MUST identify that the new code uses raw `db.Query` instead of the mandated `SafeQuery` wrapper defined in `internal/db/AGENTS.md`. | 5/5 |
| `library-godoc-verification` | Library API Verification | The agent MUST identify that `ExecuteAsync` is not a valid method on the `db.DB` type by inspecting the `lib/db/db.go` file or using godoc. | 5/5 |

<!-- EVAL_RESULTS_END:baseline -->

## MCP-Enhanced Evaluation

In this run, we enabled the `godoc` MCP server to see if the agent can better verify library signatures and API contracts.

<!-- EVAL_RESULTS_START:mcp-godoc -->
<!-- EVAL_RESULTS_END:mcp-godoc -->

## Evaluation Methodology

The evaluations are performed using an **LLM-as-a-Judge** strategy. Each evaluation case consists of:
1.  **A Base State**: A specific version of a repository.
2.  **An Input Diff**: A set of changes the agent is asked to review.
3.  **A Rubric**: Specific criteria the judge uses to score the review.

### Scoring Rubric
-   **5 (Excellent)**: Correctly identifies all issues, no false positives, professional and constructive.
-   **4 (Good)**: Identifies major issues, might miss minor style points, constructive.
-   **3 (Fair)**: Identifies the core issue but might miss nuances or include minor inaccuracies.
-   **2 (Poor)**: Misses the core issue or provides significantly misleading feedback.
-   **1 (Fail)**: Fails to identify the problem or produces harmful advice.
