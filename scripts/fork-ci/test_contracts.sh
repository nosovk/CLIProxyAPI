#!/usr/bin/env bash
set -euo pipefail

# Offline contracts: real disposable Git repos, fake Go/GitHub/registry commands.
# Run from any checkout: bash scripts/fork-ci/test_contracts.sh
# Nothing here contacts GitHub, a registry, or production.

SCRIPTS_DIR=$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)
ROOT_DIR=$(cd "$SCRIPTS_DIR/../.." && pwd)

assert_contains() {
  local haystack=$1
  local needle=$2
  local msg=${3:-"expected output to contain: $needle"}
  if [[ $haystack != *"$needle"* ]]; then
    echo "ASSERTION FAILED: $msg" >&2
    return 1
  fi
}

assert_not_contains() {
  local haystack=$1
  local needle=$2
  local msg=${3:-"expected output NOT to contain: $needle"}
  if [[ $haystack == *"$needle"* ]]; then
    echo "ASSERTION FAILED: $msg" >&2
    return 1
  fi
}

assert_eq() {
  local want=$1
  local got=$2
  local msg=${3:-"expected '$want', got '$got'"}
  if [[ $want != "$got" ]]; then
    echo "ASSERTION FAILED: $msg" >&2
    return 1
  fi
}

assert_ne() {
  local unexpected=$1
  local got=$2
  local msg=${3:-"did not expect '$unexpected'"}
  if [[ $unexpected == "$got" ]]; then
    echo "ASSERTION FAILED: $msg" >&2
    return 1
  fi
}

setup_fixture() {
  FIXTURE_ROOT=$(mktemp -d "${TMPDIR:-/tmp}/fork-ci-contracts.XXXXXX")
  REPO_DIR="$FIXTURE_ROOT/repo"
  BIN_DIR="$FIXTURE_ROOT/bin"
  OUTPUT_FILE="$FIXTURE_ROOT/output"
  CALLS_FILE="$FIXTURE_ROOT/calls"
  mkdir -p "$REPO_DIR" "$BIN_DIR"
  : > "$OUTPUT_FILE"
  : > "$CALLS_FILE"

  export PATH="$BIN_DIR:$PATH"
  export GIT_CONFIG_GLOBAL=/dev/null
  export GIT_CONFIG_NOSYSTEM=1
  export GIT_AUTHOR_NAME=Test
  export GIT_AUTHOR_EMAIL=test@example.invalid
  export GIT_COMMITTER_NAME=Test
  export GIT_COMMITTER_EMAIL=test@example.invalid
  export GIT_TERMINAL_PROMPT=0
  export GITHUB_OUTPUT="$OUTPUT_FILE"
  export TARGET_BRANCH=feature
  export UPSTREAM_BRANCH=dev
  export GITHUB_EVENT_NAME=push
  export GITHUB_REPOSITORY=test/fork
  export RUN_URL=https://example.invalid/run
  export CALLS="$CALLS_FILE"

  cd "$REPO_DIR"
  git init -q -b feature
  git config commit.gpgsign false
  commit_file "base" "file"
  BASE_SHA=$(git rev-parse HEAD)
  git branch dev
  git init -q --bare "$FIXTURE_ROOT/remote.git"
  git remote add origin "$FIXTURE_ROOT/remote.git"
  git push -q origin feature dev
  export UPSTREAM_REPO="$FIXTURE_ROOT/remote.git"
}

cleanup_fixture() {
  if [[ -n ${FIXTURE_ROOT:-} && -d ${FIXTURE_ROOT:-} ]]; then
    rm -rf "$FIXTURE_ROOT"
  fi
}

commit_file() {
  local text=$1
  local filename=${2:-file}
  printf '%s\n' "$text" > "$REPO_DIR/$filename"
  git add "$filename"
  git commit -q -m "$text"
}

fake_cmd() {
  local name=$1
  local path="$BIN_DIR/$name"
  {
    printf '%s\n' '#!/usr/bin/env bash'
    printf '%s\n' 'set -eu'
    cat
  } > "$path"
  chmod +x "$path"
}

