# Conventional Comments
Source: https://conventionalcomments.org/

## Format
`<label> [decorations]: <subject>`
`[discussion]`

## Expected Reviewer Behavior

### Language & Tone
- **Clarity of Intent**: Use the labels to remove ambiguity about the purpose of a comment.
- **Explicit Context**: Provide a clear subject and discussion to explain the reasoning and the path to resolution.
- **Respectful & Direct**: Be concise but objective. Use the `thought` and `question` labels for discussion and the `issue` label for clear problems.
- **Acknowledge Excellence**: Use the `praise` label to recognize well-written or clever code.

### Focus
- **Structured Feedback**: Organize the review into clear, labeled points.
- **Priority Identification**: Use decorations to state whether a comment is blocking.

### Tolerance
- **Non-blocking by Default**: Unless a comment is an `issue` or explicitly marked `(blocking)`, it should generally be considered a suggestion for improvement rather than a mandatory requirement.
- **Aesthetic vs. Correctness**: Use the `nit` label for aesthetic points and the `issue` label for correctness errors to clearly distinguish their importance.

## Grading & Rating System

| Label | Typical Severity | Action | Description |
|---|---|---|---|
| **issue** | **High** | **Blocking** | Significant problems that must be fixed. |
| **suggestion** | **Medium** | **Non-blocking*** | Improvements to clarity, performance, or idioms. *Can be blocking with decoration. |
| **nit** | **Low** | **Non-blocking** | Trivial points, style preferences, or small typos. |
| **question** | **Varies** | **Blocking*** | Clarification needed before approval. *Usually blocking if it affects correctness. |
| **todo** | **Medium** | **Non-blocking** | Future work or non-critical improvements. |
| **thought** | **Low** | **Non-blocking** | Observations or alternatives for discussion. |
| **chore** | **Low** | **Non-blocking** | Small, non-functional tasks (docs, typos). |
| **praise** | **N/A** | **Information** | Positive reinforcement. |

## Common Decorations
- `(blocking)`: Must be addressed before merge.
- `(non-blocking)`: Should be addressed, but merge is allowed.
- `(if-time)`: Only fix if it doesn't delay progress.

## Full Examples
- **issue (blocking):** "This function will panic if the database connection is nil. We should add a check here."
- **suggestion (non-blocking):** "Consider using a `switch` here instead of multiple `if` blocks for better readability. What do you think?"
- **nit:** "Extra whitespace at the end of line 42."
- **praise:** "The way you handled the retry logic with exponential backoff is very clean. Great job!"
