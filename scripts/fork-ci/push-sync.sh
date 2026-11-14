#!/usr/bin/env bash
set -euo pipefail
: "${TARGET_BRANCH:?}" "${FORK_LEASE:?}" "${TESTED_SHA:?}"
[[ $(git rev-parse HEAD) == "$TESTED_SHA" ]]
git push "--force-with-lease=refs/heads/$TARGET_BRANCH:$FORK_LEASE" origin "HEAD:refs/heads/$TARGET_BRANCH"
