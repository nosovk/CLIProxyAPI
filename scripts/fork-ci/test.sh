#!/usr/bin/env bash
set -euo pipefail

# Never retry compiler, setup, race-detector or process failures. Only named
# packages with test failures get ONE uncached retry; preserve both logs.
log=${TEST_LOG:-test.log}
suite_ok=false
if go test ./... 2>&1 | tee "$log"; then
  suite_ok=true
else
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
  if go test -count=1 "${failed[@]}" 2>&1 | tee "${log%.log}-retry.log"; then
    suite_ok=true
  fi
fi

if [[ $suite_ok != true ]]; then
  exit 1
fi

# Verify minimal Home-worker fork invariants uncached and assert every named
# test actually ran (so a silently dropped patch or test on rebase cannot pass
# as "[no tests to run]"). Retired standalone credential-pools / nearest-reset
# patches are intentionally not required.
verify_invariant() {
  local pkg=$1
  shift
  local tests=("$@")
  local IFS='|'
  local pattern="^(${tests[*]})$"
  local inv_log
  inv_log=$(mktemp "${TMPDIR:-/tmp}/fork-invariant.XXXXXX.log")

  if ! go test -v -count=1 -run "$pattern" "$pkg" 2>&1 | tee "$inv_log"; then
    rm -f "$inv_log"
    echo "::error::Minimal Home-worker fork invariant failed in $pkg"
    return 1
  fi
  if grep -q '\[no tests to run\]' "$inv_log"; then
    rm -f "$inv_log"
    echo "::error::Missing minimal Home-worker fork invariant test in $pkg ($pattern)"
    return 1
  fi
  local test_name
  for test_name in "${tests[@]}"; do
    if ! grep -Eq "^=== RUN[[:space:]]+${test_name}([[:space:]]|$)" "$inv_log"; then
      rm -f "$inv_log"
      echo "::error::Required minimal Home-worker fork invariant test $test_name did not run in $pkg"
      return 1
    fi
  done
  rm -f "$inv_log"
}

verify_invariant ./internal/logging \
  TestJSONRequestLogging \
  TestJSONStreamingRequestLogging \
  TestJSONRequestLoggingSerializesErrorsAndMasksAddonHeaders \
  TestResponseDiagnosticsInFileAndHomeLogs \
  TestFileRequestLogger_HomeEnabled_BoundsNonStreamingJSONBodies \
  TestFileRequestLogger_HomeEnabled_BoundsStreamingJSONBodies \
  TestFileRequestLogger_HomeEnabled_BoundsStreamingJSONUpstreamSources \
  TestFileRequestLogger_HomeEnabled_ForwardsStreamingAPIErrorResponses \
  TestFileRequestLogger_HomeEnabled_ForwardsStreamingJSONAPIErrorResponses

verify_invariant ./internal/api/middleware \
  TestRequestLoggingMiddlewareKeepsErrorLogFormatAcrossReload \
  TestCaptureRequestInfo_BoundsJSONRequestBody \
  TestRequestLogSourcesKeepFormatSnapshotAcrossReload \
  TestResponseWriterWrapperMarksFirstStageStreamingDrops

verify_invariant ./internal/config \
  TestSaveConfigPreserveComments_PrunesDefaultRequestLogFormat

verify_invariant ./internal/api \
  TestUpdateClientsAppliesRequestLogFormat \
  TestModelsMultiAgentClientConfigAndReload

verify_invariant ./internal/client/codex/models \
  TestApplyCodexClientModelMetadataPreservesMultiAgentVersionWhenDisabled \
  TestCodexClientModelsResponse_RewritesTemplateMultiAgentVersionWhenEnabled

verify_invariant ./sdk/api/handlers/openai \
  TestResponsesWebsocketTimelineUsesBoundedJSONSource \
  TestResponsesLateInterruptDoesNotPoisonNextCreate \
  TestResponsesWebsocketCleanupPreservesPendingDisconnectFrame \
  TestCodexClientModelsResponseMultiAgentV2FollowsConfig
