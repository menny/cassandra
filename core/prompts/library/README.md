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

---

## Prompt Developer Guide: Writing Cache-Friendly Prompts

Modern LLMs (Gemini 2.5, Anthropic Claude) can reuse cached intermediate states when the beginning of a prompt is **byte-for-byte identical** across calls. Cassandra structures every system prompt into three zones ordered from most- to least-stable, maximising the length of the cacheable prefix.

### The Three-Zone Model

```
┌─────────────────────────────────────────────────────────┐
│  Zone 1 — Static Prefix                                 │
│  (reviewer_prompt.md — never changes at runtime)        │
├─────────────────────────────────────────────────────────┤
│  Zone 2 — Semi-static Middle                            │
│  (main guidelines, approval rules, personal prefs —     │
│   same for every PR on the same deployment config)      │
├─────────────────────────────────────────────────────────┤
│  Zone 3 — Dynamic Suffix                                │
│  (AGENTS.md + REVIEWERS.md — varies per PR)             │
└─────────────────────────────────────────────────────────┘
```

**Zone 1 — Static Prefix**
Content that is compiled into the binary and never changes at runtime: core reviewer instructions, tool-use notes, output format rules (`reviewer_prompt.md`). This must be byte-for-byte identical on every single request.

**Zone 2 — Semi-static Middle**
Content that is fixed for a given deployment configuration but does not change per PR: the chosen review guideline file (e.g. `general.md`), the approval evaluation prompt, and personal preferences. Zones 1+2 together form the full cacheable prefix; they are identical for every PR reviewed in the same repository with the same workflow configuration.

**Zone 3 — Dynamic Suffix**
Content that varies per PR: `AGENTS.md` and `REVIEWERS.md` files discovered by walking the changed-file paths. This goes **last** so that its variability never breaks the cache hit on the stable prefix above.

### Sizing Guidance

| Provider | Minimum cacheable prefix | Notes |
|---|---|---|
| **Gemini 2.5** (implicit) | 1,024 tokens (Flash) / 2,048 tokens (Pro) | Cache is managed automatically; no explicit API calls needed. Verify current thresholds in the [Gemini documentation](https://ai.google.dev/gemini-api/docs/caching). |
| **Anthropic** (`cache_control`) | 1,024 tokens | Insert `cache_control` breakpoints at Zone 1/2 boundaries. |

Zones 1+2 together easily exceed these thresholds for any non-trivial guideline file, making every repeated review of the same repository eligible for a cache hit.

### Byte-for-Byte Consistency Rules

These rules apply to **Zone 1** and **Zone 2** content. A single character difference anywhere in the prefix — including trailing whitespace, a changed newline sequence, or a reordered XML tag — will invalidate the cache.

1. **No runtime values in Zones 1 or 2.** Do not inject PR metadata, file paths, commit SHAs, or any other per-request data before Zone 3 begins.
2. **Consistent delimiters.** Always use the same XML-style section tags (`<code_review_guidelines>`, `<approval_evaluation_guidelines>`, etc.) in the same order.
3. **No trailing whitespace.** Guideline files must not have trailing spaces or mixed line endings.
4. **Deterministic ordering.** Where multiple files contribute to Zone 2 (e.g. multiple personal preference sections), ensure the concatenation order is stable.

### Writing a Cache-Friendly Guideline File (Zone 2 Content)

The existing "Creating Custom Guidelines" structure maps naturally to Zone 2:

- **Sections 1–5** (Core Philosophy, Reviewer Behavior, Focus, Tolerance, Grading System) are purely descriptive and form the stable body of the guideline. Keep them concise and free of placeholders.
- **Section 6 — Examples** (few-shot examples) should always come **last** within the file. Few-shot examples are the single most impactful prompt element for steering model output, and placing them at the end of Zone 2 (closest to Zone 3) means any future additions to the example list only invalidate the minimal suffix of the cached prefix.

### Output Suffix Convention

The user message (the git diff + commit messages + PR metadata) forms the model's dynamic input and is never cached. To reinforce output format without breaking caching, keep any format reminders inside `reviewer_prompt.md` (Zone 1) rather than appending them dynamically to the user message. If a per-request format reminder is truly necessary, make it **identical on every call** (same bytes, same position) so Gemini's implicit caching can still absorb it.
