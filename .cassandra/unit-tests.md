# Unit Test Quality

Apply this to test files added or changed in the diff. Before flagging a test, use `read_file` on the code under test and `grep_files` for existing tests of the same behavior. Do not claim redundancy without checking.

For each new or changed test ask: **what real bug would this catch that no other test, type check, or compiler would?** If there is no answer, recommend deleting or merging it. Fewer, sharper tests are a good outcome.

## Flag these (suggestions)
- **Mirror tests**: expected values computed with the same logic as the code, or assertions that only restate what the code just did (e.g., "mock was called with the args we passed").
- **No real assertion**: only `!= nil` / "does not throw", or an assertion that cannot fail.
- **Redundant cases**: near-duplicate tests that should be one table-driven test; many tests covering the same branch.
- **Testing the language or framework**: trivial getters/setters, constants, generated code.
- **Coupled to internals**: asserting private state, call order, or exact log/error strings so that a safe refactor breaks the test.

## Scrutinize these (ask for justification)
- **Remote or external use-cases**: tests that hit the network, a real database, or another service. They belong in an integration suite, labelled as such.
- **Heavy mocking**: more than ~3 mocks, or mocks of the project's own types. Prefer a fake, a real object, or extracting a pure function. Mocks are fine at true boundaries (HTTP, filesystem, clock, process).

## Flag these (must fix)
- **Non-deterministic flows**: `sleep`, wall-clock time, unseeded randomness, unsynchronized threads/goroutines, reliance on map/iteration order, shared global state or test-order dependence. Suggest an injected clock, a seeded generator, or explicit synchronization.

## Do NOT flag
- A regression test for a bug fixed in the same PR.
- Table-driven cases that each add a distinct branch or edge case.
- Clearly labelled contract/integration tests in the correct suite.

## Output
Group findings under one **"Test quality"** heading, at most 5, worst first. Each entry names the test, the smell, and a concrete action (delete, merge, replace mock with fake, inject clock).
