#!/bin/bash
set -e

# Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>
# Example: ./scripts/run_evals.sh baseline cassandra.toml $GOOGLE_API_KEY google gemini-1.5-pro $GOOGLE_API_KEY

ID="$1"
if [ -z "$ID" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

CONFIG_PATH=${1}
if [ -z "$CONFIG_PATH" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

SUBJECT_API_KEY=${1}
if [ -z "$SUBJECT_API_KEY" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

JUDGE_PROVIDER=${1}
if [ -z "$JUDGE_PROVIDER" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

JUDGE_MODEL=${1}
if [ -z "$JUDGE_MODEL" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

JUDGE_API_KEY=${1}
if [ -z "$JUDGE_API_KEY" ]; then
  echo "Usage: ./scripts/run_evals.sh <ID> <config_path> <subject-api-key> <judge-provider> <judge-model> <judge-api-key>"
  exit 1
fi
shift

RESULTS_DIR="core/eval/results"
CONFIG_NAME=$(basename "$CONFIG_PATH" .toml)
TIMESTAMP=$(date +%Y%m%d_%H%M%S)
RESULTS_FILE="$RESULTS_DIR/${CONFIG_NAME}_${TIMESTAMP}.json"

mkdir -p "$RESULTS_DIR"

echo "===> Running evaluations for $CONFIG_PATH (ID: $ID)..."
bazel run //cmd/eval -- \
  --subject-config "$CONFIG_PATH" \
  --subject-api-key "$SUBJECT_API_KEY" \
  --judge-provider "$JUDGE_PROVIDER" \
  --judge-model "$JUDGE_MODEL" \
  --judge-api-key "$JUDGE_API_KEY" \
  --output "$RESULTS_FILE"

echo "===> Updating EVALUATIONS.md..."
bazel run //cmd/update_eval_docs -- \
  --results "$RESULTS_FILE" \
  --config "$CONFIG_PATH" \
  --id "$ID"

echo "===> Done! Results written to $RESULTS_FILE and EVALUATIONS.md updated."
