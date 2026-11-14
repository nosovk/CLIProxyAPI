# Maintaining the nosovk fork

## Patch inventory and rollback

The upstream base for the November 14, 2026 reconstruction is
`f2efe2c41305ceb88043d34ce91f61b7765100fe` (`router-for-me/CLIProxyAPI:dev`).
The original 27-commit stack ends at `fc155e67af89a5942675e0c96fde4fa4805c7db1`
and is preserved as `archive/fork-before-maintenance-2026-11-14`.
The five reconstructed topic commits have **exactly the same final Git tree**
(`cf8badc63c13ac2e7f18749953b2af2a85e522cd`). No runtime behavior was changed
by regrouping history. Performance and CI changes follow as separate commits.

| Topic / commit subject | Owned changes and invariants | Verification / removal condition |
| --- | --- | --- |
| `feat(logging): add bounded NDJSON request and websocket logging` | `internal/logging`, request/response capture middleware, executor log helpers, request-log-format config/load/reload/migration, websocket timeline. Text remains default; JSON sections are bounded and mark truncation; capture must not alter forwarded bodies. | `json_logger_test.go`, middleware capture/response tests, websocket bounded timeline test. Remove when upstream supports the same NDJSON schema, bounds, reload and timeline behavior. |
| `feat(auth): isolate downstream credential pools and preserve v8 policies` | Config models/serialization, v8 migration, authenticated routes, detached handler contexts, auth eligibility including Antigravity credits. Omitted provider is unrestricted; explicit `[]` denies all; retries, cooldown and affinity cannot escape the pool. | Config pool round-trip tests, handler context tests, auth pool/Antigravity tests. Remove only when upstream enforces the same end-to-end access policy. |
| `feat(routing): add nearest-reset and cache-preserving load-aware affinity` | Quota-reset parsing, selector/scheduler, execution/stream load accounting, routing reload, priority-respect opt-in. Unknown windows are probed round-robin; priority/eligibility precede load; existing bindings survive load changes by default. | Nearest-reset, quota-reset, credential-load, priority-recovery and routing-state tests. Remove strategy and affinity extensions independently when upstream provides equivalent behavior. |
| `fix(compat): preserve websocket interrupts and align upstream contracts` | Websocket active-response interrupt IDs; Codex model catalog opt-in compatibility; v8 translator import tripwire; plugin host isolation and cooldown-floor expectations; reviewed kvstore byte-write rationale. | OpenAI websocket/model tests, pluginhost, util and translator tests. Re-check each hunk on every sync and drop it when upstream covers its regression. The kvstore hunk is only an existing comment/reordering, retained for exact-tree reconstruction. |
| `ci: automate fork sync, image publishing and GitOps deploy proposals` | Fork-only workflows and removal of upstream contribution guards. No direct production deployment. | Offline CI contracts and actionlint. Retain while this fork publishes its own image; restore upstream contribution guards if changing to an upstream PR branch. |

Some files carry multiple topics (`config.example.yaml`, config structs/migration,
auth conductor selection). Their hunks are split by responsibility; do not assign
an entire shared file to whichever topic happened to touch it last.

## Stability and performance contract

- Keep the tracker lock: one snapshot must see coherent load values. Do not replace
  it with unrelated per-account reads without testing selection consistency.
- `acquire` release is idempotent; non-stream execution releases on errors/panic;
  stream ownership lasts until stream completion. Executors must honor context
  cancellation and close their chunk channels (upstream stream draining depends
  on that contract; this refactor does not add a timeout or change it).
- Do not optimize load balancing by moving established session bindings. Prompt
  cache locality is part of the contract. `session-affinity-respect-priority` is
  explicitly opt-in and can rebind on priority recovery.
- JSON bounds are **per captured section**, not a whole-process memory quota.
  Concurrency, compression decoder working memory and temporary files still add
  overhead. Keep compressed expansion, queue-drop, file-backed truncation and
  request/response preservation tests enabled.
- Avoid speculative lock sharding, global scratch buffers and object pools. They
  add synchronization/ownership risk and require measured evidence.

### Measured allocation change

`leastLoaded` now uses a request-local 128-score stack buffer, with a heap fallback
for larger credential pools. The lock, scores, tolerance and candidate order are
unchanged. There is no shared scratch storage and the input slice is not mutated.

Go 1.26.10, linux/amd64, i7-1370P, parallel benchmark (20 logical CPUs), three
200 ms runs per case on November 14, 2026:

| Candidates | Before B/op | After B/op | Before allocations/op | After allocations/op |
| --- | ---: | ---: | ---: | ---: |
| 4 | 64 | 32 | 2 | 1 |
| 32 | 512 | 256 | 2 | 1 |
| 128 | 2176 | 1152 | 2 | 1 |

These allocation counts were consistent with and without recent 429 penalties.
Timing varied materially between runs; **no end-to-end latency improvement is
claimed**. This trades 1 KiB of bounded stack storage per selection for one less
heap allocation. Above 128 candidates the old heap behavior remains available.

Reproduce:

