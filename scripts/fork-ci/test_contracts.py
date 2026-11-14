"""Offline contracts: real disposable Git repos, fake Go/GitHub/registry commands.

Run from any checkout: python3 -m unittest discover -s scripts/fork-ci -v
Nothing here contacts GitHub, a registry, or production.
"""
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

SCRIPTS = Path(__file__).resolve().parent
ROOT = SCRIPTS.parents[1]


class Contracts(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="fork-ci-")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.repo = self.root / "repo"
        self.repo.mkdir()
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.output = self.root / "output"
        self.output.touch()
        self.calls = self.root / "calls"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        GIT_CONFIG_GLOBAL="/dev/null", GIT_CONFIG_NOSYSTEM="1",
                        GIT_AUTHOR_NAME="Test", GIT_AUTHOR_EMAIL="test@example.invalid",
                        GIT_COMMITTER_NAME="Test", GIT_COMMITTER_EMAIL="test@example.invalid",
                        GIT_TERMINAL_PROMPT="0", GITHUB_OUTPUT=str(self.output),
                        TARGET_BRANCH="feature", UPSTREAM_BRANCH="dev",
                        GITHUB_EVENT_NAME="push", GITHUB_REPOSITORY="test/fork",
                        RUN_URL="https://example.invalid/run", CALLS=str(self.calls))
        self.git("init", "-b", "feature")
        self.git("config", "commit.gpgsign", "false")
        self.commit("base")
        self.base = self.git("rev-parse", "HEAD").stdout.strip()
        self.git("branch", "dev")
        self.git("init", "--bare", str(self.root / "remote.git"))
        self.git("remote", "add", "origin", str(self.root / "remote.git"))
        self.git("push", "origin", "feature", "dev")
        self.env["UPSTREAM_REPO"] = str(self.root / "remote.git")

    def run_command(self, *args, ok=True, **extra):
        result = subprocess.run(args, cwd=self.repo, env=self.env, text=True,
                                stdout=subprocess.PIPE, stderr=subprocess.STDOUT, **extra)
        if ok and result.returncode:
            self.fail(f"{args}: {result.stdout}")
        return result

    def git(self, *args, **kwargs):
        return self.run_command("git", *args, **kwargs)

    def commit(self, text, filename="file"):
        (self.repo / filename).write_text(text + "\n")
        self.git("add", filename)
        self.git("commit", "-m", text)

    def script(self, name, ok=True):
        return self.run_command("bash", str(SCRIPTS / name), ok=ok)

    def fake(self, name, body):
        path = self.bin / name
        path.write_text("#!/usr/bin/env bash\nset -eu\n" + body)
        path.chmod(0o755)

    def test_push_identity_and_base(self):
        self.script("sync.sh")
        out = self.output.read_text()
        for value in [f"fork_lease={self.base}", f"commit_sha={self.base}",
                      f"upstream_base={self.base}", "should_build=true"]:
            self.assertIn(value, out)

    def test_stale_push_is_not_built(self):
        self.commit("new remote")
        self.git("push", "origin", "feature")
        self.git("switch", "--detach", self.base)
        self.script("sync.sh")
        self.assertIn("should_build=false", self.output.read_text())

    def test_noop_schedule_and_force_build(self):
        self.env["GITHUB_EVENT_NAME"] = "schedule"
        self.script("sync.sh")
        self.assertIn("should_build=false", self.output.read_text())
        self.output.write_text("")
        self.env["FORCE_BUILD"] = "true"
        self.script("sync.sh")
        self.assertIn("should_build=true", self.output.read_text())

    def test_rebase_conflict_is_reported_and_aborted(self):
        self.commit("fork change")
        self.git("push", "origin", "feature")
        fork = self.git("rev-parse", "HEAD").stdout.strip()
        self.git("switch", "dev")
        self.commit("upstream change")
        self.git("push", "origin", "dev")
        self.git("switch", "feature")
        self.env["GITHUB_EVENT_NAME"] = "schedule"
        result = self.script("sync.sh", ok=False)
        self.assertNotEqual(0, result.returncode)
        out = self.output.read_text()
        self.assertIn("conflict=true", out)
        self.assertIn("\nfile\n", out)
        self.assertEqual(fork, self.git("rev-parse", "HEAD").stdout.strip())
        self.assertEqual("", self.git("status", "--porcelain").stdout)

    def test_manual_skip_keeps_fork_head_and_records_old_base(self):
        self.git("switch", "dev")
        self.commit("new upstream", "upstream-only")
        self.git("push", "origin", "dev")
        self.git("switch", "feature")
        self.env.update(GITHUB_EVENT_NAME="workflow_dispatch", SKIP_REBASE="true")
        self.script("sync.sh")
        self.assertIn(f"commit_sha={self.base}", self.output.read_text())
        self.assertIn(f"upstream_base={self.base}", self.output.read_text())

    def test_push_sync_updates_only_tested_head(self):
        self.commit("verified", "fork-only")
        tested = self.git("rev-parse", "HEAD").stdout.strip()
        self.env.update(FORK_LEASE=self.base, TESTED_SHA=self.base)
        self.assertNotEqual(0, self.script("push-sync.sh", ok=False).returncode)
        self.env["TESTED_SHA"] = tested
        self.script("push-sync.sh")
        remote = self.git("ls-remote", "origin", "refs/heads/feature").stdout
        self.assertTrue(remote.startswith(tested))

    def test_clean_rebase_and_captured_lease_rejects_later_push(self):
        self.commit("fork", "fork-only")
        self.git("push", "origin", "feature")
        lease = self.git("rev-parse", "HEAD").stdout.strip()
        self.git("switch", "dev")
        self.commit("upstream", "upstream-only")
        self.git("push", "origin", "dev")
        self.git("switch", "feature")
        self.env["GITHUB_EVENT_NAME"] = "schedule"
        self.script("sync.sh")
        tested = self.git("rev-parse", "HEAD").stdout.strip()
        self.assertNotEqual(lease, tested)
        # A maintainer advances origin while CI tests its rebased checkout.
        self.git("switch", "-c", "maintainer", lease)
        self.commit("maintainer", "maintainer-only")
        self.git("push", "origin", "HEAD:feature")
        self.git("switch", "--detach", tested)
        self.env.update(FORK_LEASE=lease, TESTED_SHA=tested)
        self.assertNotEqual(0, self.script("push-sync.sh", ok=False).returncode)

    def go_fake(self, first, retry_exit=0):
        self.env.update(FIRST=first, RETRY_EXIT=str(retry_exit))
        self.fake("go", '''
echo "$*" >> "$CALLS"
if [[ $* == 'test ./...' ]]; then
  printf '%s\n' "$FIRST"
  exit 1
fi
exit "$RETRY_EXIT"
''')

    def test_retry_only_named_packages_once(self):
        self.go_fake("FAIL\texample/b\t0.1s\nFAIL example/a 0.1s\nFAIL")
        self.script("test.sh")
        self.assertEqual(["test ./...", "test -count=1 example/a example/b"], self.calls.read_text().splitlines())
        self.assertTrue((self.repo / "test-retry.log").exists())

    def test_retry_failure_is_not_masked(self):
        self.go_fake("FAIL example/a 0.1s", retry_exit=1)
        self.assertNotEqual(0, self.script("test.sh", ok=False).returncode)
        self.assertEqual(2, len(self.calls.read_text().splitlines()))

    def test_nonretryable_failures(self):
        for text in ["network process failed", "FAIL example/a [build failed]",
                     "FAIL example/a [setup failed]", "WARNING: DATA RACE\nFAIL example/a"]:
            with self.subTest(text=text):
                self.calls.write_text("")
                self.go_fake(text)
                self.assertNotEqual(0, self.script("test.sh", ok=False).returncode)
                self.assertEqual(["test ./..."], self.calls.read_text().splitlines())

    def test_successful_test_suite_is_not_retried(self):
        self.fake("go", 'echo "$*" >> "$CALLS"\n')
        self.script("test.sh")
        self.assertEqual(["test ./..."], self.calls.read_text().splitlines())

    def bump(self, text, ok=True):
        (self.repo / "docker-compose.yml").write_text(text)
        self.env["NEW_TAG"] = "abcdef1"
        return self.run_command("python3", str(SCRIPTS / "bump-image.py"), ok=ok)

    def test_image_bump_and_noop(self):
        text = "services:\n  proxy:\n    image: ghcr.io/nosovk/cli-proxy-api:1234567\n"
        self.bump(text)
        expected = text.replace("1234567", "abcdef1")
        self.assertEqual(expected, (self.repo / "docker-compose.yml").read_text())
        self.assertIn("changed=true", self.output.read_text())
        self.output.write_text("")
        self.bump(expected)
        self.assertIn("changed=false", self.output.read_text())

    def test_missing_or_ambiguous_image_fails_closed(self):
        line = "    image: ghcr.io/nosovk/cli-proxy-api:1234567\n"
        for text in ["services: {}\n", line * 2]:
            self.assertNotEqual(0, self.bump(text, ok=False).returncode)
            self.assertEqual(text, (self.repo / "docker-compose.yml").read_text())

    def test_changelog_uses_recorded_base_and_limits_long_history(self):
        # More than 60 commits used to trip pipefail via `git log | head`.
        for i in range(65):
            self.git("commit", "--allow-empty", "-m", f"upstream-{i}")
        new_base = self.git("rev-parse", "HEAD").stdout.strip()
        self.git("push", "origin", "HEAD:dev")
        self.commit("fork topic", "fork-only")
        sha = self.git("rev-parse", "HEAD").stdout.strip()
        self.fake("docker", 'echo "$OLD_FULL"\n')
        body = self.root / "body.md"
        self.env.update(NEW_SHA=sha, NEW_BASE=new_base, OLD_TAG=self.base[:7],
                        NEW_TAG=sha[:7], OLD_FULL=self.base, BODY_FILE=str(body))
        self.script("changelog.sh")
        text = body.read_text()
        self.assertIn("65 commits", text)
        self.assertIn("and 5 more", text)
        self.assertIn("fork topic", text)
        self.assertIn(f"tested commit `{sha}`", text)
        self.env["NEW_SHA"] = self.base
        self.assertNotEqual(0, self.script("changelog.sh", ok=False).returncode)

    def test_conflict_report_survives_missing_label_permission(self):
        self.fake("gh", '''
echo "$*" >> "$CALLS"
case "$1 $2" in
  'label create') exit 1 ;;
  'issue create') [[ $* != *--label* ]] ;;
esac
''')
        self.env.update(UPSTREAM_SHA=self.base, FORK_SHA=self.base)
        self.script("conflicts.sh")
        self.assertIn("issue create", self.calls.read_text())

    def test_deploy_proposal_pushes_and_never_merges(self):
        self.fake("gh", 'echo "$*" >> "$CALLS"\n')
        self.env.update(DEPLOY_REPO="test/deploy", DEPLOY_BRANCH="deploy/bump",
                        NEW_TAG="abcdef1", OLD_TAG="1234567", BODY_FILE=str(self.root / "body"))
        (self.root / "body").write_text("Proposal only")
        (self.repo / "docker-compose.yml").write_text("new pin\n")
        self.script("propose-deploy.sh")
        calls = self.calls.read_text()
        self.assertIn("pr create", calls)
        self.assertNotIn("merge", calls)
        self.assertIn("refs/heads/deploy/bump", self.git("ls-remote", "origin").stdout)

    def test_workflow_immutable_handoff(self):
        text = (ROOT / ".github/workflows/fork-sync-build.yml").read_text()
        self.assertEqual(2, text.count("ref: ${{ needs.sync-and-test.outputs.commit_sha }}"))
        self.assertIn("upstream_base: ${{ steps.sync.outputs.upstream_base }}", text)
        self.assertIn("go-version-file: go.mod", text)
        self.assertLess(text.index("run: bash scripts/fork-ci/sync.sh"), text.index("go-version-file: go.mod"))
        self.assertIn("cancel-in-progress: false", text)


if __name__ == "__main__":
    unittest.main()
