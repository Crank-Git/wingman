# Release guide

`dev` is the integration branch. `main` is the release branch.
Each promotion from `dev` to `main` requires explicit operator approval.
The release check builds the CLI. The project has no hosted deployment target.

## Local verification

Run all commands from the repository root.
Install Go 1.25.0, as specified in [go.mod](../go.mod).
Add the Go tools to `PATH`. Install Python 3; CI uses Python 3.12.
The race detector requires a C compiler and CGO support.

Install the pinned complexity tool:

```bash
go install github.com/fzipp/gocyclo/cmd/gocyclo@v0.6.0
export PATH="$(go env GOPATH)/bin:$PATH"
```

If you set `GOBIN`, add that directory to `PATH` instead.
If the default cache is restricted, set `GOCACHE` to a writable temporary directory.

Run the complete verification command:

```bash
bash scripts/verify.sh
```

The command checks these stages:

- Required tools and a writable cache.
- Go format and cyclomatic complexity, including tests.
- Quality checker, coverage checker, and verification command regressions.
- `go vet` and aggregate statement coverage of at least 95%.
- The race detector on all packages.
- The CLI build.

Each failed stage reports its name and exits nonzero.
Verification uses read-only module mode. It leaves `go.mod` and `go.sum` unchanged.
See [scripts/verify.sh](../scripts/verify.sh) for the exact commands.

## Integration into dev

1. Create a feature branch from `dev`.
2. Run the complete verification command.
3. Open a pull request (PR) into `dev`.
4. Review the diff and resolve review requests.
5. Require a successful `test` check for the current PR revision before the merge.

For an issue batch, sub-issue PRs target its integration branch.
The batch PR targets `dev` and requires CI before the merge.
Draft sub-issue PRs with `[skip ci]` commits require complete local verification.
They do not provide CI evidence for the batch PR.

The [CI workflow](../.github/workflows/ci.yml) runs on pushes and PRs to `dev` and `main`.
It reads Go 1.25.0 from `go.mod` and sets `GOTOOLCHAIN=local`.
It installs the pinned complexity tool and runs `bash scripts/verify.sh`.

| Identifier | Exact value |
|---|---|
| Workflow name | `ci` |
| Job ID | `test` |
| Job name | `test` |
| Required check context | `test` |
| Workflow-qualified display | `ci / test` |

A skipped or absent CI run does not satisfy the required check.
If CI fails, repair the failure before the merge.

## Branch protection: operator action

The operator configures branch protection in GitHub. This guide does not change repository settings.
Use the [GitHub branch protection instructions](https://docs.github.com/en/repositories/configuring-branches-and-merges-in-your-repository/managing-protected-branches/managing-a-branch-protection-rule).

For each of `dev` and `main`:

1. Open the repository's **Settings**, then **Branches**.
2. Add or edit the protection rule for that exact branch name.
3. Require a PR before a merge.
4. Require status checks before a merge.
5. Select the `test` check from the `ci` workflow.
6. Save the rule.

If the check is absent from the selector, first obtain a completed CI run for the branch.
Branch protection does not replace explicit operator approval for a promotion to `main`.

## Approved promotion to main

1. Open a PR from `dev` to `main`.
2. Review the complete release diff.
3. Require a successful `test` check for the current PR revision.
4. Record explicit operator approval for that promotion in the PR.
5. After approval, merge the promotion PR.

If the release diff changes after approval, obtain approval for the revised promotion.
Never promote automatically after CI passes.

To check the CLI release artifact locally, run:

```bash
go build ./cmd/wingman
./wingman help
```

The build creates a local `wingman` executable. It does not publish an artifact or deploy a service.
For a local manual check, use the [run skill](../.agents/skills/run/SKILL.md).
