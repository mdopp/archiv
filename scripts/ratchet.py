#!/usr/bin/env python3
"""The ratchet: dead code, lint findings, duplicates and complexity only go
down (owner, 2026-11-03: "Code sollte immer möglichst sauber sein").

Each tool's findings of today are written down in scripts/ratchet/<tool>.txt,
one line per finding: a number, then the finding without its line number
(lines move with every edit, a finding does not). CI fails when

- a finding is new, or its number went up (a second copy of the same
  message in a file; a function's complexity score), and when
- a finding went away or its number went down: then the baseline is lower
  than written, and this PR lowers it — run `scripts/ratchet.py --update`
  and commit what it writes.

`--update` writes today's findings; it refuses to write a new or a higher
one unless `--accept` is given. Accepting is the exception, as raising a
line cap is: the reason goes into the PR, and the owner says yes.

One source (laib#1170): this file is laib's werkstatt/scaffold/repo/scripts/
ratchet.py, copied unchanged into every repo of the household;
scripts/copies.sh fails CI on a copy that differs. A change goes to laib
first, then to the copies.

Which tools run is what the repo is:

Go — every outermost tracked go.mod is a module (laib and homes have one at
the root; a `laib-app new` repo has one per apps/<name>/). The tools, pinned
(`go run <tool>@<version>`, nothing added to go.mod):

- deadcode, the whole program: every main package is a root. A function
  unreachable even from the tests is "dead"; one only the tests reach is
  "test-only" (production code nobody runs), except in a fake package
  (a directory ending in "fake"), which exists for the tests.
- staticcheck, its default checks plus U1000 (unused), without ST1005:
  the error strings here are German sentences shown to people, and a German
  sentence starts with a capital letter.
- dupl, production files, clones of DUPL_TOKENS syntax tokens or more.
- gocognit, production functions with a cognitive complexity over
  COGNIT_OVER (golangci-lint's default for the same check).

Python — a ruff.toml at the repo's root says the repo is ratcheted as
Python (it holds what ruff reads, such as the target version; the rules
and ruff's version are pinned here). ruff runs from a venv of its own
(RUFF_VENV, default ~/.cache/laib-ratchet/ruff-<version>):

- ruff, the rules RUFF_SELECT (ruff's defaults: syntax, undefined and
  unused names, plus bugbear's likely bugs), every file.
- complexity, production functions with a McCabe complexity over
  MCCABE_OVER (ruff's C901 at its default), the number is the score.

Production means: not a _test.go or test_*.py file, not testdata/ or tests/,
not a fake package. Neither a go.mod nor a ruff.toml: an error, never a
green run that checked nothing.
"""
import json
import os
import re
import subprocess
import sys
from collections import Counter
from pathlib import Path

ROOT = Path(__file__).resolve().parent.parent
BASELINE = ROOT / "scripts" / "ratchet"

DEADCODE = "golang.org/x/tools/cmd/deadcode@v0.52.0"
STATICCHECK = "honnef.co/go/tools/cmd/staticcheck@v0.8.2"
DUPL = "github.com/mibk/dupl@v1.1.0"
GOCOGNIT = "github.com/uudashr/gocognit/cmd/gocognit@v1.3.0"
CHECKS = "inherit,U1000,-ST1005"
DUPL_TOKENS = 75
COGNIT_OVER = 30
RUFF = "0.17.3"
RUFF_SELECT = "E4,E7,E9,F,B"
MCCABE_OVER = 10


def run(args, ok=(0,), stdin=None, cwd=None):
    p = subprocess.run(args, cwd=cwd or ROOT, input=stdin, capture_output=True, text=True)
    if p.returncode not in ok:
        sys.exit(f"{' '.join(map(str, args[:3]))} failed ({p.returncode}):\n{p.stderr}{p.stdout}")
    return p.stdout


def lines_of(out, pattern, tool):
    """Every line matches the tool's format, or the tool changed and the
    ratchet would count nothing: that is an error, never zero findings."""
    found = []
    for line in out.splitlines():
        if not line.strip():
            continue
        m = re.match(pattern, line)
        if not m:
            sys.exit(f"{tool}: a line the ratchet cannot read: {line}")
        found.append(m.groups())
    return found


