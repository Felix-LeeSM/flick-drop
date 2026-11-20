#!/usr/bin/env bash
set -euo pipefail

if ! command -v python3 >/dev/null 2>&1; then
  echo "agent-docs: python3 is required" >&2
  exit 1
fi

python3 -B - <<'PY'
from __future__ import annotations

import os
import re
import subprocess
import sys
import urllib.parse
from collections import Counter, defaultdict
from pathlib import Path

ROOT = Path.cwd().resolve()

ROOT_AGENTS_LIMIT = 10 * 1024
SUBDIR_AGENTS_LIMIT = 8 * 1024
CHAIN_AGENTS_LIMIT = 24 * 1024
DOC_LIMIT = 32 * 1024

RESERVED = {"index.md", "log.md"}
ALLOWED_TYPES = {
    "Agent Guide",
    "Architecture",
    "Contract",
    "Runbook",
    "Roadmap",
    "Reference",
    "Work Guide",
    "Work Note",
    "Ops Note",
    "Memory",
}
ALLOWED_STATUS = {"draft", "stable", "deprecated"}

EXEMPT = (
    "README.md",
    ".github/pull_request_template.md",
    ".agents/skills/*/SKILL.md",
    "docs/work/done/**",
    "web/**",
    "!web/AGENTS.md",
    "!web/src/lib/i18n/README.md",
)

LIST_ITEM = re.compile(
    r"^\* \[(?P<title>(?:[^\[\]]|\[[^\[\]]*\])+)\]\((?P<link>[^)\s]+)\)(?: - (?P<desc>.+))?$"
)
DATE_HEADING = re.compile(r"^## (\S.*)$")
ISO_DATE = re.compile(r"^\d{4}-\d{2}-\d{2}$")
LINK = re.compile(
    r"(?<!!)\[(?:[^\]\[]|\[[^\]]*\])*\]\(\s*<?([^)\s>]+)>?(?:\s+\"[^\"]*\")?\s*\)"
)
FENCE = re.compile(r"(^|\n)(```|~~~)[^\n]*\n.*?\n\2[^\n]*", re.S)
INLINE_CODE = re.compile(r"`[^`\n]*`")
HEADING = re.compile(r"^#{1,6}\s+(.*?)\s*#*\s*$", re.M)


def glob_regex(pattern: str) -> re.Pattern[str]:
    out, i = [], 0
    while i < len(pattern):
        if pattern.startswith("**/", i):
            out.append("(?:[^/]+/)*")
            i += 3
        elif pattern.startswith("**", i):
            out.append(".*")
            i += 2
        elif pattern[i] == "*":
            out.append("[^/]*")
            i += 1
        elif pattern[i] == "?":
            out.append("[^/]")
            i += 1
        else:
            out.append(re.escape(pattern[i]))
            i += 1
    return re.compile("".join(out) + r"\Z")


def is_exempt(rel: str) -> bool:
    exempt = False
    for pattern in EXEMPT:
        if pattern.startswith("!"):
            if glob_regex(pattern[1:]).match(rel):
                exempt = False
        elif glob_regex(pattern).match(rel):
            exempt = True
    return exempt


def tracked_markdown() -> list[Path]:
    out = subprocess.run(
        ["git", "ls-files", "-z", "--cached", "--others", "--exclude-standard", "--", "*.md"],
        cwd=ROOT,
        capture_output=True,
        check=True,
    ).stdout.decode()
    paths = set()
    for name in out.split("\0"):
        if not name:
            continue
        path = ROOT / name
        if path.is_symlink() or not path.is_file():
            continue
        paths.add(path.resolve())
    return sorted(paths)


def split_frontmatter(text: str) -> tuple[str | None, str]:
    if not text.startswith("---\n"):
        return None, text
    end = text.find("\n---\n", 3)
    if end == -1:
        if text.endswith("\n---"):
            end = len(text) - 4
        else:
            return None, text
    return text[4 : end + 1], text[end + 5 :]


def parse_simple_yaml(raw: str) -> dict[str, str]:
    data: dict[str, str] = {}
    for line in raw.splitlines():
        stripped = line.strip()
        if not stripped or stripped.startswith("#"):
            continue
        if ":" not in line:
            raise ValueError(f"invalid frontmatter line: {line!r}")
        key, val = line.split(":", 1)
        key = key.strip()
        val = val.strip()
        if (val.startswith('"') and val.endswith('"')) or (
            val.startswith("'") and val.endswith("'")
        ):
            val = val[1:-1]
        data[key] = val
    return data