install_go_fake() {
  local first=$1
  local retry_exit=${2:-0}
  export FIRST="$first"
  export RETRY_EXIT="$retry_exit"
  fake_cmd go <<'EOF'
echo "$*" >> "$CALLS"
if [[ $* == "test ./..." ]]; then
  printf "%s\n" "$FIRST"
  exit 1
fi
if [[ ${1:-} == "test" && ${2:-} == "-v" ]]; then
  if [[ ${FAKE_NO_TESTS_TO_RUN:-0} == 1 ]]; then
    echo "testing: warning: no tests to run"
    echo "[no tests to run]"
    exit 0
  fi
  pattern=""
  prev=""
  for arg in "$@"; do
    if [[ $prev == "-run" ]]; then
      pattern=$arg
      break
    fi
    prev=$arg
  done
  pattern=${pattern#\^\(}
  pattern=${pattern%\)\$}
  IFS="|" read -r -a names <<< "$pattern"
  for name in "${names[@]}"; do
    if [[ -n ${FAKE_MISSING_TEST:-} && $name == "$FAKE_MISSING_TEST" ]]; then
      continue
    fi
    echo "=== RUN   $name"
    echo "--- PASS: $name (0.00s)"
  done
  echo "PASS"
  exit 0
fi
exit "$RETRY_EXIT"
EOF
}

run_script() {
  local name=$1
  bash "$SCRIPTS_DIR/$name"
}

test_push_identity_and_base() {
  run_script sync.sh
  local out
  out=$(cat "$OUTPUT_FILE")
  assert_contains "$out" "fork_lease=$BASE_SHA"
  assert_contains "$out" "commit_sha=$BASE_SHA"
  assert_contains "$out" "upstream_base=$BASE_SHA"
  assert_contains "$out" "should_build=true"
}

test_stale_push_is_not_built() {
  commit_file "new remote"
  git push -q origin feature
  git switch -q --detach "$BASE_SHA"
  run_script sync.sh
  assert_contains "$(cat "$OUTPUT_FILE")" "should_build=false"
}

test_noop_schedule_and_force_build() {
  export GITHUB_EVENT_NAME=schedule
  run_script sync.sh
  assert_contains "$(cat "$OUTPUT_FILE")" "should_build=false"
  : > "$OUTPUT_FILE"
  export FORCE_BUILD=true
  run_script sync.sh
  assert_contains "$(cat "$OUTPUT_FILE")" "should_build=true"
}

test_rebase_conflict_is_reported_and_aborted() {
  commit_file "fork change"
  git push -q origin feature
  local fork_sha
  fork_sha=$(git rev-parse HEAD)
  git switch -q dev
  commit_file "upstream change"
  git push -q origin dev
  git switch -q feature
  export GITHUB_EVENT_NAME=schedule
  if run_script sync.sh >/dev/null 2>&1; then
    echo "expected rebase conflict to fail" >&2
    return 1
  fi
  local out
  out=$(cat "$OUTPUT_FILE")
  assert_contains "$out" "conflict=true"
  assert_contains "$out" $'\nfile\n'
  assert_eq "$fork_sha" "$(git rev-parse HEAD)"
  assert_eq "" "$(git status --porcelain)"
}

test_manual_skip_keeps_fork_head_and_records_old_base() {
  git switch -q dev
  commit_file "new upstream" "upstream-only"
  git push -q origin dev
  git switch -q feature
  export GITHUB_EVENT_NAME=workflow_dispatch
  export SKIP_REBASE=true
  run_script sync.sh
  local out
  out=$(cat "$OUTPUT_FILE")
  assert_contains "$out" "commit_sha=$BASE_SHA"
  assert_contains "$out" "upstream_base=$BASE_SHA"
}

test_push_sync_updates_only_tested_head() {
  commit_file "verified" "fork-only"
  local tested
  tested=$(git rev-parse HEAD)
  export FORK_LEASE="$BASE_SHA"
  export TESTED_SHA="$BASE_SHA"
  if run_script push-sync.sh >/dev/null 2>&1; then
    echo "expected stale TESTED_SHA push-sync to fail" >&2
    return 1
  fi
  export TESTED_SHA="$tested"
  run_script push-sync.sh
  local remote_ref
  remote_ref=$(git ls-remote origin refs/heads/feature)
  if [[ $remote_ref != "$tested"* ]]; then
    echo "expected remote ref to point to $tested, got $remote_ref" >&2
    return 1
  fi
}

