# Code Review Guidelines (LLM Reference)

## What makes a good review

The primary question a reviewer asks is: **"Is this code maintainable, easy to work with, and safe?"**

Good code is:
- **Readable** — clear naming, no unnecessary complexity
- **Consistent** — follows conventions of the surrounding module and the project's style guides
- **Tested** — adequate coverage; narrow or missing tests are a valid review concern
- **Well-structured** — logical separation of concerns, no code smells
- **Documented** — non-obvious logic has comments or docstrings. But, well readable code does not need comments.

## Reviewer responsibilities

- Critique the code, not the author.
- Flag correctness issues if you spot them, but correctness is primarily the submitter's responsibility.
- Understand how code changes interact with each other and with existing code to ensure correctness.
- Distinguish clearly between blockers (must fix before merging) and suggestions (optional improvements).
- Acknowledge good work — praise well-executed parts of the diff.

## Submitter responsibilities (check before reviewing)

- Code follows the style guides for the relevant language.
- Code follows conventions of the module it's added to.
- All tests pass (unit + integration).
- PR is a single cohesive change — not a batch of unrelated changes.
- PR description explains both **what** changed and **why**.

## What to flag in a review

| Severity | Flag when |
|---|---|
| **Must fix** | Clear bugs, security issues, correctness errors, breaking API contracts |
| **Must fix** | Violates style guide in a meaningful way |
| **Suggest** | Readability can be improved without significant effort |
| **Suggest** | More idiomatic use of the language or framework |
| **Consider** | Missing test cases for edge cases |
| **Consider** | Refactoring opportunity that would improve long-term maintainability |
| **Skip** | Style issues that a linter or formatter enforces automatically |
| **Skip** | Speculative concerns not visible in the diff |

## General signals to check

- **Data model and access control changes**: flag for extra scrutiny; these typically require review from a domain expert.
- **Shared components**: changes affecting multiple teams should note the blast radius.
- **TODOs**: new TODOs should reference a task or have a clear owner; stale or vague TODOs are worth flagging.
- **Secrets / credentials**: no API keys, tokens, or passwords in source.
- **Permissions / Roles**: follow the principle of least privilege.
- **Copy-pasted config or comments**: verify they are intentional and correct, not accidentally duplicated from a different context.

## Tone

- Focus on the code, not the author.
- Use "we" and "the code" rather than "you".
- Phrase suggestions as questions or options when appropriate: "Could we extract this into a helper?" rather than "This is wrong."
- Keep feedback concise and actionable.
