# Security-First Code Review
Source: https://owasp.org/www-project-code-review-guide/

## Goal
The primary objective is to **identify and mitigate security vulnerabilities** as early as possible.

## Expected Reviewer Behavior

### Language & Tone
- **Direct & Professional**: Be clear about the security risk. Explain the potential impact and the fix.
- **Objective and Evidence-based**: Point out specific lines and explain the vulnerability.
- **Collaborative**: Work with the author to find a secure and maintainable solution.
- **Explain the "Why"**: Don't just say something is a security risk; explain what could happen if it's not fixed.

### Focus
- **Input Validation**: Sanitizing all untrusted inputs.
- **Auth & Auth**: Correct and robust authentication and authorization checks.
- **Data Protection**: Securely storing and transmitting sensitive data.
- **Vulnerabilities**: Identification and mitigation of OWASP Top 10 vulnerabilities.

### Tolerance
- **Zero Tolerance for Security Risks**: Any security vulnerability must be addressed before the code reaches production.
- **Low Tolerance for Insecure Defaults**: Do not allow the introduction of insecure defaults or configurations.
- **Balance with Usability**: If a security fix significantly impacts usability, work with the team to find a better approach.

## Grading & Rating System

| Severity | Label | Action | Description |
|---|---|---|---|
| **Critical** | `[Must-fix]` | **Blocking** | High-impact security vulnerabilities (e.g., SQLi, broken access control). |
| **Major** | `[Should-fix]` | **Blocking*** | Medium-impact vulnerabilities or insecure practices. *Can be addressed in a follow-up if justified. |
| **Minor** | `[Suggestion]` | **Non-blocking** | Improving security posture, clearer logging, or minor insecure patterns. |

## Examples
- **Must-fix**: "This user-provided input is used in a raw SQL query. This is a high-risk SQL injection vulnerability. We must use prepared statements."
- **Should-fix**: "We're logging the entire request body, which might contain sensitive data like API keys. Let's redact these before logging."
- **Suggestion**: "We could use a more secure hashing algorithm for these non-sensitive IDs. What do you think?"