test_clean_rebase_and_captured_lease_rejects_later_push() {
  commit_file "fork" "fork-only"
  git push -q origin feature
  local lease
  lease=$(git rev-parse HEAD)
  git switch -q dev
  commit_file "upstream" "upstream-only"
  git push -q origin dev
  git switch -q feature
  export GITHUB_EVENT_NAME=schedule
  run_script sync.sh
  local tested
  tested=$(git rev-parse HEAD)
  assert_ne "$lease" "$tested"
  # A maintainer advances origin while CI tests its rebased checkout.
  git switch -q -c maintainer "$lease"
  commit_file "maintainer" "maintainer-only"
  git push -q origin HEAD:feature
  git switch -q --detach "$tested"
  export FORK_LEASE="$lease"
  export TESTED_SHA="$tested"
  if run_script push-sync.sh >/dev/null 2>&1; then
    echo "expected lease check to reject push after maintainer advance" >&2
    return 1
  fi
}

test_retry_only_named_packages_once() {
  install_go_fake $'FAIL\texample/b\t0.1s\nFAIL example/a 0.1s\nFAIL'
  run_script test.sh >/dev/null
  local non_inv_calls
  non_inv_calls=$(awk '$2 != "-v" { print }' "$CALLS_FILE")
  assert_eq $'test ./...\ntest -count=1 example/a example/b' "$non_inv_calls"
  if [[ ! -f "$REPO_DIR/test-retry.log" ]]; then
    echo "expected test-retry.log to exist" >&2
    return 1
  fi
}

test_retry_failure_is_not_masked() {
  install_go_fake "FAIL example/a 0.1s" 1
  if run_script test.sh >/dev/null 2>&1; then
    echo "expected failing retry to exit non-zero" >&2
    return 1
  fi
  assert_eq "2" "$(wc -l < "$CALLS_FILE" | tr -d ' ')"
}

test_nonretryable_failures() {
  local text
  for text in \
    "network process failed" \
    "FAIL example/a [build failed]" \
    "FAIL example/a [setup failed]" \
    $'WARNING: DATA RACE\nFAIL example/a'; do
    : > "$CALLS_FILE"
    install_go_fake "$text"
    if run_script test.sh >/dev/null 2>&1; then
      echo "expected non-retryable failure to fail immediately: $text" >&2
      return 1
    fi
    assert_eq "test ./..." "$(cat "$CALLS_FILE")"
  done
}

test_successful_test_suite_is_not_retried() {
  fake_cmd go <<'EOF'
echo "$*" >> "$CALLS"
if [[ ${1:-} == "test" && ${2:-} == "-v" ]]; then
  pattern=""
  prev=""
  for arg in "$@"; do
    if [[ $prev == "-run" ]]; then
      pattern=$arg
      break
    fi
    prev=$arg
  done
  pattern=${pattern#\^\(}
  pattern=${pattern%\)\$}
  IFS="|" read -r -a names <<< "$pattern"
  for name in "${names[@]}"; do
    echo "=== RUN   $name"
    echo "--- PASS: $name (0.00s)"
  done
fi
EOF
  run_script test.sh >/dev/null
  local non_inv_calls
  non_inv_calls=$(awk '$2 != "-v" { print }' "$CALLS_FILE")
  assert_eq "test ./..." "$non_inv_calls"
  assert_eq "6" "$(grep -c '^test -v -count=1 ' "$CALLS_FILE")"
}

