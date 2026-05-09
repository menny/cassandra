# Plan: Multi-Configuration Evaluation Suites

## Objective
Decouple evaluation criteria (rubrics and metadata) from physical fixture data (diffs and base states). By introducing an `evaluations.json` manifest (a "Suite"), we can run different combinations of tests with varying rubrics against different Cassandra configurations, reusing the same physical data.

## Scope & Impact
- **Data Structure**: Moving from a directory-crawling approach (reading `metadata.json` per folder) to a manifest-driven approach.
- **CLI Interface**: Changing `cmd/eval` to accept a suite manifest file instead of a cases directory.
- **Reusability**: Multiple suite files can point to the same `fixture_path` but apply a different `rubric` (e.g., a "minimalist" suite expecting brief comments vs. a "pedantic" suite).

## Proposed Solution

### 1. Data Structures (`core/eval/types.go`)
Introduce a new `TestSuite` struct that contains a list of cases. The `EvalCase` struct will be updated to reflect its role as an entry in the suite.

```go
type TestSuite struct {
    Name        string     `json:"name"`
    Description string     `json:"description"`
    Cases       []EvalCase `json:"cases"`
}

type EvalCase struct {
    ID          string `json:"id"`
    Name        string `json:"name"`
    Description string `json:"description"`
    Rubric      string `json:"rubric"`
    
    // FixturePath points to the directory containing input.diff and base states.
    // It is resolved relative to the directory containing the suite JSON file.
    FixturePath string `json:"fixture_path"` 
    
    // BaseSource remains optional. If omitted, the runner looks in FixturePath.
    BaseSource  string `json:"base_source,omitempty"`
    
    // Loaded at runtime
    Diff string `json:"-"`
}
```

### 2. Refactor Runner Loading (`core/eval/runner.go`)
Update the loading logic to read the suite file rather than walking a directory.

*   Remove the directory crawler `LoadCases`.
*   Create `LoadSuite(suiteFilePath string) (*TestSuite, error)`.
*   The function will parse the JSON, iterate through the `Cases`, and load `input.diff` and `base.tar.gz`/`base` by resolving `FixturePath` relative to the `suiteFilePath`'s directory.

### 3. Update CLI (`cmd/eval/main.go`)
*   Replace `--cases-dir` with `--suite` (defaulting to `core/eval/testdata/evaluations.json`).
*   Update the setup phase to load the suite and pass the parsed cases to the runner.

### 4. Data Migration
*   Create `core/eval/testdata/evaluations.json`.
*   Migrate the contents of the existing `metadata.json` files into this central manifest.
*   Update `FixturePath` for the existing cases to point to their respective directories.
*   Delete the obsolete `metadata.json` files.

## Verification
-   Run `bazel run //cmd/eval` with the newly created default suite to ensure the "Simple Bug Fix" case still executes and passes successfully.
-   Ensure all path resolutions correctly anchor to the location of the `evaluations.json` file, regardless of where the `bazel run` command is invoked from.