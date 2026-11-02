# Cassandra Evaluation Results

This document tracks the performance of various Cassandra configurations against our evaluation suite. It provides transparency into the agent's reasoning capabilities, tool usage, and adherence to security standards.

## Baseline Evaluation

This configuration uses the standard Cassandra settings without any supplemental guidelines or MCP servers. It represents the "out-of-the-box" experience.

<!-- EVAL_RESULTS_START:baseline -->
**Config**: `cassandra.toml`
**Commit**: [`a550c62`](https://github.com/menny/cassandra/commit/a550c62f03d7b48c8cb692d9ee59d63030816221)

| Eval ID | Eval Name | Judge Criteria | Min | Max | Mean |
| --- | --- | --- | --- | --- | --- |
| `simple-bug` | Simple Bug Fix | The agent should identify that the `Divide` function does not check for division by zero. | 5 | 5 | 5.00 |
| `interface-contract` | Interface Contract Violation | The agent MUST identify that `CreateAndSave` in `service.go` passes a potentially nil user to `SaveUser`, which explicitly requires a non-nil pointer in `repository.go`. | 5 | 5 | 5.00 |
| `security-path-traversal` | Security: Path Traversal | The agent MUST identify the path traversal vulnerability in the `/file` handler and recommend using `filepath.Clean` or a boundary check. | 5 | 5 | 5.00 |
| `local-agents-convention` | Local Agents Convention | The agent MUST identify that the new code uses raw `db.Query` instead of the mandated `SafeQuery` wrapper defined in `internal/db/AGENTS.md`. | 5 | 5 | 5.00 |
| `unupdated-caller-blast-radius` | Unupdated Caller Blast Radius | The agent MUST search the repository for callers of `ParseTimeout`, identify that untouched callers (`pkg/client/http.go` and/or `cmd/worker/worker.go`) still multiply the return value by `time.Second` after `ParseTimeout` was changed to return milliseconds (causing timeouts to be 1000x too long), and reject the change. | 5 | 5 | 5.00 |
| `clean-pr-false-positive-restraint` | Clean PR False-Positive Restraint | The agent MUST approve the change (`Approve`) and MUST NOT raise false-positive blocking issues (such as falsely claiming `onEvict` is called unsafely outside the mutex lock, which is explicitly mandated by `internal/cache/REVIEWERS.md` to prevent deadlocks). | 5 | 5 | 5.00 |
| `goroutine-leak-unbuffered-channel` | Concurrency: Goroutine Leak on Context Cancellation | The agent MUST identify the goroutine leak in `Service.Get`: when `ctx.Done()` fires before `FetchRecord` completes, `Get` returns early and the spawned goroutine blocks indefinitely trying to send to the unbuffered `resCh` channel. The agent should recommend using a buffered channel (`make(chan fetchResult, 1)`) or propagating `ctx`. | 5 | 5 | 5.00 |
| **OVERALL** | | | **5.00** | **5.00** | **5.00** |

<!-- EVAL_RESULTS_END:baseline -->

## MCP Invoke Evaluation

This suite evaluates the agent's ability to discover, understand, and correctly invoke tools provided via the Model Context Protocol (MCP).

<!-- EVAL_RESULTS_START:baseline_mcp_invoking -->
**Config**: `cassandra.toml`
**Commit**: [`a550c62`](https://github.com/menny/cassandra/commit/a550c62f03d7b48c8cb692d9ee59d63030816221)

| Eval ID | Eval Name | Judge Criteria | Min | Max | Mean |
| --- | --- | --- | --- | --- | --- |
| `library-godoc-verification` | Library API Verification | The agent MUST identify that `ExecuteAsync` is not a valid method on the `db.DB` type by inspecting the `lib/db/db.go` file or using godoc. | 5 | 5 | 5.00 |
| `external-url-verification` | External URL Verification | The agent MUST fetch the URL https://ai.google.dev/api/generate-content#v1beta.models.generateContent and identify any discrepancies or confirm the API usage based on the documentation. | 2 | 3 | 2.20 |
| **OVERALL** | | | **3.50** | **4.00** | **3.60** |

<!-- EVAL_RESULTS_END:baseline_mcp_invoking -->

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