test_missing_invariant_test_fails_closed() {
  install_go_fake "" 0
  export FAKE_MISSING_TEST=TestFileRequestLogger_HomeEnabled_ForwardsStreamingJSONAPIErrorResponses
  # Override go fake to pass initial `go test ./...` while missing one invariant test.
  fake_cmd go <<'EOF'
echo "$*" >> "$CALLS"
if [[ $* == "test ./..." ]]; then
  exit 0
fi
if [[ ${1:-} == "test" && ${2:-} == "-v" ]]; then
  if [[ ${FAKE_NO_TESTS_TO_RUN:-0} == 1 ]]; then
    echo "[no tests to run]"
    exit 0
  fi
  pattern=""
  prev=""
  for arg in "$@"; do
    if [[ $prev == "-run" ]]; then
      pattern=$arg
      break
    fi
    prev=$arg
  done
  pattern=${pattern#\^\(}
  pattern=${pattern%\)\$}
  IFS="|" read -r -a names <<< "$pattern"
  for name in "${names[@]}"; do
    if [[ $name == "${FAKE_MISSING_TEST:-}" ]]; then
      continue
    fi
    echo "=== RUN   $name"
  done
fi
EOF
  if run_script test.sh >/dev/null 2>&1; then
    echo "expected missing invariant test to fail test.sh" >&2
    return 1
  fi
  unset FAKE_MISSING_TEST
  export FAKE_NO_TESTS_TO_RUN=1
  if run_script test.sh >/dev/null 2>&1; then
    echo "expected [no tests to run] to fail test.sh" >&2
    return 1
  fi
}

test_image_bump_and_noop() {
  local text=$'services:\n  proxy:\n    image: ghcr.io/nosovk/cli-proxy-api:1234567\n'
  printf '%s' "$text" > "$REPO_DIR/docker-compose.yml"
  export NEW_TAG=abcdef1
  run_script bump-image.sh
  local expected=${text/1234567/abcdef1}
  diff -u <(printf '%s' "$expected") "$REPO_DIR/docker-compose.yml"
  local out
  out=$(cat "$OUTPUT_FILE")
  assert_contains "$out" "old_tag=1234567"
  assert_contains "$out" "changed=true"

  : > "$OUTPUT_FILE"
  run_script bump-image.sh
  out=$(cat "$OUTPUT_FILE")
  assert_contains "$out" "old_tag=abcdef1"
  assert_contains "$out" "changed=false"
}

test_missing_or_ambiguous_image_fails_closed() {
  local line='    image: ghcr.io/nosovk/cli-proxy-api:1234567'
  export NEW_TAG=abcdef1
  local text
  for text in $'services: {}\n' "${line}"$'\n'"${line}"$'\n'; do
    printf '%s' "$text" > "$REPO_DIR/docker-compose.yml"
    if run_script bump-image.sh >/dev/null 2>&1; then
      echo "expected bump-image.sh to fail closed for: $text" >&2
      return 1
    fi
    diff -u <(printf '%s' "$text") "$REPO_DIR/docker-compose.yml"
  done
  printf '    image: ghcr.io/nosovk/cli-proxy-api:1234567\n' > "$REPO_DIR/docker-compose.yml"
  export NEW_TAG=not-a-sha
  if run_script bump-image.sh >/dev/null 2>&1; then
    echo "expected invalid NEW_TAG to fail closed" >&2
    return 1
  fi
}

test_changelog_uses_recorded_base_and_limits_long_history() {
  local i
  for ((i = 0; i < 65; i++)); do
    git commit -q --allow-empty -m "upstream-$i"
  done
  local new_base
  new_base=$(git rev-parse HEAD)
  git push -q origin HEAD:dev
  commit_file "fork topic" "fork-only"
  local sha
  sha=$(git rev-parse HEAD)
  fake_cmd docker <<'EOF'
echo "$OLD_FULL"
EOF
  local body="$FIXTURE_ROOT/body.md"
  export NEW_SHA="$sha"
  export NEW_BASE="$new_base"
  export OLD_TAG="${BASE_SHA:0:7}"
  export NEW_TAG="${sha:0:7}"
  export OLD_FULL="$BASE_SHA"
  export BODY_FILE="$body"
  run_script changelog.sh
  local text
  text=$(cat "$body")
  assert_contains "$text" "65 commits"
  assert_contains "$text" "and 5 more"
  assert_contains "$text" "fork topic"
  assert_contains "$text" "tested commit \`$sha\`"
  export NEW_SHA="$BASE_SHA"
  if run_script changelog.sh >/dev/null 2>&1; then
    echo "expected changelog.sh to reject mismatched HEAD" >&2
    return 1
  fi
}

