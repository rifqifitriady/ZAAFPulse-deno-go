#!/usr/bin/env bash
# Summarises a `deno test` log (run with NO_COLOR=1) as Markdown: the passed, failed and ignored counts and the failed
# tests by name and place. CI appends it to the job summary and uploads it (P0 T1).
#   scripts/migration/deno-test-summary.sh <log file> <title> > summary.md
set -euo pipefail
log="$1"
title="$2"
summary=$(grep -E '^(ok|FAILED) \| ' "$log" | tail -n 1 || true)
count() { echo "$summary" | grep -oE "[0-9]+ $1" | grep -oE '[0-9]+' || echo 0; }
passed=$(count passed)
failed=$(count failed)
ignored=$(count ignored)
echo "## $title"
echo
if [ -z "$summary" ]; then
  echo "No summary line: the run did not finish (see the log artifact)."
  exit 0
fi
echo "| passed | failed | ignored |"
echo "|---|---|---|"
echo "| $passed | $failed | $ignored |"
if [ "$failed" != "0" ]; then
  echo
  echo "Failed tests:"
  echo
  lines=$(sed -n '/^ FAILURES $/,$p' "$log" | grep ' => ' | sed 's/^/- /' || true)
  if [ -n "$lines" ]; then echo "$lines"; else echo "(no per-test lines; see the log)"; fi
fi
