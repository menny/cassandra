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

RESULTS_DIR="core/eval/results"
TIMESTAMP=$(date -u +%Y%m%d_%H%M%SZ)

mkdir -p "$RESULTS_DIR"

RESULTS_FILES=()

for ((i=1; i<=REPEATS; i++)); do
  # Filename format: <ID>_<TIMESTAMP>_run<i].json
  RESULTS_FILE="$RESULTS_DIR/${ID}_${TIMESTAMP}_run${i}.json"
  echo "===> [Run $i/$REPEATS] Running evaluations for $CONFIG_PATH (ID: $ID)..."
  bazel run //cmd/eval -- \
    --subject-config "$CONFIG_PATH" \
    --subject-api-key "$SUBJECT_API_KEY" \
    --judge-provider "$JUDGE_PROVIDER" \
    --judge-model "$JUDGE_MODEL" \
    --judge-api-key "$JUDGE_API_KEY" \
    --output "$RESULTS_FILE"
  RESULTS_FILES+=("--results=$RESULTS_FILE")
done

echo "===> Updating EVALUATIONS.md with ${#RESULTS_FILES[@]} results..."
bazel run //cmd/update_eval_docs -- \
  "${RESULTS_FILES[@]}" \
  --config "$CONFIG_PATH" \
  --id "$ID"

echo "===> Done! EVALUATIONS.md updated."
