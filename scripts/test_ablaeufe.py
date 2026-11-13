"""scripts/ablaeufe.py: a scenario without a test is red unless baselined,
the baseline only shrinks, a test may name only a scenario that exists.

The marker word is built at run time (M), so this file names no scenario
itself: it is a test file the checker reads too."""
import importlib.util
import io
import subprocess
import tempfile
import unittest
from contextlib import redirect_stdout
from pathlib import Path

spec = importlib.util.spec_from_file_location("ablaeufe", Path(__file__).parent / "ablaeufe.py")
ablaeufe = importlib.util.module_from_spec(spec)
spec.loader.exec_module(ablaeufe)

M = "Abl" + "auf"
DOC = "# Abläufe\n\n```\n### X-9 Beispiel im Format\n```\n\n## Inbox\n\n### T-1 Eine Karte erscheint\nWenn …\nDann …\n\n### T-2 Freigeben\nDann …\n"


class Parse(unittest.TestCase):
    def test_headings_are_the_scenarios_and_fenced_examples_are_not(self):
        self.assertEqual(ablaeufe.scenarios(DOC), (["T-1", "T-2"], []))

    def test_an_id_twice_and_one_without_a_title(self):
        ids, problems = ablaeufe.scenarios("### T-1 a\n### T-1 b\n### T-2\n")
        self.assertEqual(ids, ["T-1", "T-1", "T-2"])
        self.assertIn("T-1 is there twice", problems[0])
        self.assertIn("T-2 has no title", problems[1])

    def test_the_markers(self):
        text = f"// {M} T-1\n# {M} T-2, T-3\ntest('{M.lower()}:T-4 shows', …)\n// {M}: T-5\n"
        self.assertEqual(ablaeufe.markers(text), {"T-1", "T-2", "T-3", "T-4", "T-5"})

    def test_prose_is_no_marker(self):
        for text in [f"kein {M} „Homes“", f"Der {M} T-1x", f"Ab{M.lower()} T-1", f"{M} T-0", f"{M} t-1"]:
            self.assertEqual(ablaeufe.markers(text), set(), text)

    def test_the_baseline_takes_comments(self):
        self.assertEqual(ablaeufe.baseline_ids("# why\nT-1  # a VM proof\n\nT-2\nT-1\n"), (["T-1", "T-2"], ["T-1"]))


class Check(unittest.TestCase):
    def check(self, baseline="", where=None, before=None):
        return ablaeufe.check(DOC, baseline, where if where is not None else {"T-1": ["a_test.go"], "T-2": ["b_test.go"]}, before)[2]

    def test_every_scenario_named_is_green(self):
        self.assertEqual(self.check(), [])

    def test_a_scenario_without_a_test_is_red(self):
        problems = self.check(where={"T-1": ["a_test.go"]})
        self.assertEqual(len(problems), 1)
        self.assertIn("T-2 has no test", problems[0])

    def test_a_baselined_scenario_without_a_test_is_green(self):
        self.assertEqual(self.check("T-2\n", {"T-1": ["a_test.go"]}, ["T-2"]), [])

    def test_a_baselined_scenario_with_a_test_must_leave(self):
        problems = self.check("T-2\n", before=["T-2"])
        self.assertEqual(len(problems), 1)
        self.assertIn("T-2 has a test now (b_test.go): take it out", problems[0])

    def test_a_baselined_id_that_is_no_scenario_must_leave(self):
        self.assertIn("T-7 is no scenario", self.check("T-7\n", before=["T-7"])[0])

    def test_the_baseline_only_shrinks(self):
        problems = self.check("T-2\n", {"T-1": ["a_test.go"]}, ["T-1"])
        self.assertEqual(len(problems), 1)
        self.assertIn("T-2 is new in the baseline", problems[0])

    def test_the_first_baseline_is_taken_as_it_is(self):
        self.assertEqual(self.check("T-2\n", {"T-1": ["a_test.go"]}, None), [])

    def test_no_base_is_red_not_skipped(self):
        self.assertEqual(self.check(before="no merge base with origin/main"), ["no merge base with origin/main"])

    def test_a_test_naming_an_unknown_id_is_red(self):
        problems = self.check(where={"T-1": ["a_test.go"], "T-2": ["b_test.go"], "T-3": ["c_test.go"]})
        self.assertEqual(len(problems), 1)
        self.assertIn("c_test.go: names T-3", problems[0])


class Repo(unittest.TestCase):
    """The whole run in a git repo: the files the patterns pick, the merge base."""

    def setUp(self):
        self.dir = tempfile.TemporaryDirectory()
        self.root = Path(self.dir.name)
        ablaeufe.ROOT = self.root
        self.git("init", "-q", "-b", "main")
        self.write("docs/ABLAEUFE.md", DOC)
        self.write("x/a_test.go", f"// {M} T-1\n")
        self.write("scripts/ablaeufe-baseline.txt", "T-2\n")
        self.write("x/notes.go", f"// {M} T-2 is not a test file\n")
        self.git("add", ".")
        self.git("-c", "user.name=t", "-c", "user.email=t@t", "commit", "-qm", "base")
        self.git("update-ref", "refs/remotes/origin/main", "HEAD")

    def tearDown(self):
        self.dir.cleanup()

    def git(self, *args):
        subprocess.run(["git", "-C", str(self.root), *args], check=True, capture_output=True)

    def write(self, rel, text):
        (self.root / rel).parent.mkdir(parents=True, exist_ok=True)
        (self.root / rel).write_text(text)

    def main(self, *args):
        out = io.StringIO()
        with redirect_stdout(out):
            code = ablaeufe.main(list(args))
        return code, out.getvalue()

    def test_green_with_the_baseline(self):
        code, out = self.main("--list")
        self.assertEqual(code, 0, out)
        self.assertIn("T-1  x/a_test.go\nT-2  baselined\n", out)
        self.assertIn("2 scenarios, 1 named by a test, 1 baselined, 0 problems", out)

    def test_an_untracked_test_counts_and_the_baseline_must_shrink(self):
        self.write("web/e2e/freigeben.spec.ts", f"test('{M.lower()}:T-2 freigeben', () => {{}})\n")
        code, out = self.main()
        self.assertEqual(code, 1)
        self.assertIn("T-2 has a test now (web/e2e/freigeben.spec.ts)", out)

    def test_the_repos_own_patterns_replace_the_defaults(self):
        self.write("scripts/ablaeufe-tests.txt", "# only Kotlin\n*/src/test/*.kt\n")
        self.write("app/src/test/java/FreigabeTest.kt", f"// {M} T-1\n")
        self.write("x/a_test.go", "// nothing\n")
        self.assertEqual(self.main()[0], 0)
        (self.root / "app/src/test/java/FreigabeTest.kt").unlink()
        self.assertIn("T-1 has no test", self.main()[1])

    def test_a_grown_baseline_against_the_merge_base_is_red(self):
        self.write("docs/ABLAEUFE.md", DOC + "\n### T-3 Neu\nDann …\n")
        self.write("scripts/ablaeufe-baseline.txt", "T-2\nT-3\n")
        code, out = self.main()
        self.assertEqual(code, 1)
        self.assertIn("T-3 is new in the baseline", out)

    def test_without_the_doc_it_is_red(self):
        (self.root / "docs/ABLAEUFE.md").unlink()
        self.assertEqual(self.main()[0], 1)


if __name__ == "__main__":
    unittest.main()
