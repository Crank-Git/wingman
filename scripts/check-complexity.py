#!/usr/bin/env python3
import json
from pathlib import Path
import shutil
import subprocess
import sys


def tool_path():
    tool = json.loads((Path(__file__).parent / "tools.json").read_text())["gocyclo"]
    install = f"go install {tool['module']}/cmd/gocyclo@{tool['version']}"
    binary = shutil.which("gocyclo")
    if not binary:
        raise ValueError(f"missing gocyclo; run {install}")
    result = subprocess.run(["go", "version", "-m", binary], capture_output=True, text=True, check=True)
    modules = [line.split()[:3] for line in result.stdout.splitlines()]
    if ["mod", tool["module"], tool["version"]] not in modules:
        raise ValueError(f"gocyclo has the wrong version; run {install}")
    return binary


def exceptions():
    entries = {}
    for line in Path("scripts/complexity-exceptions.tsv").read_text().splitlines():
        if not line or line.startswith("#"):
            continue
        path, package, function, maximum = line.split("\t")
        key = (path, package, function)
        if key in entries or int(maximum) <= 20:
            raise ValueError(f"invalid exception: {line}")
        entries[key] = int(maximum)
    return entries


def source_files():
    files = []
    for root in (Path("cmd"), Path("internal")):
        if not root.is_dir():
            raise ValueError(f"missing source directory: {root}")
        for path in sorted(root.rglob("*.go")):
            # Directives bypass the per-function exception record.
            if "gocyclo:ignore" in path.read_text():
                raise ValueError(f"{path}: remove gocyclo:ignore; use a specific exception")
            files.append(str(path))
    if not files:
        raise ValueError("no Go source files")
    return files


def check():
    binary = tool_path()
    limits = exceptions()
    result = subprocess.run([binary, "-over", "20", *source_files()], capture_output=True, text=True)
    if result.returncode not in (0, 1) or result.stderr:
        raise ValueError(f"gocyclo failed: {result.stderr.strip() or result.returncode}")
    failures = []
    for line in result.stdout.splitlines():
        score, package, function, location = line.split()
        path = location.rsplit(":", 2)[0]
        if int(score) > limits.get((path, package, function), 20):
            failures.append(line)
    if result.returncode == 1 and not result.stdout.strip():
        raise ValueError("gocyclo failed without a result")
    if failures:
        raise ValueError("functions exceed complexity 20 or their recorded maximum:\n" + "\n".join(failures))
    print("complexity: passes (limit 20)")


if __name__ == "__main__":
    try:
        check()
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"complexity: {error}", file=sys.stderr)
        sys.exit(1)
