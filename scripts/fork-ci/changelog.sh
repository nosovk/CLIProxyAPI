#!/usr/bin/env bash
set -euo pipefail
: "${NEW_SHA:?}" "${NEW_BASE:?}" "${OLD_TAG:?}" "${NEW_TAG:?}" "${UPSTREAM_BRANCH:?}" "${UPSTREAM_REPO:?}" "${RUN_URL:?}" "${BODY_FILE:?}"
[[ $(git rev-parse HEAD) == "$NEW_SHA" ]]
git merge-base --is-ancestor "$NEW_BASE" "$NEW_SHA"
git fetch "$UPSTREAM_REPO" "$UPSTREAM_BRANCH"
upstream=$(git rev-parse FETCH_HEAD)
fork_commits=$(git log --format='- %h %s' "$NEW_BASE..$NEW_SHA")
lag=$(git rev-list --count "$NEW_BASE..$upstream")
old_full=$(docker buildx imagetools inspect "ghcr.io/nosovk/cli-proxy-api:$OLD_TAG" --format '{{ index .Image.Config.Labels "org.opencontainers.image.revision" }}' 2>/dev/null || true)
if [[ ! $old_full =~ ^[0-9a-f]{40}$ ]]; then
  old_full=$OLD_TAG
fi
old_base=''
if [[ $old_full =~ ^[0-9a-f]{7,40}$ ]]; then
  git fetch -q origin "$old_full" 2>/dev/null || true
  old_base=$(git merge-base "$old_full" "$upstream" 2>/dev/null || true)
fi
{
  echo "Automated image bump: \`$OLD_TAG\` → \`$NEW_TAG\`."
  echo
  echo "Image: \`ghcr.io/nosovk/cli-proxy-api:$NEW_TAG\` (tested commit \`$NEW_SHA\`)"
  echo "Fork run: $RUN_URL"
  echo
  echo "### Fork commits on upstream/$UPSTREAM_BRANCH@${NEW_BASE:0:7}"
  echo "$fork_commits"
  echo
  if [[ $lag -gt 0 ]]; then
    echo "> Upstream is $lag commit(s) ahead of this build's base."
    echo
  fi
  if [[ -n $old_base ]] && git merge-base --is-ancestor "$old_base" "$NEW_BASE"; then
    count=$(git rev-list --count "$old_base..$NEW_BASE")
    echo "### Upstream changes since previous deploy ($count commits)"
    if [[ $count == 0 ]]; then
      echo 'None — same upstream base; only fork patches changed.'
    else
      echo "https://github.com/router-for-me/CLIProxyAPI/compare/${old_base:0:7}...${NEW_BASE:0:7}"
      git log -60 --format='- %h %s' "$old_base..$NEW_BASE"
      if [[ $count -gt 60 ]]; then echo "- … and $((count - 60)) more"; fi
    fi
  else
    echo 'Previous upstream base could not be resolved; inspect upstream before merging.'
  fi
  echo
  echo 'Merging deploys via doco-cd. Review before merging; this workflow never merges the PR.'
} > "$BODY_FILE"
