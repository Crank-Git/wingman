import json
import base64
import os
from pathlib import Path
import signal
import subprocess
import tempfile
import time
import unittest


ROOT = Path(__file__).resolve().parent.parent
FAKE = '''#!/usr/bin/env python3
import json, os, pathlib, signal, sys, time
args = sys.argv[1:]
role = args[0]
key = pathlib.Path(args[args.index('--dns-tsig-key-file') + 1])
record = dict(args=args, pid=os.getpid(), key=str(key), mode=key.stat().st_mode & 0o777,
              directory_mode=key.parent.stat().st_mode & 0o777, content=json.loads(key.read_text()))
pathlib.Path(os.environ['AUDIT'], role).write_text(json.dumps(record))
signal.signal(signal.SIGTERM, lambda *_: sys.exit(0))
if os.environ.get('MODE') == 'failure' and role == 'answer': sys.exit(7)
if os.environ.get('MODE') == 'stubborn': signal.signal(signal.SIGTERM, signal.SIG_IGN)
if os.environ.get('MODE') in ('hang', 'stubborn'):
    while True: time.sleep(.05)
if role == 'serve':
    print('wingman server listening on 127.0.0.1:12345 (zone "example.com")', flush=True)
    while True: time.sleep(.05)
print(role + ': connected', flush=True)
line = sys.stdin.readline()
other = 'offer' if role == 'answer' else 'answer'
print(role + ': received "demo message from ' + other + '"', flush=True)
sys.stdin.read()
'''