def modules():
    """The outermost Go modules: a go.mod inside another module's tree is
    the scaffold's or a testdata's, and `./...` of the outer one skips it."""
    dirs = sorted({Path(f).parent for f in run(["git", "ls-files", "go.mod", "*/go.mod"]).split()
                   if "testdata" not in Path(f).parts}, key=lambda d: (len(d.parts), str(d)))
    outer = []
    for d in dirs:
        if not any(o == Path(".") or o in d.parents for o in outer):
            outer.append(d)
    return [ROOT / d for d in outer]


def rel(mod, path):
    """A path a tool printed in mod, as the repo names it."""
    return os.path.relpath(os.path.join(mod, path), ROOT)


DEAD = r"^(\S+?):\d+:\d+: unreachable func: (\S+)$"


def fake(path):
    return any(part.endswith("fake") for part in Path(path).parent.parts)


def testfile(path):
    p = Path(path)
    return (p.name.endswith("_test.go") or p.name.startswith("test_") or p.name.endswith("_test.py")
            or "testdata" in p.parts or "tests" in p.parts or fake(path))


def deadcode():
    found = Counter()
    for mod in modules():
        def find(*args):
            return {f"{rel(mod, f)}: {fn}" for f, fn in lines_of(run(["go", "run", DEADCODE, *args, "./..."], cwd=mod), DEAD, "deadcode")}
        dead, unrun = find("-test"), find()
        found.update(f"dead {k}" for k in dead)
        for k in unrun - dead:
            if not fake(k.split(":")[0]):
                found[f"test-only {k}"] += 1
    return found


def staticcheck():
    found = Counter()
    for mod in modules():
        out = run(["go", "run", STATICCHECK, "-checks", CHECKS, "-f", "json", "./..."], ok=(0, 1), cwd=mod)
        for line in out.splitlines():
            d = json.loads(line)
            if d["code"] == "compile":
                sys.exit(f"staticcheck: does not compile: {d['message']}")
            found[f"{rel(mod, d['location']['file'])}: {d['message']} ({d['code']})"] += 1
    return found


DUP = r"^(.+?):(\d+)-\d+: duplicate of (.+?):(\d+)-\d+$"


def dupl():
    found = Counter()
    for mod in modules():
        files = [f for f in run(["git", "ls-files", "*.go"], cwd=mod).split() if not testfile(f)]
        out = run(["go", "run", DUPL, "-files", "-plumbing", "-t", str(DUPL_TOKENS)], stdin="\n".join(files), cwd=mod)
        for a, la, b, lb in lines_of(out, DUP, "dupl"):
            if (a, int(la)) < (b, int(lb)):  # each pair is printed both ways
                found[f"{rel(mod, a)} ~ {rel(mod, b)}"] += 1
    return found


def gocognit():
    found = Counter()
    for mod in modules():
        out = run(["go", "run", GOCOGNIT, "-over", str(COGNIT_OVER), "-ignore", r"_test\.go$|testdata/|fake/",
                   "-f", "{{.Complexity}} {{.Pos.Filename}} {{.PkgName}}.{{.FuncName}}", "."], ok=(0, 1), cwd=mod)
        found.update({f"{rel(mod, f)}: {fn}": int(n) for n, f, fn in lines_of(out, r"^(\d+) (\S+) (\S+)$", "gocognit")})
    return found


def ruff_exe():
    venv = Path(os.environ.get("RUFF_VENV") or Path.home() / ".cache" / "laib-ratchet" / f"ruff-{RUFF}")
    exe = venv / "bin" / "ruff"
    if not exe.exists():
        run([sys.executable, "-m", "venv", str(venv)])
        run([str(venv / "bin" / "pip"), "install", "-q", f"ruff=={RUFF}"])
    if run([str(exe), "--version"]).split()[-1] != RUFF:
        sys.exit(f"ruff: {exe} is not {RUFF}; remove {venv}")
    return str(exe)


def ruff_findings(*args):
    out = run([ruff_exe(), "check", "--no-fix", "--exit-zero", "--output-format", "json", *args, "."])
    found = []
    for d in json.loads(out):
        if not d.get("code"):
            sys.exit(f"ruff: cannot read {d['filename']}: {d['message']}")
        found.append((os.path.relpath(d["filename"], ROOT), d["code"], d["message"]))
    return found


