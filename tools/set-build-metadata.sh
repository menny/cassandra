#!/usr/bin/env bash
set -eu

BRANCH="${GITHUB_HEAD_REF:-${GITHUB_REF_NAME:-$(git rev-parse --abbrev-ref HEAD 2>/dev/null || echo "unknown")}}"
if [[ "$BRANCH" == "HEAD" ]]; then
  BRANCH="${GITHUB_HEAD_REF:-unknown}"
fi

COMMIT="$(git rev-parse HEAD 2>/dev/null || echo "unknown")"
REPO="$(git config --get remote.origin.url 2>/dev/null || echo "local")"

printf 'STABLE_BRANCH %s\n' "$BRANCH"
printf 'COMMIT_SHA %s\n' "$COMMIT"
printf 'STABLE_REPO %s\n' "$REPO"

case "${CI:-}" in
  true|TRUE|1)
    ROLE="CI"
    ;;
  *)
    ROLE="developer"
    ;;
esac
printf 'ROLE %s\n' "$ROLE"