test_conflict_report_survives_missing_label_permission() {
  fake_cmd gh <<'EOF'
echo "$*" >> "$CALLS"
case "$1 $2" in
  "label create") exit 1 ;;
  "issue create") [[ $* != *--label* ]] ;;
esac
EOF
  export UPSTREAM_SHA="$BASE_SHA"
  export FORK_SHA="$BASE_SHA"
  run_script conflicts.sh
  assert_contains "$(cat "$CALLS_FILE")" "issue create"
}

test_deploy_proposal_pushes_and_never_merges() {
  fake_cmd gh <<'EOF'
echo "$*" >> "$CALLS"
EOF
  export DEPLOY_REPO=test/deploy
  export DEPLOY_BRANCH=deploy/bump
  export NEW_TAG=abcdef1
  export OLD_TAG=1234567
  export BODY_FILE="$FIXTURE_ROOT/body"
  printf 'Proposal only\n' > "$BODY_FILE"
  printf 'new pin\n' > "$REPO_DIR/docker-compose.yml"
  run_script propose-deploy.sh
  local calls
  calls=$(cat "$CALLS_FILE")
  assert_contains "$calls" "pr create"
  assert_not_contains "$calls" "merge"
  assert_contains "$(git ls-remote origin)" "refs/heads/deploy/bump"
}

test_workflow_immutable_handoff() {
  local wf="$ROOT_DIR/.github/workflows/fork-sync-build.yml"
  local ref_count
  # shellcheck disable=SC2016
  ref_count=$(grep -cF 'ref: ${{ needs.sync-and-test.outputs.commit_sha }}' "$wf")
  assert_eq "2" "$ref_count" "expected commit_sha checkout in build and propose-deploy"
  local text
  text=$(cat "$wf")
  # shellcheck disable=SC2016
  assert_contains "$text" 'upstream_base: ${{ steps.sync.outputs.upstream_base }}'
  assert_contains "$text" 'go-version-file: go.mod'
  assert_contains "$text" 'cancel-in-progress: false'
  local sync_line go_line
  sync_line=$(grep -nF 'run: bash scripts/fork-ci/sync.sh' "$wf" | head -n 1 | cut -d: -f1)
  go_line=$(grep -nF 'go-version-file: go.mod' "$wf" | head -n 1 | cut -d: -f1)
  if [[ -z $sync_line || -z $go_line || $sync_line -ge $go_line ]]; then
    echo "expected sync.sh (line $sync_line) before go-version-file (line $go_line)" >&2
    return 1
  fi
}

