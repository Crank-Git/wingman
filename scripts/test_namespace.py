import os
from pathlib import Path
import subprocess
import tempfile
import unittest


ROOT = Path(__file__).resolve().parent.parent


class NamespaceChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix="wingman-namespace-test.")
        self.addCleanup(self.temp.cleanup)
        self.path = Path(self.temp.name)
        self.log = self.path / "calls"
        for name, body in {
            "go": "exit 0\n",
            "docker": '''case "$1" in
  info) case "$3" in *Architecture*) echo arm64 ;; *) echo linux ;; esac ;;
  run)
    last="${@: -1}"
    case "$last" in
      dns|readiness|two-peer)
        echo "$last" >> "$CALL_LOG"
        [[ "$last" != "${FAIL_SCENARIO:-}" ]] || exit 7 ;;
    esac ;;
esac
''',
        }.items():
            file = self.path / name
            file.write_text("#!/usr/bin/env bash\nset -eu\n" + body)
            file.chmod(0o755)
        self.env = dict(os.environ, PATH=f"{self.path}:{os.environ['PATH']}",
                        CALL_LOG=str(self.log))

    def run_scenario(self, scenario, **env):
        return subprocess.run(["bash", "scripts/namespace-smoke.sh", "--scenario", scenario],
                              cwd=ROOT, env=dict(self.env, **env), capture_output=True, text=True,
                              timeout=20)

    def test_accepts_each_scenario(self):
        for scenario in ["dns", "readiness", "two-peer"]:
            with self.subTest(scenario=scenario):
                result = self.run_scenario(scenario)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_unknown_scenario(self):
        self.assertNotEqual(self.run_scenario("unknown").returncode, 0)
        self.assertFalse(self.log.exists())

    def test_runs_all_scenarios_in_order(self):
        result = self.run_scenario("all")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.log.read_text().splitlines(), ["dns", "readiness", "two-peer"])

    def test_stops_after_each_failed_scenario(self):
        for index, scenario in enumerate(["dns", "readiness", "two-peer"]):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_scenario("all", FAIL_SCENARIO=scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.log.read_text().splitlines(),
                                 ["dns", "readiness", "two-peer"][:index + 1])
                self.assertIn("namespace host cleanup: complete", result.stdout)
                self.assertIn(f"namespace scenario: {scenario} fails", result.stderr)


if __name__ == "__main__":
    unittest.main()
