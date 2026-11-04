import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent
STAGES = ["format", "complexity", "quality-tests", "coverage-tests", "verify-tests",
          "vet", "coverage", "race", "build"]


class VerificationChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="wingman-verify-test.")
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        scripts = self.root / "scripts"
        scripts.mkdir()
        shutil.copy(ROOT / "scripts/verify.sh", scripts)
        self.bin = self.root / "bin"
        self.bin.mkdir()
        self.log = self.root / "calls"
        self.env = dict(os.environ, PATH=f"{self.bin}:{os.environ['PATH']}",
                        CALL_LOG=str(self.log), REAL_GO=shutil.which("go"))
        self.command("go", '''#!/usr/bin/env bash
set -eu
case "$1" in
  vet) stage=vet ;;
  test) stage=race ;;
  build) stage=build ;;
  *) exit 2 ;;
esac
printf '%s\\n' "$stage" >> "$CALL_LOG"
test -d "$GOCACHE"
if [[ "$stage" == race && "${REAL_RACE:-}" == yes ]]; then
  exec "$REAL_GO" "$@"
fi
if [[ "${FAIL_STAGE:-}" == "$stage" ]]; then exit 7; fi
''')
        self.command("gocyclo", "#!/usr/bin/env bash\nexit 0\n")
        for name, stage in [("check-format.sh", "format"),
                            ("coverage_test.sh", "coverage-tests"),
                            ("coverage.sh", "coverage")]:
            (scripts / name).write_text(
                f'printf "%s\\n" "{stage}" >> "$CALL_LOG"\n'
                f'if [[ "${{FAIL_STAGE:-}}" == "{stage}" ]]; then exit 7; fi\n')
        (scripts / "check-complexity.py").write_text(
            'import os, sys\n'
            'with open(os.environ["CALL_LOG"], "a") as log:\n'
            '    log.write("complexity\\n")\n'
            'sys.exit(7 if os.environ.get("FAIL_STAGE") == "complexity" else 0)\n')
        for name, stage in [("test_quality.py", "quality-tests"),
                            ("test_verify.py", "verify-tests")]:
            (scripts / name).write_text(
                'import os, unittest\n'
                'class Fixture(unittest.TestCase):\n'
                '    def test_stage_passes(self):\n'
                '        with open(os.environ["CALL_LOG"], "a") as log:\n'
                f'            log.write("{stage}\\n")\n'
                f'        self.assertNotEqual(os.environ.get("FAIL_STAGE"), "{stage}")\n')

    def command(self, name, body):
        path = self.bin / name
        path.write_text(body)
        path.chmod(0o755)

    def verify(self, **env):
        return subprocess.run(["bash", str(self.root / "scripts/verify.sh")],
                              cwd=self.temp.name, env=dict(self.env, **env),
                              text=True, capture_output=True, timeout=180)

    def test_runs_each_required_stage(self):
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)
        self.assertEqual(self.log.read_text().splitlines(), STAGES)
        self.assertIn("verification: passes", result.stdout)

    def test_reports_each_failed_stage_and_stops(self):
        for index, stage in enumerate(STAGES):
            with self.subTest(stage=stage):
                self.log.write_text("")
                result = self.verify(FAIL_STAGE=stage)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn(f"stage {stage}: failed", result.stderr)
                self.assertEqual(self.log.read_text().splitlines(), STAGES[:index + 1])
                self.assertNotIn("verification: passes", result.stdout)

    def test_rejects_a_real_data_race(self):
        (self.root / "go.mod").write_text("module fixture\n\ngo 1.25.0\n")
        (self.root / "race_test.go").write_text('''package fixture

import (
    "sync"
    "testing"
)

func TestConcurrentWrites(t *testing.T) {
    var value int
    var done sync.WaitGroup
    done.Add(2)
    for i := 0; i < 2; i++ {
        go func() {
            defer done.Done()
            for n := 0; n < 1000; n++ {
                value++
            }
        }()
    }
    done.Wait()
    t.Log(value)
}
''')
        result = self.verify(REAL_RACE="yes")
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("DATA RACE", result.stdout + result.stderr)
        self.assertIn("stage race: failed", result.stderr)
        self.assertNotIn("build", self.log.read_text().splitlines())

    def test_reports_a_missing_complexity_tool(self):
        (self.bin / "gocyclo").unlink()
        self.env["PATH"] = str(self.bin)
        for name in ("bash", "dirname", "mktemp", "rm", "python3", "gofmt"):
            (self.bin / name).symlink_to(shutil.which(name))
        result = self.verify()
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("stage tools: failed", result.stderr)
        self.assertIn("go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0", result.stderr)

    def test_creates_a_writable_default_cache(self):
        self.env.pop("GOCACHE", None)
        result = self.verify()
        self.assertEqual(result.returncode, 0, result.stdout + result.stderr)


class WorkflowChecks(unittest.TestCase):
    def test_ci_uses_the_verification_entry_point(self):
        workflow = (ROOT / ".github/workflows/ci.yml").read_text()
        self.assertIn("go-version-file: go.mod", workflow)
        self.assertIn("GOTOOLCHAIN: local", workflow)
        self.assertIn("run: bash scripts/verify.sh", workflow)
        self.assertIn("github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0", workflow)


if __name__ == "__main__":
    unittest.main()
