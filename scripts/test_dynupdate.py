import importlib.util
import io
from pathlib import Path
import tempfile
import tarfile
import unittest
from unittest.mock import patch


PATH = Path(__file__).with_name("dynupdate-acceptance.py")


class DynUpdateFixtureChecks(unittest.TestCase):
    def setUp(self):
        spec = importlib.util.spec_from_file_location("bind_fixture", PATH)
        self.fixture = importlib.util.module_from_spec(spec)
        spec.loader.exec_module(self.fixture)

    def test_limits_updates_to_authenticated_txt_subtree(self):
        with tempfile.TemporaryDirectory() as work:
            self.fixture.configure(Path(work))
            config = (Path(work) / "named.conf").read_text()
            self.assertIn("grant fixture.example. subdomain sig.example.test. TXT;", config)
            self.assertNotIn("zonesub", config)
            self.assertNotIn("allow-update", config)
            self.assertEqual((Path(work) / "key.json").stat().st_mode & 0o777, 0o600)
            with tarfile.open(fileobj=io.BytesIO(self.fixture.archive(Path(work)))) as files:
                for member in files:
                    self.assertEqual((member.uid, member.gid), (0, 0))

    def test_removes_container_after_failed_acceptance(self):
        calls = []

        def run(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("go", "test"):
                raise RuntimeError("test failure")
            if args[:2] == ("docker", "image"):
                return "linux/arm64 sha256:test"
            if args[:2] == ("docker", "port"):
                return "127.0.0.1:12345"
            if args[:2] == ("docker", "logs"):
                return "running"
            return "BIND 9.20.24-1ubuntu0.4"

        with patch.object(self.fixture, "run", side_effect=run):
            with self.assertRaisesRegex(RuntimeError, "test failure"):
                self.fixture.acceptance()
        self.assertTrue(any(c[:3] == ("docker", "rm", "--force") for c in calls))
        self.assertTrue(any(c[:3] == ("docker", "network", "rm") for c in calls))
        create = next(c for c in calls if c[:2] == ("docker", "create"))
        self.assertIn("127.0.0.1::5353/tcp", create)
        self.assertNotIn("--privileged", create)

    def test_rejects_wrong_runtime_version_and_removes_fixture(self):
        calls = []

        def run(*args, **kwargs):
            calls.append(args)
            return "BIND 9.18"

        with patch.object(self.fixture, "run", side_effect=run):
            with self.assertRaisesRegex(RuntimeError, "version differs"):
                self.fixture.acceptance()
        self.assertTrue(any(c[:3] == ("docker", "rm", "--force") for c in calls))
        self.assertTrue(any(c[:3] == ("docker", "network", "rm") for c in calls))

    def test_reports_missing_docker(self):
        with patch.object(self.fixture, "run", side_effect=FileNotFoundError("docker")):
            with self.assertRaises(FileNotFoundError):
                self.fixture.acceptance()

    def test_removes_fixture_after_success(self):
        calls = []
        paths = []

        def run(*args, **kwargs):
            calls.append(args)
            if args[:2] == ("docker", "port"):
                return "127.0.0.1:12345"
            if args[:2] == ("go", "test"):
                paths.append(Path(kwargs["env"]["WINGMAN_BIND_KEY"]).parent)
            return "BIND 9.20.24-1ubuntu0.4 running"

        with patch.object(self.fixture, "run", side_effect=run):
            self.fixture.acceptance()
        self.assertTrue(any(c[:3] == ("docker", "rm", "--force") for c in calls))
        self.assertTrue(any(c[:3] == ("docker", "network", "rm") for c in calls))
        self.assertEqual(len(paths), 1)
        self.assertFalse(paths[0].exists())


if __name__ == "__main__":
    unittest.main()
