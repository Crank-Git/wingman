# Format and complexity checks

Run these commands from the repository root:

```sh
go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
export PATH="$(go env GOPATH)/bin:$PATH"
bash scripts/check-format.sh
python3 scripts/check-complexity.py
python3 -m unittest discover -s scripts -p test_quality.py
```

The checks require Go, Bash, and Python 3. The complexity check requires the version in `scripts/tools.json`.
For a restricted environment, set `GOCACHE`, `GOPATH`, `GOMODCACHE`, and `GOBIN` to writable temporary directories before installation.
Add `GOBIN` to `PATH` when you set it.

Both checks inspect all Go files under `cmd/` and `internal/`. This includes tests.
The format check reports files that need gofmt. It leaves source files unchanged.
The complexity check rejects functions above 20. It rejects `gocyclo:ignore` directives.
Each failure exits with a nonzero status and reports its cause on stderr.

`scripts/complexity-exceptions.tsv` records each legacy exception by path, package, function, and maximum score.
Use tabs between the fields. A new function receives no exception from another function in the same file.
An existing exception permits no increase above its recorded score.
The inherited baseline needs no exceptions.

The regression tests create temporary Go fixtures. They use the installed complexity tool and require no network access.
The fixtures cover complexity 20, complexity 21, format failures, exception boundaries, and a missing tool.

Verified against [the gocyclo v0.6.0 documentation](https://github.com/fzipp/gocyclo/blob/v0.6.0/README.md), retrieved 2026-11-04.