def strip_code(text: str) -> str:
    return INLINE_CODE.sub("", FENCE.sub("\n", text))


def resolve_link(source: Path, target: str) -> Path | None:
    target = urllib.parse.unquote(target)
    if not target:
        return source
    if re.match(r"^[a-zA-Z][a-zA-Z0-9+.-]*:", target):
        return None
    if target.startswith("/"):
        return (ROOT / target.lstrip("/")).resolve()
    return (source.parent / target).resolve()


_listing_cache: dict[Path, set[str]] = {}


def exists_exact(path: Path) -> bool:
    path = path.resolve()
    try:
        parts = path.relative_to(ROOT).parts
        base = ROOT
    except ValueError:
        parts = (path.name,)
        base = path.parent
    for part in parts:
        if base not in _listing_cache:
            _listing_cache[base] = set(os.listdir(base))
        if part not in _listing_cache[base]:
            return False
        base = base / part
    return True


_slug_cache: dict[Path, set[str]] = {}


def heading_anchors(path: Path) -> set[str]:
    if path in _slug_cache:
        return _slug_cache[path]
    text = FENCE.sub("\n", path.read_text(encoding="utf-8", errors="replace"))
    _, text = split_frontmatter(text)
    seen: Counter[str] = Counter()
    result: set[str] = set()
    for match in HEADING.finditer(text):
        heading = re.sub(r"\[([^\]]*)\]\([^)]*\)", r"\1", match.group(1))
        heading = (
            re.sub(r"<[^>]+>", "", heading)
            .replace("`", "")
            .replace("**", "")
            .replace("__", "")
        )
        slug = re.sub(r"[^\w\- ]", "", heading.strip().lower()).replace(" ", "-")
        result.add(slug if seen[slug] == 0 else f"{slug}-{seen[slug]}")
        seen[slug] += 1
    result.update(a.lower() for a in re.findall(r'<a\s+(?:name|id)="([^"]+)"', text))
    _slug_cache[path] = result
    return result


def owner_dir(path: Path, index_dirs: set[Path]) -> Path | None:
    if path.name == "index.md":
        if path.parent == ROOT:
            return None
        directory = path.parent.parent
    else:
        directory = path.parent
    while True:
        if directory in index_dirs:
            return directory
        if directory == ROOT:
            return None
        directory = directory.parent


problems: list[str] = []


def fail(path: str, msg: str) -> None:
    problems.append(f"agent-docs: {path}: {msg}")


files = tracked_markdown()
concepts: set[Path] = set()
agents_sizes: dict[Path, int] = {}

for path in files:
    rel = path.relative_to(ROOT).as_posix()
    if path.name.lower() in RESERVED and path.name not in RESERVED:
        fail(rel, "case collision with reserved filename index.md/log.md")
        continue
    if is_exempt(rel):
        continue

    size = path.stat().st_size
    if path.name == "AGENTS.md":
        agents_sizes[path.parent] = size
        limit = ROOT_AGENTS_LIMIT if path.parent == ROOT else SUBDIR_AGENTS_LIMIT
        if size > limit:
            fail(rel, f"size {size} B exceeds limit {limit} B")
    elif rel.startswith("docs/") and path.name not in RESERVED:
        if size > DOC_LIMIT:
            fail(rel, f"size {size} B exceeds limit {DOC_LIMIT} B")

    if path.name in RESERVED:
        continue

    concepts.add(path)
    text = path.read_text(encoding="utf-8")
    raw, _ = split_frontmatter(text)
    if raw is None:
        fail(rel, "missing OKF frontmatter")
        continue
    try:
        fm = parse_simple_yaml(raw)
    except ValueError as err:
        fail(rel, str(err))
        continue
    for key in ("type", "title", "description"):
        if not fm.get(key):
            fail(rel, f"frontmatter missing required key `{key}`")
    doc_type = fm.get("type", "")
    if doc_type and doc_type not in ALLOWED_TYPES:
        fail(rel, f"unknown frontmatter type {doc_type!r}")
    status = fm.get("status", "")
    if status and status not in ALLOWED_STATUS:
        fail(rel, f"invalid frontmatter status {status!r}")

