#!/usr/bin/env python3
"""Run the mandatory acceptance checks against a disposable BIND server."""

import base64
import argparse
import json
import io
import os
from pathlib import Path
import secrets
import signal
import subprocess
import sys
import tarfile
import tempfile
import time
import uuid


ROOT = Path(__file__).resolve().parent.parent
IMAGE = ("ubuntu/bind9:9.20-26.04_stable@sha256:"
         "8f62529754757e8cb7bbf27f43a1d280f9c5f92c2d30704565242058d8d24872")
VERSION = "BIND 9.20.24-1ubuntu0.4"


def run(*args, **kwargs):
    binary = isinstance(kwargs.get("input"), bytes)
    result = subprocess.run(args, cwd=ROOT, check=True, text=not binary,
                            stdout=subprocess.PIPE, stderr=subprocess.PIPE,
                            timeout=240, **kwargs)
    if binary:
        return result.stdout.decode().strip()
    if args[:2] == ("docker", "logs"):
        return (result.stdout + result.stderr).strip()
    return result.stdout.strip()


def configure(work):
    secret = base64.b64encode(secrets.token_bytes(32)).decode()
    key = work / "key.json"
    key.touch(mode=0o600)
    key.write_text(json.dumps(dict(name="fixture.example.",
                                  algorithm="hmac-sha256.", secret=secret)))
    config = work / "named.conf"
    config.touch(mode=0o600)
    config.write_text('''options {
    directory "/fixture";
    listen-on port 5353 { any; };
    listen-on-v6 { none; };
    recursion no;
    dnssec-validation no;
    pid-file "/fixture/named.pid";
    session-keyfile "/fixture/session.key";
};
controls {};
key "fixture.example." { algorithm hmac-sha256; secret "%s"; };
zone "example.test" {
    type primary;
    file "/fixture/zone";
    update-policy { grant fixture.example. subdomain sig.example.test. TXT; };
};
''' % secret)
    (work / "zone").write_text('''$TTL 1
@ IN SOA ns.example.test. hostmaster.example.test. (1 60 60 60 1)
@ IN NS ns.example.test.
ns IN A 127.0.0.1
unrelated IN TXT "keep"
selected.offer.sig IN A 192.0.2.1
''')


def wait_ready(name):
    for _ in range(100):
        if "running" in run("docker", "logs", name):
            return
        time.sleep(0.1)
    raise RuntimeError("BIND does not become ready")


def archive(work):
    def root_owner(info):
        # named drops capabilities before it reads the restricted configuration.
        info.uid = info.gid = 0
        info.uname = info.gname = "root"
        return info

    data = io.BytesIO()
    with tarfile.open(fileobj=data, mode="w") as files:
        files.add(work, arcname="fixture", filter=root_owner)
    return data.getvalue()


def acceptance(platform=None):
    platform_args = ["--platform", platform] if platform else []
    run("docker", "info")
    run("docker", "pull", *platform_args, IMAGE)
    metadata = run("docker", "image", "inspect", *platform_args, IMAGE, "--format",
                   "{{.Os}}/{{.Architecture}} {{json .RepoDigests}}")
    print(f"BIND image: {IMAGE}\nBIND platform: {metadata}", flush=True)
    name = "wingman-bind-" + uuid.uuid4().hex[:12]
    with tempfile.TemporaryDirectory(prefix="wingman-bind.") as directory:
        work = Path(directory)
        configure(work)
        try:
            run("docker", "network", "create", name)
            run("docker", "create", *platform_args, "--name", name, "--network", name,
                "--user", "0:0", "--publish", "127.0.0.1::5353/tcp", "--entrypoint", "/usr/sbin/named",
                IMAGE, "-g", "-u", "root", "-c", "/fixture/named.conf", "-n", "1")
            run("docker", "cp", "--archive", "-", name + ":/", input=archive(work))
            run("docker", "start", name)
            version = run("docker", "exec", name, "named", "-V")
            if VERSION not in version:
                raise RuntimeError("BIND version differs from the fixture pin")
            print(version.splitlines()[0], flush=True)
            wait_ready(name)
            address = run("docker", "port", name, "5353/tcp")
            if not address.startswith("127.0.0.1:") or "\n" in address:
                raise RuntimeError("BIND port is not one loopback address")
            binary = work / "wingman"
            run("go", "build", "-o", str(binary), "./cmd/wingman")
            env = dict(os.environ, WINGMAN_BIND_ADDR=address,
                       WINGMAN_BIND_KEY=str(work / "key.json"), WINGMAN_BIND_CLI=str(binary))
            print(run("go", "test", "-tags=bindintegration", "./internal/dns",
                      "-run", "^TestBIND", "-count=1", "-v", env=env), flush=True)
        except Exception:
            print("\n".join(run("docker", "logs", name).splitlines()[-25:]), file=sys.stderr)
            raise
        finally:
            # Each resource has a unique name, so cleanup cannot remove another fixture.
            try:
                run("docker", "rm", "--force", "--volumes", name)
            finally:
                run("docker", "network", "rm", name)
    print("BIND acceptance: passes; fixture cleanup: complete", flush=True)


if __name__ == "__main__":
    def interrupted(_signal, _frame):
        raise RuntimeError("fixture interrupted")

    signal.signal(signal.SIGTERM, interrupted)
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--platform", choices=["linux/amd64", "linux/arm64"])
    options = parser.parse_args()
    try:
        acceptance(options.platform)
    except (OSError, RuntimeError, subprocess.SubprocessError) as error:
        print(f"BIND acceptance: failed: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError):
            print(error.stdout, error.stderr, file=sys.stderr)
        sys.exit(1)
