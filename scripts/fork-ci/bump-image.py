"""Change exactly one pinned CLIProxyAPI image; fail closed on config drift."""
import os
import re
from pathlib import Path

path = Path("docker-compose.yml")
text = path.read_text()
tag = os.environ["NEW_TAG"]
if not re.fullmatch(r"[0-9a-f]{7,40}", tag):
    raise SystemExit("NEW_TAG must be a commit hash")
pattern = re.compile(r"^(\s*image: ghcr\.io/nosovk/cli-proxy-api:)([A-Za-z0-9._-]+)([ \t]*)$", re.M)
matches = list(pattern.finditer(text))
if len(matches) != 1:
    raise SystemExit(f"Expected exactly one pinned image, found {len(matches)}")
old = matches[0][2]
if old != tag:
    path.write_text(pattern.sub(lambda m: m[1] + tag + m[3], text))
with open(os.environ["GITHUB_OUTPUT"], "a") as output:
    output.write(f"old_tag={old}\nchanged={str(old != tag).lower()}\n")