for directory in sorted(agents_sizes):
    total = 0
    cur = directory
    while True:
        total += agents_sizes.get(cur, 0)
        if cur == ROOT:
            break
        cur = cur.parent
    if total > CHAIN_AGENTS_LIMIT:
        rel_dir = "." if directory == ROOT else directory.relative_to(ROOT).as_posix()
        fail(f"{rel_dir}/AGENTS.md", f"AGENTS.md chain sum {total} B exceeds limit {CHAIN_AGENTS_LIMIT} B")

indexes = [
    p
    for p in files
    if p.name == "index.md" and not is_exempt(p.relative_to(ROOT).as_posix())
]
index_dirs = {p.parent for p in indexes}
required: dict[Path, set[Path]] = defaultdict(set)
for item in [*concepts, *indexes]:
    owner = owner_dir(item, index_dirs)
    if owner is not None:
        required[owner].add(item)

if not (ROOT / "index.md").is_file():
    fail("index.md", "missing root index.md")

for path in files:
    rel = path.relative_to(ROOT).as_posix()
    if is_exempt(rel):
        continue
    text = path.read_text(encoding="utf-8")
    if path.name == "index.md":
        raw, body = split_frontmatter(text)
        if path.parent == ROOT:
            if raw is None:
                fail(rel, "root index.md must include okf_version frontmatter")
            else:
                try:
                    fm = parse_simple_yaml(raw)
                except ValueError as err:
                    fail(rel, str(err))
                else:
                    if set(fm) != {"okf_version"} or fm.get("okf_version") != "0.2":
                        fail(rel, 'root index.md frontmatter must contain only okf_version: "0.2"')
        elif raw is not None:
            fail(rel, "non-root index.md must not have frontmatter")

        linked: set[Path] = set()
        for lineno, line in enumerate(body.splitlines(), 1):
            stripped = line.strip()
            if not stripped or stripped.startswith("#") or stripped.startswith(">"):
                continue
            m = LIST_ITEM.match(stripped)
            if not m:
                if stripped.startswith(("* ", "- ")):
                    fail(rel, f"line {lineno}: expected '* [Title](path) - description'")
                continue
            target = resolve_link(path, m.group("link").split("#", 1)[0])
            if target is None or not target.exists():
                fail(rel, f"line {lineno}: broken index target {m.group('link')}")
                continue
            linked.add(target.resolve())
            if target.is_dir():
                linked.add((target / "index.md").resolve())

        for req in sorted(required[path.parent]):
            if req.resolve() not in linked:
                fail(rel, f"missing entry for {req.relative_to(path.parent).as_posix()}")

    elif path.name == "log.md":
        _, body = split_frontmatter(text)
        prev = None
        for lineno, line in enumerate(strip_code(body).splitlines(), 1):
            m = DATE_HEADING.match(line)
            if not m:
                continue
            val = m.group(1).strip()
            if not ISO_DATE.match(val):
                fail(rel, f"line {lineno}: expected '## YYYY-MM-DD' heading, got {val!r}")
                continue
            if prev is not None and val > prev:
                fail(rel, f"line {lineno}: dates must be reverse chronological ({val} > {prev})")
            prev = val

for source in files:
    rel = source.relative_to(ROOT).as_posix()
    if is_exempt(rel):
        continue
    text = strip_code(source.read_text(encoding="utf-8", errors="replace"))
    for match in LINK.finditer(text):
        raw_link = match.group(1)
        path_part, _, anchor = raw_link.partition("#")
        target = resolve_link(source, path_part)
        if target is None:
            continue
        if not target.exists():
            fail(rel, f"broken link: {raw_link}")
            continue
        if not exists_exact(target):
            fail(rel, f"link case mismatch: {raw_link}")
            continue
        if anchor and target.suffix == ".md" and target.is_file():
            if urllib.parse.unquote(anchor).lower() not in heading_anchors(target):
                fail(rel, f"missing anchor: {raw_link}")

if problems:
    for p in problems:
        print(p, file=sys.stderr)
    sys.exit(1)

print("agent-docs: ok")
PY
