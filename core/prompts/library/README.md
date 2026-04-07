# Cassandra Reviewer Prompts Library

This directory contains a collection of pre-defined code review guidelines. These prompts define the "personality," focus, and grading system used by Cassandra during the review process.

## Library Overview

| Prompt | Focus | Source / Inspiration |
|---|---|---|
| `general` | **Robust Maintainer**: Prioritizes long-term sustainability, system reliability, and code transparency. | General consensus |
| `asana-do-try-consider` | **Team Empowerment**: Uses the "Do, Try, Consider" framework to clarify the "volume" and expectation of feedback. | [Asana Engineering](https://jackiebo.medium.com/do-try-consider-how-we-give-product-feedback-at-asana-db9bc754cc4a) |
| `conventional-comments` | **Structured Clarity**: Uses standard labels (praise, nit, suggestion, etc.) to remove ambiguity in communication. | [Conventional Comments](https://conventionalcomments.org/) |
| `google` | **Code Health**: Focuses on improving the overall health of the codebase with each change, favoring "good enough" over perfection. | [Google Engineering Practices](https://google.github.io/eng-practices/review/reviewer/standard.html) |
| `minimalist` | **Velocity**: Focuses strictly on critical correctness, performance, and security issues to maximize development speed. | [Minimalist Review Patterns](https://en.wikipedia.org/wiki/Code_review) |
| `palantir` | **Quality & Mentorship**: Emphasizes high-quality design, rigorous testing, and using reviews for knowledge sharing. | [Palantir Engineering](https://blog.palantir.com/code-review-best-practices-19e0b1245038) |
| `security-first` | **Vulnerability Mitigation**: Prioritizes identifying security risks and ensuring robust input validation and data protection. | [OWASP Code Review Guide](https://owasp.org/www-project-code-review-guide/) |

---

## Creating Custom Guidelines

If the built-in library doesn't match your team's workflow, you can provide your own guidelines via a Markdown file in your repository. To ensure Cassandra remains consistent and high-signal, follow this structure:

### 1. Core Philosophy
Define the high-level goal of the review. Is it about mentorship? Security? Velocity? This helps the AI align its overall tone.

### 2. Expected Reviewer Behavior
Describe how the AI should interact with the author:
- **Language & Tone**: Should it be inquisitive (using "we") or direct?
- **Professional Conduct**: How should it explain its reasoning?

### 3. Focus
List the specific technical areas the AI should prioritize (e.g., Error Handling, Performance, Documentation, Test Coverage).

### 4. Tolerance
Be explicit about what the AI should **ignore**. For example:
- "High tolerance for minor style inconsistencies."
- "Zero tolerance for missing error handling."
- "Ignore formatting issues (handled by linter)."

### 5. Grading & Rating System
Provide a table or list defining the labels and their "blocking" status. This is critical for the AI to decide whether to `✅ Approve` or `❌ Reject` a PR.
- **Blocking**: The PR cannot move forward without a fix.
- **Non-blocking**: Suggestions or observations for the author's consideration.

### 6. Examples
Provide 3-5 concrete examples of feedback items using your defined labels. This "few-shot" prompting is the most effective way to ensure the AI's output matches your expectations.

#### Example Template:
```markdown
# [Team Name] Review Guidelines

## Core Philosophy
[Goal]

## Expected Reviewer Behavior
[Tone/Language]

## Focus
- [Priority 1]
- [Priority 2]

## Tolerance
- [What to ignore]

## Grading & Rating System
| Label | Action | Description |
|---|---|---|
| [Label Name] | [Blocking/Non-blocking] | [Definition] |

## Examples
- [Label] [path/to/file:line] [Feedback text]
```
