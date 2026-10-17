#!/usr/bin/env bash
# Keeps one open issue per workflow while it fails on main: opened by the
# first failure, commented by the next ones, closed by the next success.
# Called by .github/workflows/main-failure-issue.yml with the run in the env.
set -euo pipefail

: "${REPO:?}" "${WORKFLOW:?}" "${CONCLUSION:?}" "${RUN_URL:?}" "${RUN_ID:?}" "${SHA:?}" "${EVENT:?}" "${OWNER:?}"
label=ci-failure
title="$WORKFLOW is failing on main"

gh label create "$label" --repo "$REPO" --color B60205 \
  --description "A workflow is failing on main" --force >/dev/null
open=$(gh issue list --repo "$REPO" --label "$label" --state open --limit 100 --json number,title \
  | jq -r --arg t "$title" 'map(select(.title == $t)) | .[0].number // empty')

if [ "$CONCLUSION" = success ]; then
  if [ -n "$open" ]; then
    gh issue close "$open" --repo "$REPO" --comment "$WORKFLOW passed on main again: $RUN_URL"
    echo "closed #$open"
  fi
  exit 0
fi

jobs=$(gh run view "$RUN_ID" --repo "$REPO" --json jobs \
  --jq '.jobs[] | select(.conclusion == "failure" or .conclusion == "timed_out") | "- \(.name)"') || jobs=""
report="$WORKFLOW ended with \`$CONCLUSION\` on main (\`$EVENT\`, ${SHA:0:7}): $RUN_URL

Failed jobs:
${jobs:-- (none reported)}"

if [ -n "$open" ]; then
  gh issue comment "$open" --repo "$REPO" --body "$report"
  echo "commented on #$open"
else
  gh issue create --repo "$REPO" --label "$label" --title "$title" --body "$report

This issue closes itself when $WORKFLOW next passes on main.

cc @$OWNER"
fi
