# Google Engineering Practices: The Standard of Code Review
Source: https://google.github.io/eng-practices/review/reviewer/standard.html

## Core Philosophy
The primary goal of code review is to **improve the overall health of the codebase over time**. Reviewers should favor approving a change once it is in a state where it definitely improves the health of the system, even if it isn't perfect.

## Expected Reviewer Behavior

### Language & Tone
- **Objective and Respectful**: Focus exclusively on the code, never the author.
- **Explain the "Why"**: Don't just state that something is wrong; explain the reasoning, the potential impact, and the preferred alternative.
- **Mentorship over Pedantry**: Use reviews as a teaching tool. If a change is acceptable but could be more idiomatic, suggest it as a learning opportunity rather than a blocking requirement.
- **Language**: Use inclusive "we" (e.g., "Can we simplify this?") rather than "you" (e.g., "You should simplify this").

### Focus
- **Code Health**: Does this make the codebase better?
- **Maintainability**: Will this be easy for others to understand and modify in six months?
- **Correctness**: Does it actually do what it claims to do?

### Tolerance
- **Favor the Author on Preferences**: If a point is a matter of personal preference and not covered by a style guide, the reviewer should defer to the author's choice.
- **Incremental Improvement**: Accept "good enough" changes that move the needle in the right direction. Do not hold up a PR for perfection if it's already an improvement.
- **Style vs. Substance**: Automate style checks. Reviewers should focus on design, logic, and architecture, not indentation or bracing.

## Grading & Rating System
To ensure consistency, categorize feedback into these levels:

| Severity | Label | Action | Description |
|---|---|---|---|
| **Critical** | `[Must-fix]` | **Blocking** | Bugs, security vulnerabilities, breaking API contracts, or clear violations of established style guides. |
| **Major** | `[Should-fix]` | **Blocking*** | Significant design flaws or maintainability issues that will cause debt. *May be downgraded to non-blocking if the author provides a strong justification. |
| **Minor** | `[Nit]` | **Non-blocking** | Small style preferences, minor readability improvements, or optional refactors. The author is encouraged but not required to address these. |
| **Educational** | `[Thought]` | **Non-blocking** | Suggestions for more idiomatic code or alternative approaches meant for learning and discussion. |

## Examples
- **Must-fix**: "This logic will cause a deadlock if the database connection fails. We must add a timeout or a retry mechanism here."
- **Nit**: "The variable name `data` is a bit generic. Consider `userPrefs` for better clarity, but I'll leave it up to you."
- **Thought**: "I noticed you're using a manual loop here. In this version of Go, `slices.DeleteFunc` is the more idiomatic way to handle this. It's worth checking out for future changes!"
