#!/usr/bin/env python3
"""Abläufe: every behaviour scenario has a test that names it (owner,
2026-11-12: "jedes Programm bekommt nummerierte Abläufe").

docs/ABLAEUFE.md holds the program's scenarios in plain German, each a
heading that starts with its id (`### L-12 Titel`, one prefix per repo),
then a few Wenn/Dann lines. A test names the scenario it proves with a
marker in a comment or in its name:

    // Ablauf L-12          # Ablauf L-12, L-13          test("ablauf:L-12 …")

The word, then a blank or a colon, then one id or several joined by commas.
CI fails on

- a scenario no test names, unless its id is in scripts/ablaeufe-baseline.txt;
- a baselined id that a test names by now, or that is no scenario any more:
  take it out of the baseline in the same PR;
- a baseline that grew against the merge base with origin/main (ABLAEUFE_BASE
  names another ref): the baseline only shrinks, a new scenario comes with
  its test. Where the base has no baseline yet, this one is the first;
- a test that names an id docs/ABLAEUFE.md does not have, and an id twice.

The tests are the tracked and untracked (not ignored) files whose path
matches a pattern of DEFAULT_TESTS, or of scripts/ablaeufe-tests.txt (one
pattern per line, replacing the defaults) when the repo has it. A pattern
without a slash matches the file name, one with a slash the whole path
(fnmatch, where * crosses slashes).

One source (laib#1170): laib's werkstatt/scaffold/repo/scripts/ablaeufe.py,
copied unchanged into every repo of the household (scripts/copies.sh).
Usage: scripts/ablaeufe.py [--list]
"""
import fnmatch
import os
import re
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
DOC = "docs/ABLAEUFE.md"
BASELINE = "scripts/ablaeufe-baseline.txt"
TESTS = "scripts/ablaeufe-tests.txt"
DEFAULT_TESTS = [
    "*_test.go",
    "test_*.py",
    "*_test.py",
    "*.spec.ts",
    "*.spec.js",
    "*.spec.tsx",
    "*.test.ts",
    "*.test.js",
    "*.test.tsx",
    "*/e2e/*.ts",
    "*/e2e/*.js",
    "*/src/test/*.kt",
    "*/src/androidTest/*.kt",
]
ID = r"[A-Z][A-Z0-9]{0,4}-[1-9][0-9]{0,3}"
HEADING = re.compile(r"^#{2,6}\s+(" + ID + r")(?![\w-])\s*(.*)$")
MARKER = re.compile(r"(?<![A-Za-z])[Aa]blauf[ :]\s*(" + ID + r"(?:\s*,\s*" + ID + r")*)(?![\w-])")


def scenarios(text):
    """The ids of the doc's scenario headings, in order (fenced code skipped),
    and the problems with them: an id twice, a heading without a title."""
    ids, problems, fenced = [], [], False
    for n, line in enumerate(text.splitlines(), 1):
        if line.lstrip().startswith("```"):
            fenced = not fenced
            continue
        m = None if fenced else HEADING.match(line)
        if not m:
            continue
        if m.group(1) in ids:
            problems.append(f"{DOC}:{n}: {m.group(1)} is there twice: give the second one the next free number")
        elif not m.group(2).strip():
            problems.append(f"{DOC}:{n}: {m.group(1)} has no title: one sentence after the id")
        ids.append(m.group(1))
    return ids, problems


def markers(text):
    """Every id a file names with the marker."""
    found = set()
    for m in MARKER.finditer(text):
        found.update(re.findall(ID, m.group(1)))
    return found


def baseline_ids(text):
    """The baseline's ids, one per line, # starts a comment; and the ones twice."""
    ids, twice = [], []
    for line in text.splitlines():
        word = line.split("#", 1)[0].strip()
        if word:
            (twice if word in ids else ids).append(word)
    return ids, twice


def git(*args):
    return subprocess.run(["git", "-C", str(ROOT), *args], capture_output=True, text=True)


