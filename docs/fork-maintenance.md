# Maintaining the nosovk fork (minimal Home-worker patch set)

## Patch inventory and rollback

The initial upstream base for this minimal Home-worker reconstruction was
`f2efe2c41305ceb88043d34ce91f61b7765100fe` (`router-for-me/CLIProxyAPI:dev`).

The 2026-11-18 sync rebases onto `9b4f3f30c` (`dev`). The pre-sync head
`52b444116230a2be855f5d4b3bac0bce84934970` is preserved as
`archive/fork-before-sync-2026-11-18`. Logging keeps upstream's separation of
local diagnostics from provider responses: both file and Home JSON logs put
local failures in `response.errors`, not `api_response_errors`. The Codex
catalog test follows the refreshed template's empty GPT-5.5 `service_tiers`.

The previous full standalone fork stack is preserved on remote `fork` as
`backup/standalone-fork-635ac08-2026-11-16`
(`635ac082d933c0cc1f421816dafbf9bfc89e33ff`), and the earlier 27-commit stack
remains preserved as `archive/fork-before-maintenance-2026-11-14`
(`fc155e67af89a5942675e0c96fde4fa4805c7db1`).

### Retained patches (minimal Home-worker set)

| Topic / commit subject | Owned changes and invariants | Verification / removal condition |
| --- | --- | --- |
| `feat(logging): add bounded NDJSON request and websocket logging` | `internal/logging`, request/response capture middleware, executor log helpers, `request-log-format` config/load/reload/v8/preservation, websocket timeline, and Home streaming JSON forwarding of `w.apiResponseErrors` to `writeJSONLog`. Text remains default; JSON sections are bounded and mark truncation; capture must not alter forwarded bodies; `TestSaveConfigPreserveComments_PrunesDefaultRequestLogFormat` is included here. | `json_logger_test.go`, `request_logger_home_test.go` (including `TestFileRequestLogger_HomeEnabled_ForwardsStreamingJSONAPIErrorResponses`), middleware capture/response tests, websocket bounded timeline test, config pruning test. Remove when upstream supports the same NDJSON schema, bounds, reload, Home error forwarding and timeline behavior. |
| `fix(compat): preserve websocket interrupts and align upstream contracts` | Websocket `responsesLocalInterrupt` `responseID` tracking and nil guard (`openai_responses_websocket_input.go`, `openai_responses_websocket_forward.go`); Codex model catalog `multi_agent_version` cleanup when opt-in is false (`internal/client/codex/models/models.go` + `server_multi_agent_config_test.go` / `codex_client_models_test.go`, keeping `models_test.go` identical to `f2efe2c41`); v8 translator import boundary, plugin host isolation/cooldown-floor tests, and reviewed kvstore no-copy rationale. | OpenAI websocket/model tests, `internal/api` multi-agent reload tests, pluginhost, util and translator tests. Re-check each hunk on every sync and drop it when upstream covers its regression. |
| `ci: automate minimal Home-worker fork sync and zero-Python checks` | Fork-only workflows (`.github/workflows/fork-sync-build.yml`, `.github/workflows/fork-checks.yml`), pure-shell scripts under `scripts/fork-ci/` (zero Python: `bump-image.sh` and `test_contracts.sh`), uncached minimal-invariant checks in `test.sh`, and removal of upstream PR contribution guards (`agents-md-guard.yml`, `pr-path-guard.yml`). | Offline CI contracts (`bash scripts/fork-ci/test_contracts.sh`), `shellcheck`, `actionlint`. Retain while this fork publishes its own image; restore upstream guards if changing to an upstream PR branch. |

### Retired standalone commits

In Home-worker mode, credential selection, routing, and downstream pool policy
enforcement are delegated to Home rather than executed locally on the worker.
The following standalone-only commits were retired from `feature/json-request-logging`
and remain preserved on `backup/standalone-fork-635ac08-2026-11-16`:

| Retired commit | Subject | Reason for retirement |
| --- | --- | --- |
| `b1c040dd2` | `feat(auth): isolate downstream credential pools and preserve v8 policies` | Downstream pool isolation is handled by Home; misplaced `nearest-reset` hunk in `internal/api/handlers/management/config_basic.go` from `2ea18fdfb` was also dropped. |
| `c1496a305` | `feat(routing): add nearest-reset and cache-preserving load-aware affinity` | Local credential routing/affinity strategy is not used by Home workers. |
| `e59c8ace1` | `perf(auth): avoid heap score buffers for common credential pools` | Optimization for retired local `leastLoaded` credential pool selection. |

## Stability contracts

