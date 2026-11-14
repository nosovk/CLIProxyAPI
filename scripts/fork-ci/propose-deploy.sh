#!/usr/bin/env bash
set -euo pipefail
: "${DEPLOY_REPO:?}" "${DEPLOY_BRANCH:?}" "${NEW_TAG:?}" "${OLD_TAG:?}" "${BODY_FILE:?}"
# Capture the existing proposal branch, not a tracking ref that a later fetch can
# silently refresh. An empty lease permits creation only when it stays absent.
lease=$(git ls-remote origin "refs/heads/$DEPLOY_BRANCH" | awk '{print $1}')
git config user.name 'github-actions[bot]'
git config user.email 'github-actions[bot]@users.noreply.github.com'
git switch -c "$DEPLOY_BRANCH"
git add docker-compose.yml
git commit -m "deploy: bump cli-proxy-api to $NEW_TAG"
git push "--force-with-lease=refs/heads/$DEPLOY_BRANCH:$lease" origin "HEAD:refs/heads/$DEPLOY_BRANCH"
title="deploy: bump cli-proxy-api $OLD_TAG → $NEW_TAG"
existing=$(gh pr list --repo "$DEPLOY_REPO" --head "$DEPLOY_BRANCH" --state open --json number --jq '.[0].number')
if [[ -n $existing ]]; then
  gh pr edit "$existing" --repo "$DEPLOY_REPO" --title "$title" --body-file "$BODY_FILE"
else
  gh pr create --repo "$DEPLOY_REPO" --base main --head "$DEPLOY_BRANCH" --title "$title" --body-file "$BODY_FILE"
fi