def test_files(patterns):
    """The tracked and untracked, not ignored, files the patterns match."""
    out = git("ls-files", "--cached", "--others", "--exclude-standard", "-z")
    if out.returncode != 0:
        raise SystemExit(f"ablaeufe: git ls-files failed: {out.stderr.strip()}")
    files = []
    for path in sorted(set(filter(None, out.stdout.split("\0")))):
        name = path.rsplit("/", 1)[-1]
        if any(fnmatch.fnmatchcase(path if "/" in p else name, p) for p in patterns):
            files.append(path)
    return files


def named(files):
    """id -> the files that name it."""
    where = {}
    for path in files:
        try:
            text = (ROOT / path).read_text(encoding="utf-8", errors="replace")
        except OSError:
            continue
        for i in markers(text):
            where.setdefault(i, []).append(path)
    return where


def base_baseline():
    """The baseline at the merge base: its ids, None when the base has no
    baseline file, or an error string when the base cannot be read."""
    ref = os.environ.get("ABLAEUFE_BASE", "origin/main")
    base = git("merge-base", "HEAD", ref)
    if base.returncode != 0:
        return f"no merge base with {ref} (fetch it, or set ABLAEUFE_BASE): the baseline's shrink-only check cannot run"
    shown = git("show", f"{base.stdout.strip()}:{BASELINE}")
    if shown.returncode != 0:
        return None
    return baseline_ids(shown.stdout)[0]


def read(rel):
    p = ROOT / rel
    return p.read_text(encoding="utf-8") if p.is_file() else None


def check(doc, base_text, where, before):
    """The problems, given the doc, the baseline's text, the ids the tests
    name (id -> files) and the baseline at the merge base (ids, None, or an
    error string)."""
    ids, problems = scenarios(doc)
    base, twice = baseline_ids(base_text or "")
    problems += [f"{BASELINE}: {i} is there twice" for i in twice]
    problems += [
        f"{i} has no test: write the test that proves it, with `Ablauf {i}` in a comment or its name"
        for i in ids
        if i not in where and i not in base
    ]
    problems += [
        f"{', '.join(where[i])}: names {i}, which {DOC} does not have: fix the id or write the scenario"
        for i in sorted(set(where) - set(ids))
    ]
    return ids, base, problems + shrinks(ids, base, where, before)


def shrinks(ids, base, where, before):
    """What keeps the baseline shrinking: no id in it that is covered or gone,
    none that the merge base did not have."""
    problems = []
    for i in base:
        if i not in ids:
            problems.append(f"{BASELINE}: {i} is no scenario in {DOC}: take it out of the baseline")
        elif i in where:
            problems.append(f"{BASELINE}: {i} has a test now ({', '.join(where[i])}): take it out of the baseline")
    if isinstance(before, str):
        problems.append(before)
    elif before is not None:
        problems += [f"{BASELINE}: {i} is new in the baseline: it only shrinks; write the test instead" for i in base if i not in before]
    return problems


def main(argv):
    doc = read(DOC)
    if doc is None:
        print(f"ablaeufe: {DOC} is missing: every program writes its scenarios there")
        return 1
    patterns = DEFAULT_TESTS
    own = read(TESTS)
    if own is not None:
        patterns = [p.strip() for p in own.splitlines() if p.strip() and not p.lstrip().startswith("#")]
    where = named(test_files(patterns))
    ids, base, problems = check(doc, read(BASELINE), where, base_baseline())
    if "--list" in argv:
        for i in ids:
            print(f"{i}  {', '.join(where.get(i, [])) or ('baselined' if i in base else 'NO TEST')}")
    covered = sum(1 for i in ids if i in where)
    for p in problems:
        print(f"::error title=ablaeufe::{p}" if os.environ.get("GITHUB_ACTIONS") else f"  {p}")
    print(f"ablaeufe: {len(ids)} scenarios, {covered} named by a test, {len(base)} baselined, {len(problems)} problems")
    return 1 if problems else 0


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
