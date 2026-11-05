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
  info)
    [[ "${NO_DAEMON:-}" != 1 ]] || exit 1
    case "$3" in *Architecture*) echo arm64 ;; *) echo "${DOCKER_OS:-linux}" ;; esac ;;
  run)
    last="${@: -1}"
    case "$last" in
      dns|readiness|two-peer|relay)
        echo "$last" >> "$CALL_LOG"
        [[ "$last" != "${FAIL_SCENARIO:-}" ]] || exit 7 ;;
      *) [[ "${NO_CAPABILITY:-}" != 1 ]] || exit 1 ;;
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
        for scenario in ["dns", "readiness", "two-peer", "relay"]:
            with self.subTest(scenario=scenario):
                result = self.run_scenario(scenario)
                self.assertEqual(result.returncode, 0, result.stderr)

    def test_rejects_unknown_scenario(self):
        self.assertNotEqual(self.run_scenario("unknown").returncode, 0)
        self.assertFalse(self.log.exists())

    def test_runs_all_scenarios_in_order(self):
        result = self.run_scenario("all")
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(self.log.read_text().splitlines(), ["dns", "readiness", "two-peer", "relay"])

    def test_reports_unavailable_prerequisites_as_skips(self):
        for env in [{"NO_DAEMON": "1"}, {"DOCKER_OS": "windows"}, {"NO_CAPABILITY": "1"}]:
            with self.subTest(env=env):
                result = self.run_scenario("relay", **env)
                self.assertEqual(result.returncode, 77, result.stderr)
                self.assertIn("namespace skip:", result.stderr)
                self.assertNotIn("relay passes", result.stdout)

    def test_stops_after_each_failed_scenario(self):
        for index, scenario in enumerate(["dns", "readiness", "two-peer", "relay"]):
            with self.subTest(scenario=scenario):
                self.log.unlink(missing_ok=True)
                result = self.run_scenario("all", FAIL_SCENARIO=scenario)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(self.log.read_text().splitlines(),
                                 ["dns", "readiness", "two-peer", "relay"][:index + 1])
                self.assertIn("namespace host cleanup: complete", result.stdout)
                self.assertIn(f"namespace scenario: {scenario} fails", result.stderr)

    def make_relay_fixture(self):
        wingman = self.path / "wingman"
        wingman.write_text('''#!/usr/bin/env bash
case "$1" in
  serve) echo 'server listening on 10.203.0.1:5353'; exec sleep 20 ;;
  offer|answer)
    echo "$1: connected"
    echo "selected pair: $SELECTED_PAIR"
    echo "$1: received \\"namespace message from peer-a\\""
    echo "$1: received \\"namespace message from peer-b\\""
    cat >/dev/null ;;
esac
''')
        wingman.chmod(0o755)
        ip = self.path / "ip"
        ip.write_text('''#!/usr/bin/env bash
if [[ "$*" == *'route get '*'ipproto 17 sport 49152' ]]; then
  [[ "${HOST_ROUTE_AVAILABLE:-}" == 1 ]] && exit 0
  exit 2
fi
if [[ $1 == netns && $2 == exec ]]; then shift 3; exec "$@"; fi
''')
        ip.chmod(0o755)
        source = (ROOT / "scripts/namespace/two-peer.sh").read_text()
        source = source.replace("/bin-fixture/wingman", str(wingman)).replace("{1..600}", "{1..3}")
        script = self.path / "relay.sh"
        script.write_text('''#!/usr/bin/env bash
set -euo pipefail
offer_pid= answer_pid= server_pid= scenario_work=
cleanup() {
  for pid in "$offer_pid" "$answer_pid" "$server_pid"; do
    if [[ -n "$pid" ]]; then kill "$pid" 2>/dev/null || true; wait "$pid" 2>/dev/null || true; fi
  done
  rm -rf "$scenario_work"
}
trap cleanup EXIT
''' + source)
        return script

    def test_relay_rejects_available_host_route(self):
        script = self.make_relay_fixture()
        result = subprocess.run(["bash", str(script), "relay"],
                                env=dict(self.env, HOST_ROUTE_AVAILABLE="1",
                                         SELECTED_PAIR="local=relay remote=relay"),
                                capture_output=True, text=True, timeout=10)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn("host candidate route remains available", result.stderr)
        self.assertNotIn("namespace relay: both peers", result.stdout)

    def test_relay_requires_both_selected_types(self):
        script = self.make_relay_fixture()
        for pair in ["local=host remote=relay", "local=relay remote=host", "unavailable"]:
            with self.subTest(pair=pair):
                result = subprocess.run(["bash", str(script), "relay"],
                                        env=dict(self.env, SELECTED_PAIR=pair),
                                        capture_output=True, text=True, timeout=10)
                self.assertNotEqual(result.returncode, 0)
                self.assertIn("timeout waiting for selected pair: local=relay remote=relay", result.stderr)
                self.assertNotIn("namespace relay:", result.stdout)


if __name__ == "__main__":
    unittest.main()
