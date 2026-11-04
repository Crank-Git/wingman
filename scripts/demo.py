"""Run two CLI peers with a disposable authenticated DNS server."""

import argparse
import base64
from contextlib import ExitStack
import json
import os
from pathlib import Path
import re
import secrets
import signal
import subprocess
import sys
import tempfile
import time


ROOT = Path(__file__).resolve().parent.parent


def interrupt(signum, _frame):
    raise InterruptedError(f'demo receives signal {signum}')


class Demo:
    def __init__(self, work, stack, timeout):
        self.work = work
        self.stack = stack
        self.deadline = time.monotonic() + timeout
        self.children = []

    def start(self, name, args, input_pipe=False):
        log = self.work / f'{name}.log'
        output = self.stack.enter_context(log.open('w'))
        child = subprocess.Popen(args, stdin=subprocess.PIPE if input_pipe else subprocess.DEVNULL,
                                 stdout=output, stderr=subprocess.STDOUT, cwd=ROOT, start_new_session=True)
        self.children.append((name, child, log))
        return child, log

    def wait_notice(self, child, log, notice):
        while True:
            for name, process, _ in self.children:
                if process.poll() is not None:
                    raise RuntimeError(f'{name} exits before {notice} (exit {process.returncode})')
            content = log.read_text()
            if notice in content:
                return content
            if time.monotonic() >= self.deadline:
                raise TimeoutError(f'deadline expires before {notice}')
            time.sleep(.02)

    def join(self, name, child):
        remaining = max(.01, self.deadline - time.monotonic())
        try:
            code = child.wait(timeout=remaining)
        except subprocess.TimeoutExpired as error:
            raise TimeoutError(f'deadline expires before {name} exits') from error
        if code:
            raise RuntimeError(f'{name} exits with status {code}')

    def cleanup(self):
        # Ignore repeated interrupts until all children release their files.
        signal.signal(signal.SIGINT, signal.SIG_IGN)
        signal.signal(signal.SIGTERM, signal.SIG_IGN)
        for _, child, _ in self.children:
            if child.poll() is None:
                # The Go build can own compiler processes in the same process group.
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    # A child can exit between the status check and the signal.
                    pass
        deadline = time.monotonic() + 2
        for _, child, _ in self.children:
            try:
                child.wait(timeout=max(.01, deadline - time.monotonic()))
            except subprocess.TimeoutExpired:
                try:
                    os.killpg(child.pid, signal.SIGKILL)
                except ProcessLookupError:
                    pass
                child.wait()
            if child.stdin:
                try:
                    child.stdin.close()
                except BrokenPipeError:
                    # A failed peer can close its input before cleanup flushes it.
                    pass

    def exchange(self, binary):
        key = self.work / 'key.json'
        key.write_text(json.dumps(dict(name='demo.example.', algorithm='hmac-sha256.',
                                       secret=base64.b64encode(secrets.token_bytes(32)).decode())))
        room = 'demo-' + secrets.token_hex(8)
        shared = ['--zone', 'example.com', '--dns-tsig-key-file', str(key)]
        server, server_log = self.start('serve', [binary, 'serve', '--listen', '127.0.0.1:0', *shared])
        content = self.wait_notice(server, server_log, 'server listening on ')
        address = re.search(r'^wingman server listening on (127\.0\.0\.1:\d+) \(zone "example.com"\)$', content, re.MULTILINE)
        if address is None:
            raise RuntimeError('server reports no loopback address')
        peers = []
        for role in ('offer', 'answer'):
            child, log = self.start(role, [binary, role, '--addr', address[1], '--room', room,
                                          '--handshake-timeout', '20s', *shared], True)
            peers.append((role, child, log))
        for role, child, log in peers:
            self.wait_notice(child, log, role + ': connected')
        for role, child, _ in peers:
            child.stdin.write(f'demo message from {role}\n'.encode())
            child.stdin.flush()
        for role, child, log in peers:
            other = 'answer' if role == 'offer' else 'offer'
            self.wait_notice(child, log, f'{role}: received "demo message from {other}"')
        # Both receipts must arrive before either EOF can close the data channel.
        for _, child, _ in peers:
            child.stdin.close()
        for role, child, _ in peers:
            self.join(role, child)
        server.terminate()
        self.join('serve', server)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, help='use an existing CLI binary instead of a temporary build')
    parser.add_argument('--timeout', type=float, default=30, help='exchange deadline in seconds (default: 30)')
    args = parser.parse_args()
    if not 0 < args.timeout < float('inf'):
        parser.error('--timeout must be a finite positive number')
    os.umask(0o077)
    signal.signal(signal.SIGINT, interrupt)
    signal.signal(signal.SIGTERM, interrupt)
    with tempfile.TemporaryDirectory(prefix='wingman-demo.') as directory, ExitStack() as stack:
        work = Path(directory)
        demo = Demo(work, stack, args.timeout)
        try:
            binary = str(args.binary.resolve()) if args.binary else str(work / 'wingman')
            if not args.binary:
                # The build shares the same cleanup path and bounded child lifetime.
                child, _ = demo.start('build', ['go', 'build', '-o', binary, './cmd/wingman'])
                demo.join('build', child)
                demo.children.clear()
                demo.deadline = time.monotonic() + args.timeout
            demo.exchange(binary)
        except (OSError, RuntimeError, TimeoutError) as error:
            print(f'demo: {error}', file=sys.stderr)
            return 1
        finally:
            demo.cleanup()
            for name, _, log in demo.children:
                print(f'--- {name} ---')
                print(log.read_text(), end='')
    print('demo: both peers receive known text; temporary credentials and children are removed')
    return 0


if __name__ == '__main__':
    sys.exit(main())
