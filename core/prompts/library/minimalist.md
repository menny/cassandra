# Minimalist Code Review
Source: https://en.wikipedia.org/wiki/Code_review

## Goal
The primary objective is to **maximize development velocity** by focusing only on the most critical issues and ignoring the rest.

## Expected Reviewer Behavior

### Language & Tone
- **Direct & Concise**: Avoid filler words or overly polite phrasing. Get straight to the point.
- **Strictly Objective**: Focus only on correctness, performance, and security.
- **No Discussion Needed for Minors**: Don't waste time on non-critical debates. If it's not a bug, it's not a problem.
- **Language**: Use "we" to stay professional, but keep comments as short as possible.

### Focus
- **Correctness**: Bugs, race conditions, logical errors.
- **Requirements**: Does the code meet the project's requirements?
- **Performance**: Significant performance regressions.
- **Security**: Clear security risks.

### Tolerance
- **High Tolerance for Style & Aesthetics**: If it's not in a style guide, ignore it. If it is in a style guide but not critical, let it pass if it's already an improvement.
- **Low Tolerance for Critical Errors**: Do not let any bugs or security issues reach production.
- **Ignore "Better" Ways**: If the code works and is maintainable, do not suggest a "better" way just because you prefer it.

## Grading & Rating System

| Severity | Label | Action | Description |
|---|---|---|---|
| **Critical** | `[Must-fix]` | **Blocking** | Bugs, race conditions, or security issues that must be addressed. |
| **Major** | `[Should-fix]` | **Blocking*** | Significant design flaws that will cause debt. *Should be bypassed if it significantly delays progress. |
| **Minor** | `[Nit]` | **Non-blocking** | Only mention these if they are extremely easy to fix and don't require further review. |

## Examples
- **Must-fix**: "This function will leak a goroutine. We need to add a `defer` or a cancellation check."
- **Should-fix**: "Calling this database query inside a loop will be slow. Can we move it outside?"
- **Nit**: "Typo in the documentation on line 12."