test_zero_python_in_ci() {
  if find "$SCRIPTS_DIR" -name '*.py' | grep -q .; then
    echo "unexpected Python files in $SCRIPTS_DIR" >&2
    return 1
  fi
  if grep -En '(^|[[:space:]/])py[t]hon3?([[:space:]]|$)' \
    "$ROOT_DIR/.github/workflows/fork-checks.yml" \
    "$ROOT_DIR/.github/workflows/fork-sync-build.yml" \
    "$SCRIPTS_DIR"/*.sh; then
    echo "unexpected Python reference in fork CI files" >&2
    return 1
  fi
}

test_minimal_worker_fork_invariants() {
  local test_sh_code
  test_sh_code=$(grep -v '^[[:space:]]*#' "$SCRIPTS_DIR/test.sh")

  # All 4 minimal Home-worker fork invariant groups must be enforced by test.sh.
  local required_test
  for required_test in \
    TestJSONRequestLogging \
    TestJSONStreamingRequestLogging \
    TestFileRequestLogger_HomeEnabled_BoundsStreamingJSONBodies \
    TestFileRequestLogger_HomeEnabled_ForwardsStreamingAPIErrorResponses \
    TestFileRequestLogger_HomeEnabled_ForwardsStreamingJSONAPIErrorResponses \
    TestCaptureRequestInfo_BoundsJSONRequestBody \
    TestSaveConfigPreserveComments_PrunesDefaultRequestLogFormat \
    TestUpdateClientsAppliesRequestLogFormat \
    TestModelsMultiAgentClientConfigAndReload \
    TestResponsesWebsocketTimelineUsesBoundedJSONSource \
    TestResponsesLateInterruptDoesNotPoisonNextCreate \
    TestCodexClientModelsResponseMultiAgentV2FollowsConfig; do
    assert_contains "$test_sh_code" "$required_test" "test.sh missing invariant $required_test"
  done

  # Retired standalone credential-pools / nearest-reset patches must not be required.
  local retired_pattern
  for retired_pattern in credential-pool credential_pool nearest-reset nearest_reset TestCredentialLoad; do
    assert_not_contains "$test_sh_code" "$retired_pattern" "test.sh must not require retired $retired_pattern"
  done

  # Source-level minimal Home-worker fork contracts.
  if ! grep -Eq 'writeJSONLog\(.*w\.apiResponseErrors' "$ROOT_DIR/internal/logging/request_logger_home.go"; then
    echo "request_logger_home.go must forward w.apiResponseErrors to writeJSONLog" >&2
    return 1
  fi
  if ! grep -q 'func (s \*responsesLocalInterrupt) setResponseID(' "$ROOT_DIR/sdk/api/handlers/openai/openai_responses_websocket_input.go"; then
    echo "openai_responses_websocket_input.go missing setResponseID" >&2
    return 1
  fi
  if ! grep -q 'opts.localInterrupt.setResponseID(' "$ROOT_DIR/sdk/api/handlers/openai/openai_responses_websocket_forward.go"; then
    echo "openai_responses_websocket_forward.go missing setResponseID call" >&2
    return 1
  fi
  if ! grep -q 'delete(entry, "multi_agent_version")' "$ROOT_DIR/internal/client/codex/models/models.go"; then
    echo "models.go missing multi_agent_version cleanup" >&2
    return 1
  fi
  if grep -q 'nearest-reset' "$ROOT_DIR/internal/api/handlers/management/config_basic.go"; then
    echo "config_basic.go unexpectedly contains retired nearest-reset strategy" >&2
    return 1
  fi
  local retired_file
  for retired_file in \
    sdk/cliproxy/auth/credential_pool.go \
    sdk/cliproxy/auth/credential_load.go \
    internal/config/credential_pool.go; do
    if [[ -e "$ROOT_DIR/$retired_file" ]]; then
      echo "retired standalone file still present: $retired_file" >&2
      return 1
    fi
  done
}

TESTS=(
  test_push_identity_and_base
  test_stale_push_is_not_built
  test_noop_schedule_and_force_build
  test_rebase_conflict_is_reported_and_aborted
  test_manual_skip_keeps_fork_head_and_records_old_base
  test_push_sync_updates_only_tested_head
  test_clean_rebase_and_captured_lease_rejects_later_push
  test_retry_only_named_packages_once
  test_retry_failure_is_not_masked
  test_nonretryable_failures
  test_successful_test_suite_is_not_retried
  test_missing_invariant_test_fails_closed
  test_image_bump_and_noop
  test_missing_or_ambiguous_image_fails_closed
  test_changelog_uses_recorded_base_and_limits_long_history
  test_conflict_report_survives_missing_label_permission
  test_deploy_proposal_pushes_and_never_merges
  test_workflow_immutable_handoff
  test_zero_python_in_ci
  test_minimal_worker_fork_invariants
)

passed=0
failed=0
for fn in "${TESTS[@]}"; do
  FIXTURE_ROOT=''
  setup_fixture
  out_file="$FIXTURE_ROOT/test-output.log"
  set +e
  (
    set -euo pipefail
    "$fn"
  ) >"$out_file" 2>&1
  rc=$?
  set -e
  if [[ $rc -eq 0 ]]; then
    echo "PASS: $fn"
    passed=$((passed + 1))
  else
    echo "FAIL: $fn (exit $rc)" >&2
    cat "$out_file" >&2
    failed=$((failed + 1))
  fi
  cleanup_fixture
done

echo "Fork CI contracts: $passed passed, $failed failed (total ${#TESTS[@]})"
if [[ $failed -ne 0 ]]; then
  exit 1
fi