```sh
GOTOOLCHAIN=go1.26.10 go test ./sdk/cliproxy/auth -run '^TestCredentialLoad' \
  -bench '^BenchmarkCredentialLoadLeastLoaded$' -benchmem -benchtime=200ms -count=3
```

## CI ownership and guarantees

Before: one 381-line workflow mixed sync, retries, reports and deploy generation;
build and changelog checked out a moving branch; deploy used an unconditional
force push; setup-go happened before a potentially toolchain-changing rebase.

After: YAML owns events, permissions, job dependencies and immutable outputs.
`scripts/fork-ci/` owns the locally testable policies:

- `sync.sh`: capture remote lease, reject stale checkout, rebase, capture tested
  SHA and upstream base. Never push before tests pass.
- `push-sync.sh`: publish only the tested HEAD using the captured explicit lease.
- `test.sh`: one uncached retry of named failing packages. Build/setup/race errors
  and failures without a package list fail immediately; both logs are retained.
- `conflicts.sh`: update conflict reports; close them only after tests and push.
- `bump-image.py`: replace exactly one image pin or fail without changing it.
- `changelog.sh`: describe the tested SHA and recorded base, not a new branch tip;
  use `git log -60`, avoiding SIGPIPE under `pipefail` on long upstream histories.
- `propose-deploy.sh`: lease-protected update of the dedicated proposal branch;
  open/update a PR, never merge it.

The added coupling is a small environment-variable contract between workflow and
scripts, covered by `test_contracts.py`. Go version is read **after** sync. Build
and changelog both check out the tested SHA. A single non-cancelling concurrency
group serializes workflow writers; explicit leases protect against external
maintainer pushes. Only the sync job has contents/issues write permission; only
the build job has package write permission. PR checks are read-only and never
publish or use the deployment token.

Limits: GitHub concurrency is not a FIFO queue. A maintainer can push while an
older immutable build is running; the older image still contains exactly its
labelled/tested code, and the newer run follows. The dedicated deploy branch is
bot-owned: do not place manual changes on it (leases protect concurrent changes,
not edits that predate lease capture). Registry and GitHub permission failures
must be validated in a live Actions run; offline tests cannot prove credentials.

## Local checks before publishing

```sh
python3 -m unittest discover -s scripts/fork-ci -p 'test_*.py' -v
for script in scripts/fork-ci/*.sh; do bash -n "$script"; done
mise x shellcheck@0.11.0 -- shellcheck scripts/fork-ci/*.sh
mise x actionlint@1.7.7 shellcheck@0.11.0 -- actionlint .github/workflows/fork-sync-build.yml .github/workflows/fork-checks.yml
GOTOOLCHAIN=go1.26.10 go test ./...
GOTOOLCHAIN=go1.26.10 go vet ./...
GOTOOLCHAIN=go1.26.10 go build -o /tmp/opencode/cli-proxy-api-check ./cmd/server
GOTOOLCHAIN=go1.26.10 go test -race ./sdk/cliproxy/auth ./sdk/api/handlers/openai \
  ./internal/logging ./internal/api/middleware ./internal/util
git diff --check
```

Offline tests use temporary local repositories and fake external commands. They
do not contact GitHub/registries or modify production. Real Go race checks are
not retried. CI uploads full-suite/retry logs to retain evidence of flaky tests.

### Reconstruction verification record

- Identical tree hash at the five-topic checkpoint verified against `fc155e67`.
- Each of the first three feature checkpoints compiled all packages separately
  (`go test -run '^$' ./...` from exported trees).
- Full `go test ./...`, `go vet ./...`, server build: passed on Go 1.26.10.
- Race detector passed for all five packages listed above; the new load tests
  also passed five uncached race runs and ten ordinary runs.
- 17 offline CI contracts, shellcheck 0.11.0, actionlint 1.7.7 and diff whitespace
  checks passed. No external service is needed to verify these boundaries.
- Remaining live validation: the first GitHub Actions publication/proposal run.

## Recurring sync and patch retirement

1. Fetch fork and upstream; record the full remote head SHA and upstream base.
2. Use a separate clean worktree; preserve a dated archive ref before rewriting.
3. Rebase topic commits onto upstream `dev`. For each conflict, inspect upstream
   callers/tests first; do not blindly keep the fork side.
4. Re-evaluate each inventory removal condition. Drop upstreamed hunks and keep
   regression coverage; do not carry historical repair commits indefinitely.
5. Run checks, simplify, review both upstream-relative diff and old-head diff.
   For history-only changes require identical tree hashes. Explain every net
   runtime difference and retain performance evidence for optimizations.
6. Push the archive ref, then push the active branch with
   `--force-with-lease=refs/heads/feature/json-request-logging:<observed-full-SHA>`.
   If the lease fails, inspect the new head; never replace it by refreshing the
   lease without review.
7. Check Actions and deploy PR comments. Image publication is not production
   deployment: production changes only when a human merges the ai-gate deploy PR
   and doco-cd reconciles it. Never run the legacy SCP/restart deploy script.

Rollback: use the archive as the source for a new reviewed image build and deploy
proposal (or pin a previously verified image through an ai-gate PR). Rewriting a
branch alone does not roll back production. Do not reset another worktree or
force the remote without recording its current head and inspecting new work.
