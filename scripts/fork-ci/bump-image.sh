#!/usr/bin/env bash
set -euo pipefail

# Change exactly one pinned CLIProxyAPI image; fail closed on config drift.
: "${NEW_TAG:?}" "${GITHUB_OUTPUT:?}"

if [[ ! $NEW_TAG =~ ^[0-9a-f]{7,40}$ ]]; then
  echo 'NEW_TAG must be a commit hash' >&2
  exit 1
fi

path=docker-compose.yml
if [[ ! -f $path ]]; then
  echo "Missing $path" >&2
  exit 1
fi

pattern='^([[:blank:]]*image: ghcr\.io/nosovk/cli-proxy-api:)([A-Za-z0-9._-]+)([[:blank:]]*)$'
matches=0
old=''
lines=()
trailing_newline=false
if [[ -s $path ]] && [[ $(tail -c 1 "$path" | wc -l) -eq 1 ]]; then
  trailing_newline=true
fi

while IFS= read -r line || [[ -n $line ]]; do
  if [[ $line =~ $pattern ]]; then
    matches=$((matches + 1))
    old="${BASH_REMATCH[2]}"
    line="${BASH_REMATCH[1]}${NEW_TAG}${BASH_REMATCH[3]}"
  fi
  lines+=("$line")
done < "$path"

if [[ $matches -ne 1 ]]; then
  echo "Expected exactly one pinned image, found $matches" >&2
  exit 1
fi

changed=false
if [[ $old != "$NEW_TAG" ]]; then
  changed=true
  tmp=$(mktemp "${path}.tmp.XXXXXX")
  trap 'rm -f "${tmp:-}"' EXIT
  for i in "${!lines[@]}"; do
    if [[ $i -lt $((${#lines[@]} - 1)) ]] || [[ $trailing_newline == true ]]; then
      printf '%s\n' "${lines[$i]}" >> "$tmp"
    else
      printf '%s' "${lines[$i]}" >> "$tmp"
    fi
  done
  mv "$tmp" "$path"
  tmp=''
fi

{
  echo "old_tag=$old"
  echo "changed=$changed"
} >> "$GITHUB_OUTPUT"