class DemoChecks(unittest.TestCase):
    def setUp(self):
        self.temp = tempfile.TemporaryDirectory(prefix='demo test ')
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.audit = self.root / 'audit'
        self.audit.mkdir()
        self.binary = self.root / 'fake wingman'
        self.binary.write_text(FAKE)
        self.binary.chmod(0o700)
        self.env = dict(os.environ, AUDIT=str(self.audit), TMPDIR=str(self.root))
        self.command = ['bash', str(ROOT / 'scripts/demo.sh'), '--binary', str(self.binary), '--timeout', '2']

    def assert_clean(self):
        records = [json.loads(p.read_text()) for p in self.audit.iterdir()]
        self.assertTrue(records)
        for record in records:
            self.assertFalse(Path(record['key']).exists())
            with self.assertRaises(ProcessLookupError):
                os.kill(record['pid'], 0)
        self.assertEqual(list(self.root.glob('wingman-demo.*')), [])
        return records

    def test_exchanges_text_with_restricted_files_and_cleans_children(self):
        result = subprocess.run(self.command, env=self.env, capture_output=True, text=True, timeout=8)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertIn('demo: both peers receive known text', result.stdout)
        records = self.assert_clean()
        self.assertEqual(len(records), 3)
        self.assertEqual(len({r['args'][r['args'].index('--room') + 1] for r in records if r['args'][0] != 'serve'}), 1)
        for record in records:
            self.assertEqual(record['mode'], 0o600)
            self.assertEqual(record['directory_mode'], 0o700)
            self.assertEqual(set(record['content']), {'name', 'algorithm', 'secret'})
            self.assertNotIn(record['content']['secret'], result.stdout + result.stderr)
            self.assertNotIn(record['content']['secret'], record['args'])

    def test_propagates_child_failure_and_cleans_files(self):
        result = subprocess.run(self.command, env=dict(self.env, MODE='failure'), capture_output=True, text=True, timeout=8)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('answer exits', result.stderr)
        self.assert_clean()

    def test_bounds_waits_and_cleans_files(self):
        result = subprocess.run(self.command, env=dict(self.env, MODE='hang'), capture_output=True, text=True, timeout=8)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('deadline', result.stderr)
        self.assert_clean()

    def test_kills_a_child_that_ignores_shutdown(self):
        result = subprocess.run(self.command, env=dict(self.env, MODE='stubborn'), capture_output=True, text=True, timeout=8)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('deadline', result.stderr)
        self.assert_clean()

    def test_cleans_children_after_interrupt(self):
        child = subprocess.Popen(self.command, env=dict(self.env, MODE='hang'), stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            deadline = time.monotonic() + 4
            while not (self.audit / 'serve').exists() and child.poll() is None and time.monotonic() < deadline:
                time.sleep(.02)
            self.assertTrue((self.audit / 'serve').exists())
            child.send_signal(signal.SIGINT)
            child.communicate(timeout=8)
            self.assertNotEqual(child.returncode, 0)
            self.assert_clean()
        finally:
            if child.poll() is None:
                child.kill()
            child.communicate()

    def test_reports_build_failure_and_removes_temporary_files(self):
        binary = self.root / 'go'
        binary.write_text('#!/usr/bin/env bash\necho "build marker" >&2\nexit 7\n')
        binary.chmod(0o700)
        result = subprocess.run(['bash', str(ROOT / 'scripts/demo.sh')],
                                env=dict(self.env, PATH=f"{self.root}:{os.environ['PATH']}"),
                                capture_output=True, text=True, timeout=8)
        self.assertNotEqual(result.returncode, 0)
        self.assertIn('build exits with status 7', result.stderr)
        self.assertIn('build marker', result.stdout)
        self.assertEqual(list(self.root.glob('wingman-demo.*')), [])


class DemoRuntimeChecks(unittest.TestCase):
    def test_real_cli_exchanges_text_without_credential_or_sdp_output(self):
        with tempfile.TemporaryDirectory(prefix='demo runtime ') as directory:
            work = Path(directory)
            binary = work / 'wingman'
            subprocess.run(['go', 'build', '-o', str(binary), './cmd/wingman'], cwd=ROOT, check=True, timeout=120)
            wrapper = work / 'observed wingman'
            wrapper.write_text('''#!/usr/bin/env python3
import base64, json, os, pathlib, re, sys
args = sys.argv[1:]
key = pathlib.Path(args[args.index('--dns-tsig-key-file') + 1])
if args[0] == 'serve':
    data = json.loads(key.read_text())
    data['secret'] = base64.b64encode(b'recognizable-demo-tsig-secret').decode()
    key.write_text(json.dumps(data))
    (key.parent / 'password').write_text('recognizable-demo-turn-password')
    args += ['--turn-server', '127.0.0.1:0', '--turn-user', 'demo-user', '--turn-pass-file', str(key.parent / 'password')]
else:
    log = (key.parent / 'serve.log').read_text()
    address = re.search(r'turn-server listening on (127\\.0\\.0\\.1:\\d+)', log)[1]
    args += ['--turn', 'turn:' + address, '--user', 'demo-user', '--pass-file', str(key.parent / 'password')]
os.execv(os.environ['REAL_WINGMAN'], [os.environ['REAL_WINGMAN'], *args])
''')
            wrapper.chmod(0o700)
            result = subprocess.run(['bash', str(ROOT / 'scripts/demo.sh'), '--binary', str(wrapper)],
                                    env=dict(os.environ, REAL_WINGMAN=str(binary), TMPDIR=str(work)),
                                    capture_output=True, text=True, timeout=45)
            output = result.stdout + result.stderr
            self.assertEqual(result.returncode, 0, output)
            for role, other in [('offer', 'answer'), ('answer', 'offer')]:
                self.assertIn(f'{role}: received "demo message from {other}"', output)
            secret = 'recognizable-demo-tsig-secret'
            for marker in [secret, base64.b64encode(secret.encode()).decode(),
                           'recognizable-demo-turn-password', 'v=0', 'a=ice-pwd:',
                           'a=ice-ufrag:', 'a=fingerprint:', 'a=candidate:', 'm=application', '"sdp"']:
                self.assertNotIn(marker, output)
            self.assertEqual(list(work.glob('wingman-demo.*')), [])


if __name__ == '__main__':
    unittest.main()
