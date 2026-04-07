# Palantir Code Review Best Practices
Source: https://blog.palantir.com/code-review-best-practices-19e0b1245038

## Goals
- **High-Quality Code**: Maintain the codebase in a healthy, maintainable state.
- **Knowledge Sharing**: Use every review to spread context, design patterns, and mentoring.

## Expected Reviewer Behavior

### Language & Tone
- **"We" not "You"**: Use language that emphasizes collective responsibility for the code (e.g., "Can we rename this?" vs. "You should rename this").
- **Ask, Don't Tell**: Phrase suggestions as questions to encourage collaboration and discussion (e.g., "Could we use a `switch` here?" rather than "Use a `switch`").
- **Contextualize Requests**: Always explain *why* a change is requested, linking to documentation or design patterns if applicable.
- **Praise the Good**: Explicitly acknowledge elegant solutions, good test coverage, or clear documentation.

### Focus
- **Correctness & Edge Cases**: Does the code work as intended and handle failure modes correctly?
- **Clarity & Readability**: Is it easy to follow the logic and understand the intent?
- **Testing**: Are the tests meaningful and cover appropriate scenarios?
- **Consistency**: Does this fit naturally within the existing module and the broader project architecture?

### Tolerance
- **Respect Author Intent**: As long as the solution is sound and maintainable, don't force a different approach just because it's not the one you would have chosen.
- **Balance Speed and Quality**: Aim for timely feedback. Don't let minor concerns block progress if the core logic is solid and tested.

## Grading & Rating System
Reviewers must distinguish clearly between blocking and non-blocking feedback:

| Severity | Label | Action | Description |
|---|---|---|---|
| **Must-Fix** | `[Must-fix]` | **Blocking** | Bugs, missing tests, major design flaws, or security issues. |
| **Should-Fix** | `[Should-fix]` | **Blocking*** | Significant maintainability or consistency concerns. *May be bypassed with discussion and agreement. |
| **Suggestion** | `[Suggestion]` | **Non-blocking** | Improving clarity, choosing more idiomatic patterns, or minor refactors. |
| **Nit** | `[Nit]` | **Non-blocking** | Trivial points, minor style preferences, or small typos. |
| **Praise** | `[Praise]` | **Information** | Highlighting good work or clever solutions. |

## Examples
- **Must-Fix**: "I'm concerned that this error is being swallowed on line 42. If the upstream service fails, we'll lose that context. Can we wrap this error and return it?"
- **Should-Fix**: "This module is starting to handle two very different responsibilities. Should we consider splitting the user authentication from the session management?"
- **Suggestion**: "We could use a `map` here to simplify this lookup. It would make the logic a bit cleaner, but the current approach works too."
- **Praise**: "This test case for the race condition is excellent. Very clear and covers a tricky scenario!"
