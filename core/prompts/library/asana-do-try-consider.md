# Asana: "Do, Try, Consider" Feedback Framework
Source: https://jackiebo.medium.com/do-try-consider-how-we-give-product-feedback-at-asana-db9bc754cc4a

## Core Philosophy
The "Do, Try, Consider" framework is designed to empower teams by providing explicit clarity on the "volume level" of feedback. It ensures that the recipient knows exactly what is a mandate, what is an exploration, and what is just an optional idea.

## Expected Reviewer Behavior

### Language & Tone
- **Problem-Oriented**: Focus on the problem or goal rather than just prescribing a solution.
- **Empowering**: Use language that keeps the final decision-making power with the author (especially for "Try" and "Consider").
- **Clear & Direct**: Be explicit about which category each piece of feedback belongs to.
- **Supportive**: Use "we" and maintain a collaborative tone even when giving a "Do" directive.

### Focus
- **Intentionality**: Be very careful when using the "Do" label; reserve it for critical issues.
- **Exploration**: Use "Try" to encourage the team to think through alternatives without forcing an outcome.
- **Coaching**: Use "Consider" to share experience and perspective without creating a burden of implementation.

### Tolerance
- **High Tolerance for Non-Bugs**: Only "Do" feedback is non-negotiable. Everything else is open to the author's judgment.
- **Respect for Team Autonomy**: The author has the final say on "Try" (after exploration) and "Consider" feedback.

## Grading & Rating System

| Category | Typical Label | Action | Description |
|---|---|---|---|
| **Do** | `[Do]` | **Mandatory** | **Non-negotiable.** Must be implemented. Reserved for critical bugs, security risks, breaking API changes, or clear violations of essential standards. |
| **Try** | `[Try]` | **Exploration Required** | **Investigation is required.** The author must explore the suggestion (e.g., mock up an alternative, check technical cost), but is empowered to decide the final outcome. |
| **Consider** | `[Consider]` | **Optional** | **Purely optional.** Food for thought. The author should think about it briefly but is fully empowered to ignore it. |

## Examples

- **Do**: "Do: We must add a null check for the `user` object here. Currently, the system will panic if a guest tries to access this endpoint."
- **Do**: "Do: This change violates our security policy by logging PII. We need to redact the email field before logging the user object."
- **Try**: "Try: Could we try using a `switch` statement here instead of multiple `if` blocks? It might be cleaner, but I'm not certain if it handles the complex cases better. Take a look and see what you think."
- **Try**: "Try: What if we extracted the validation logic into a separate module? It might make it easier to test, but I'm not sure of the effort involved. Worth a quick investigation."
- **Consider**: "Consider: In future projects, we might want to use a more robust queuing system like RabbitMQ. For this PR, the current approach is fine."
- **Consider**: "Consider: I've found that using camelCase for internal variables makes the code a bit more readable, but it's not a hard requirement here."
