import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent


class QualityChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        (self.root / "cmd").mkdir()
        (self.root / "internal").mkdir()
        (self.root / "scripts").mkdir()
        self.exceptions = self.root / "scripts/complexity-exceptions.tsv"
        self.exceptions.write_text("")

    def run_check(self, name, env=None):
        command = ["bash", str(SCRIPTS / name)] if name.endswith(".sh") else ["python3", str(SCRIPTS / name)]
        return subprocess.run(command, cwd=self.root, env=env, text=True, capture_output=True)

    def source(self, name, branches):
        code = "package fixture\n\nfunc " + name + "(n int) int {\n"
        code += "\tif n > 0 { n-- }\n" * branches
        code += "\treturn n\n}\n"
        return code

    def test_rejects_unformatted_source(self):
        (self.root / "cmd/main.go").write_text("package fixture\nfunc main(){ }\n")
        result = self.run_check("check-format.sh")
        self.assertEqual(result.returncode, 1)
        self.assertIn("cmd/main.go", result.stderr)

    def test_accepts_formatted_source(self):
        (self.root / "cmd/main.go").write_text("package fixture\n\nfunc main() {}\n")
        self.assertEqual(self.run_check("check-format.sh").returncode, 0)

    def test_rejects_complexity_21(self):
        (self.root / "cmd/main.go").write_text(self.source("New", 20))
        result = self.run_check("check-complexity.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("21 fixture New", result.stderr)

    def test_accepts_complexity_20(self):
        (self.root / "cmd/main.go").write_text(self.source("New", 19))
        self.assertEqual(self.run_check("check-complexity.py").returncode, 0)

    def test_exception_accepts_only_the_named_function(self):
        (self.root / "cmd/main.go").write_text(self.source("Legacy", 20) + self.source("New", 20).split("\n\n", 1)[1])
        self.exceptions.write_text("cmd/main.go\tfixture\tLegacy\t21\n")
        result = self.run_check("check-complexity.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("21 fixture New", result.stderr)
        self.assertNotIn("21 fixture Legacy", result.stderr)

    def test_exception_accepts_the_recorded_score(self):
        (self.root / "cmd/main.go").write_text(self.source("Legacy", 20))
        self.exceptions.write_text("cmd/main.go\tfixture\tLegacy\t21\n")
        self.assertEqual(self.run_check("check-complexity.py").returncode, 0)

    def test_exception_rejects_a_higher_score(self):
        (self.root / "cmd/main.go").write_text(self.source("Legacy", 21))
        self.exceptions.write_text("cmd/main.go\tfixture\tLegacy\t21\n")
        self.assertEqual(self.run_check("check-complexity.py").returncode, 1)

    def test_rejects_ignore_directives(self):
        (self.root / "cmd/main.go").write_text(self.source("New", 20).replace("func New", "//gocyclo:ignore\nfunc New"))
        result = self.run_check("check-complexity.py")
        self.assertEqual(result.returncode, 1)
        self.assertIn("gocyclo:ignore", result.stderr)

    def test_reports_a_missing_tool(self):
        env = dict(os.environ, PATH="/usr/bin:/bin")
        result = self.run_check("check-complexity.py", env)
        self.assertEqual(result.returncode, 1)
        self.assertIn("go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0", result.stderr)


if __name__ == "__main__":
    unittest.main()
