#!/bin/bash
set -e

# Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]
# Example: ./scripts/run_evals.sh baseline cassandra.toml $GOOGLE_API_KEY google gemini-1.5-pro $GOOGLE_API_KEY 3

ID="$1"
if [ -z "$ID" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

CONFIG_PATH=${1}
if [ -z "$CONFIG_PATH" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

SUBJECT_API_KEY=${1}
if [ -z "$SUBJECT_API_KEY" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

JUDGE_PROVIDER=${1}
if [ -z "$JUDGE_PROVIDER" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

JUDGE_MODEL=${1}
if [ -z "$JUDGE_MODEL" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

JUDGE_API_KEY=${1}
if [ -z "$JUDGE_API_KEY" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key> [repeats]"
  exit 1
fi
shift

REPEATS=${1:-1}

# Capture current commit SHA for reporting
CURRENT_SHA=$(git rev-parse HEAD 2>/dev/null || echo "")

RESULTS_DIR="core/eval/results"
TIMESTAMP=$(date -u +%Y%m%d_%H%M%SZ)

# Clear old results to ensure fresh collection
rm -rf "$RESULTS_DIR"
mkdir -p "$RESULTS_DIR"

# run_suite <suite_id> <suite_path>
run_suite() {
  local suite_id="$1"
  local suite_path="$2"
  local current_results_file
  local results_files_for_suite=()

  for ((i=1; i<=REPEATS; i++)); do
    # Filename format: <suite_id>_<TIMESTAMP>_run<i>.json
    current_results_file="$RESULTS_DIR/${suite_id}_${TIMESTAMP}_run${i}.json"
    echo "===> [Run $i/$REPEATS] Running evaluations for $suite_id using $suite_path..."

    bazel run //cmd/eval -- \
      --suite "$suite_path" \
      --subject-config "$CONFIG_PATH" \
      --subject-api-key "$SUBJECT_API_KEY" \
      --judge-provider "$JUDGE_PROVIDER" \
      --judge-model "$JUDGE_MODEL" \
      --judge-api-key "$JUDGE_API_KEY" \
      --output "$current_results_file"
    
    results_files_for_suite+=("--results=$current_results_file")
  done

  echo "===> Updating EVALUATIONS.md with ${#results_files_for_suite[@]} results for $suite_id..."
  bazel run //cmd/update_eval_docs -- \
    --config "$CONFIG_PATH" \
    --id "$suite_id" \
    --sha "$CURRENT_SHA" \
    --suite "$suite_path" \
    "${results_files_for_suite[@]}"
}

# Run Core Suite
run_suite "$ID" "core/eval/testdata/evaluations.json"

# Run MCP Suite
run_suite "${ID}_mcp_invoking" "core/eval/testdata/mcp_evaluations.json"

echo "===> Done! EVALUATIONS.md updated."