- **Bounded JSON logging**: JSON bounds are **per captured section**
  (`maxJSONFileBackedSectionBytes` and `maxJSONStreamingResponseBytes`), not a
  whole-process memory quota. Keep compressed expansion, queue-drop, file-backed
  truncation, and request/response preservation tests enabled.
- **Home streaming error forwarding**: `homeStreamingLogWriter.Close` must pass
  `w.apiResponseErrors` to `writeJSONLog` in JSON mode so upstream errors remain
  present in `api_response_errors` for downstream log consumers.
- **Local response diagnostics**: file and Home logs, streaming or not, retain
  bounded local failures separately from provider errors. Text logs render them
  in `RESPONSE`; JSON logs use `response.errors` without changing response status
  or body. `TestResponseDiagnosticsInFileAndHomeLogs` covers all eight paths.
- **Websocket local interrupt isolation**: `responsesLocalInterrupt` scopes
  local HTTP fallback interrupt claims to the active `responseID` and clears
  it across turn transitions so stale `response.interrupt` frames cannot cancel
  subsequent responses.
- **Codex `multi_agent_version` opt-in**: When `optimize-multi-agent-v2` is
  disabled, synthesized/template `multi_agent_version: "v2"` metadata must be
  removed rather than leaked by default.

## CI ownership and guarantees

- `.github/workflows/fork-sync-build.yml` and `.github/workflows/fork-checks.yml`
  own triggers, permissions, job dependencies, and immutable SHA handoffs.
- `scripts/fork-ci/` owns the locally testable policies in pure Bash (zero
  Python dependencies):
  - `sync.sh`: capture remote lease, reject stale checkout, rebase, capture
    tested SHA and upstream base. Never push before tests pass.
  - `push-sync.sh`: publish only the tested HEAD using the captured explicit
    lease.
  - `test.sh`: run `go test ./...` with at most one uncached retry of named
    failing packages (failing immediately on build/setup/race/fatal errors or
    missing package lists), then run uncached (`-count=1`) invariant tests for
    all retained Home-worker patches and fail closed on `[no tests to run]`.
  - `conflicts.sh`: update conflict reports; close them only after tests and
    push.
  - `bump-image.sh`: replace exactly one pinned image tag in
    `docker-compose.yml` or fail closed without changing it.
  - `changelog.sh`: describe the tested SHA and recorded base; use
    `git log -60`, avoiding SIGPIPE under `pipefail` on long upstream histories.
  - `propose-deploy.sh`: lease-protected update of the dedicated proposal
    branch; open/update a PR, never merge it.
  - `test_contracts.sh`: offline contract suite verifying all CI scripts,
    immutable workflow handoffs, zero Python usage, and minimal Home-worker
    fork invariants without contacting GitHub, registries, or production.

## Local checks before publishing

```sh
bash scripts/fork-ci/test_contracts.sh
for script in scripts/fork-ci/*.sh; do bash -n "$script"; done
mise x shellcheck@0.11.0 -- shellcheck scripts/fork-ci/*.sh
mise x actionlint@1.7.7 shellcheck@0.11.0 -- actionlint .github/workflows/fork-sync-build.yml .github/workflows/fork-checks.yml
GOTOOLCHAIN=go1.26.10 bash scripts/fork-ci/test.sh
GOTOOLCHAIN=go1.26.10 go vet ./...
GOTOOLCHAIN=go1.26.10 go build -o /tmp/opencode/cli-proxy-api-check ./cmd/server
GOTOOLCHAIN=go1.26.10 go test -race ./sdk/api/handlers/openai ./internal/logging ./internal/api/middleware ./internal/config ./internal/client/codex/models ./internal/util
git diff --check
```

## Recurring sync and rollback

1. Fetch `fork` and `origin`; record the full remote head SHA and upstream base.
2. Preserve a dated archive ref before rewriting.
3. Rebase the retained commits onto upstream `dev`. For each conflict, inspect
   upstream callers/tests first; do not blindly keep the fork side.
4. Re-evaluate each inventory removal condition. Drop upstreamed hunks and
   keep regression coverage; do not re-introduce retired standalone patches.
5. Run all local checks above, review both the upstream-relative diff and
   old-head diff, and verify `test_contracts.sh` passes.
6. Push the active branch with
   `--force-with-lease=refs/heads/feature/json-request-logging:<observed-full-SHA>`.
   If the lease fails, inspect the new head before retrying.
7. Check Actions and deploy PR comments. Image publication is not production
   deployment: production changes only when a human merges the `ai-gate` deploy
   PR and `doco-cd` reconciles it.

Rollback: restore from `backup/standalone-fork-635ac08-2026-11-16` (or the
previous verified image tag in `ai-gate`) to build/pin a reviewed replacement.
Rewriting a branch alone does not roll back production.
