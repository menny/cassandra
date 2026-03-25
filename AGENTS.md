# Cassandra: AI Agent Guidelines

## Repository Technical Details

- **Language**: Go (specifically targeting `1.24.4+` to satisfy `langchaingo` requirements).
- **Build System**: **Bazel 8.6.0** with **BzlMod**. 
  - Standard `go.mod` and `go.sum` files are maintained, and Bazel resolves Go dependencies via Gazelle's `go_deps` extension in `MODULE.bazel`. 
  - *Note:* Do not upgrade to Bazel 9 without verifying `rules_go` compatibility, as recent Bazel releases removed Xcode configuration targets required by the legacy CGo pipeline on macOS.
- **LLM Abstraction**: We utilize [`langchaingo`](https://github.com/tmc/langchaingo) for LLM interactions.

## Formatting & Linting

This project uses `aspect_rules_lint` (v1.0.8) to integrate standard Go tooling directly into the Bazel graph:
- **Formatter (`gofumpt`)**: You MUST ensure any code generated is properly formatted. Run `bazel run //:format` from the root of the workspace to auto-format all go files before committing. `format_test` targets are also present in `BUILD.bazel` files.
- **Linter (`golangci-lint`)**: A `.golangci.yml` is maintained at the root. You MUST rely exclusively on `bazel run` or `bazel test //...` to invoke linting and formatting natively; do not assume the presence of external CLI wrappers like `aspect`.

## Architecture Reference

Before introducing major changes or restructuring the review loop, please read the **[DESIGN.md](file:///Users/mennyevendanan/dev/menny/cassandra/DESIGN.md)** document. It covers:
- The decision to use a native Go ReAct loop rather than complex Python graphs.
- The rationale behind the custom Tool Registry.
- The `Do / Try / Consider` feedback format.

## Git Commit Guidelines

When committing changes on behalf of the user, strictly follow these commit message rules based on Conventional Commits:

1. **Type**: Prefix the commit with the type of change:
   - `feat:` for new features/tools.
   - `fix:` for bug fixes.
   - `docs:` for documentation updates.
   - `refactor:` for code changes that neither fix a bug nor add a feature.
   - `chore:` for build process or auxiliary tool changes.
2. **Subject Line**:
   - Keep it concise (under 50 characters).
   - Use the imperative mood (e.g., "Add tool" not "Added tool").
   - Do not capitalize the first letter.
   - Do not end with a period.
3. **Body** (if applicable):
   - Leave a blank line between the subject and the body.
   - Wrap lines at 72 characters.
   - Explain *what* was changed and *why*, rather than *how* (the diff already shows how).

**Example Commit:**
```text
feat: add local diff parsing tool

Introduce a new context tool via the registry to parse uncommitted git diffs.
This allows the ReAct loop to analyze isolated lines of code changes before formulating the final review.
```
