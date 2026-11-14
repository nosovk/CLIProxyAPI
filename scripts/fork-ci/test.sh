#!/usr/bin/env bash
set -euo pipefail

# Never retry compiler, setup, race-detector or process failures. Only named
# packages with test failures get ONE uncached retry; preserve both logs.
log=${TEST_LOG:-test.log}
if go test ./... 2>&1 | tee "$log"; then
  exit 0
fi
if grep -Eq '\[build failed\]|\[setup failed\]|^# |WARNING: DATA RACE|^fatal error:' "$log"; then
  echo '::error::Non-retryable test infrastructure/build/race failure'
  exit 1
fi
mapfile -t failed < <(awk '/^FAIL[[:space:]]+[^[:space:]]+/ { print $2 }' "$log" | sort -u)
if [[ ${#failed[@]} == 0 ]]; then
  echo '::error::go test failed without a package list'
  exit 1
fi
echo "::warning::Retrying failed packages once: ${failed[*]}"
go test -count=1 "${failed[@]}" 2>&1 | tee "${log%.log}-retry.log"
