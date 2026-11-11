"""scripts/ratchet.py: a new finding is red, a fixed one asks for a lower
baseline, --update lowers only, --accept writes a new one; the tools are
what the repo is (laib#1170)."""
import importlib.util
import io
import subprocess
import tempfile
import unittest
from collections import Counter
from contextlib import redirect_stdout
from pathlib import Path

spec = importlib.util.spec_from_file_location("ratchet", Path(__file__).parent / "ratchet.py")
ratchet = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ratchet)


class Ratchet(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        ratchet.BASELINE = Path(self.dir.name)
        self.now = Counter()
        ratchet.TOOLS = {"t": lambda: Counter(self.now)}

    def tearDown(self):
        self.dir.cleanup()

    def main(self, *args):
        out = io.StringIO()
        with redirect_stdout(out):
            code = ratchet.main(list(args))
        return code, out.getvalue()

    def test_same_findings_are_green(self):
        self.now.update({"a.go: f": 1, "b.go: g": 40})
        self.assertEqual(self.main("--update", "--accept")[0], 0)
        self.assertEqual(self.main(), (0, "t: 2 findings (baseline 2)\n"))

    def test_a_new_finding_is_red(self):
        self.now["a.go: f"] = 1
        self.main("--update", "--accept")
        self.now["c.go: h"] = 1
        code, out = self.main()
        self.assertEqual(code, 1)
        self.assertIn("new or higher: 1 c.go: h (baseline 0)", out)

    def test_a_higher_number_is_red(self):
        self.now["a.go: f"] = 35
        self.main("--update", "--accept")
        self.now["a.go: f"] = 36
        self.assertEqual(self.main()[0], 1)

    def test_a_fixed_finding_asks_for_a_lower_baseline(self):
        self.now.update({"a.go: f": 1, "b.go: g": 1})
        self.main("--update", "--accept")
        del self.now["b.go: g"]
        code, out = self.main()
        self.assertEqual(code, 1)
        self.assertIn("lower the baseline in this PR", out)
        self.assertEqual(self.main("--update")[0], 0)
        self.assertEqual(self.main()[0], 0)

    def test_update_lowers_only(self):
        self.now.update({"a.go: f": 40, "b.go: g": 1})
        self.main("--update", "--accept")
        self.now.update({"a.go: f": 2, "c.go: new": 1})  # a.go goes up to 42
        del self.now["b.go: g"]
        code, out = self.main("--update")
        self.assertEqual(code, 1)
        self.assertIn("not written: 1 c.go: new", out)
        self.assertEqual(ratchet.read("t"), Counter({"a.go: f": 40}))

    def test_a_line_the_ratchet_cannot_read_is_an_error(self):
        with self.assertRaises(SystemExit):
            ratchet.lines_of("internal/x.go:1:1: something new\n", ratchet.DEAD, "deadcode")


class WhatTheRepoIs(unittest.TestCase):
    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = Path(self.dir.name)
        subprocess.run(["git", "init", "-q", str(self.root)], check=True)
        self.saved = ratchet.ROOT
        ratchet.ROOT = self.root

    def tearDown(self):
        ratchet.ROOT = self.saved
        self.dir.cleanup()

    def track(self, *files):
        for f in files:
            (self.root / f).parent.mkdir(parents=True, exist_ok=True)
            (self.root / f).write_text("")
        subprocess.run(["git", "add", *files], cwd=self.root, check=True)

    def test_the_outermost_go_modules(self):
        self.track("apps/a/go.mod", "apps/b/go.mod", "apps/b/testdata/x/go.mod", "apps/a/scaffold/c/go.mod")
        self.assertEqual(ratchet.modules(), [self.root / "apps/a", self.root / "apps/b"])
        self.track("go.mod")
        self.assertEqual(ratchet.modules(), [self.root])
        self.assertEqual(set(ratchet.found()), set(ratchet.GO))

    def test_a_ruff_toml_adds_the_python_tools(self):
        self.track("ruff.toml")
        self.assertEqual(set(ratchet.found()), set(ratchet.PYTHON))

    def test_nothing_to_check_is_an_error(self):
        self.track("README.md")
        with self.assertRaises(SystemExit):
            ratchet.found()

    def test_complexity_is_the_score_of_production_functions(self):
        saved = ratchet.ruff_findings
        ratchet.ruff_findings = lambda *a: [("ordnen/ordnen.py", "C901", "`main` is too complex (14 > 10)"),
                                            ("ordnen/tests/test_ordnen.py", "C901", "`test_x` is too complex (12 > 10)")]
        try:
            self.assertEqual(ratchet.complexity(), Counter({"ordnen/ordnen.py: main": 14}))
            ratchet.ruff_findings = lambda *a: [("a.py", "C901", "`f` has grown too complex")]
            with self.assertRaises(SystemExit):
                ratchet.complexity()
        finally:
            ratchet.ruff_findings = saved


if __name__ == "__main__":
    unittest.main()