def ruff():
    return Counter(f"{f}: {msg} ({code})" for f, code, msg in ruff_findings("--select", RUFF_SELECT))


TOO_COMPLEX = r"^`(.+)` is too complex \((\d+) > \d+\)$"


def complexity():
    found = Counter()
    for f, _, msg in ruff_findings("--select", "C901", "--config", f"lint.mccabe.max-complexity = {MCCABE_OVER}"):
        if testfile(f):
            continue
        (fn, n), = lines_of(msg, TOO_COMPLEX, "complexity")
        found[f"{f}: {fn}"] = max(found[f"{f}: {fn}"], int(n))
    return found


GO = {"deadcode": deadcode, "staticcheck": staticcheck, "dupl": dupl, "gocognit": gocognit}
PYTHON = {"ruff": ruff, "complexity": complexity}
TOOLS = None  # what the repo is (found()); the tests set their own
UNIT = {"gocognit": "functions over %d, the number is the score" % COGNIT_OVER,
        "complexity": "functions over %d, the number is the McCabe score" % MCCABE_OVER,
        "dupl": "file pairs, the number is the cloned fragments of %d tokens or more" % DUPL_TOKENS}


def found():
    tools = {}
    if modules():
        tools.update(GO)
    if (ROOT / "ruff.toml").exists():
        tools.update(PYTHON)
    if not tools:
        sys.exit("ratchet: neither a go.mod nor a ruff.toml at the root, nothing to check")
    return tools


def read(tool):
    base = Counter()
    p = BASELINE / f"{tool}.txt"
    if p.exists():
        for line in p.read_text().splitlines():
            if line and not line.startswith("#"):
                n, key = line.split(" ", 1)
                base[key] = int(n)
    return base


def write(tool, now):
    head = [f"# scripts/ratchet.py {tool}: {UNIT.get(tool, 'the number is how often')}",
            "# written by `scripts/ratchet.py --update`; lowered in the PR that fixes one, raised only with --accept",
            f"# total {len(now)}"]
    body = [f"{now[k]} {k}" for k in sorted(now)]
    BASELINE.mkdir(parents=True, exist_ok=True)
    (BASELINE / f"{tool}.txt").write_text("\n".join(head + body) + "\n")


def compare(base, now):
    """Return (worse, better): findings new or above their baseline, and
    findings gone or below it."""
    worse = sorted(k for k in now if now[k] > base.get(k, 0))
    better = sorted(k for k in base if now.get(k, 0) < base[k])
    return worse, better


def main(argv):
    update, accept = "--update" in argv, "--accept" in argv
    tools = TOOLS if TOOLS is not None else found()
    pick = [a for a in argv if not a.startswith("--")] or list(tools)
    unknown = [t for t in pick if t not in tools]
    if unknown:
        sys.exit(f"ratchet: no tool {', '.join(unknown)} for this repo; it has {', '.join(tools)}")
    bad = 0
    for tool in pick:
        now, base = tools[tool](), read(tool)
        worse, better = compare(base, now)
        print(f"{tool}: {len(now)} findings (baseline {len(base)})")
        if update:
            if worse and not accept:
                for k in worse:
                    print(f"  new or higher, not written: {now[k]} {k} (baseline {base.get(k, 0)})")
                print("  --update lowers only; --accept writes these too: the reason in the PR, the owner's yes")
                now = Counter({k: min(n, base[k]) for k, n in now.items() if k in base})
                bad = 1
            write(tool, now)
            continue
        for k in worse:
            print(f"::error title=ratchet {tool}::new or higher: {now[k]} {k} (baseline {base.get(k, 0)})")
        if worse:
            print(f"  fix it; if it must stay: scripts/ratchet.py --update --accept {tool}, the reason in the PR")
        if better:
            for k in better:
                print(f"  lower: {k} is {now.get(k, 0)}, the baseline says {base[k]}")
            print(f"::error title=ratchet {tool}::{len(better)} findings went down: lower the baseline in this PR: scripts/ratchet.py --update {tool}")
        bad |= bool(worse or better)
    return bad


if __name__ == "__main__":
    sys.exit(main(sys.argv[1:]))
