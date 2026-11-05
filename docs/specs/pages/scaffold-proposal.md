---
id: scaffold-proposal
title: Scaffold proposal
---

# Scaffold proposal

## Approval scope
The operator approved these file contents on 2026-11-04. The scaffold now contains these files.
Approval includes file creation and the two .gitignore additions. Existing content remains intact.
The scaffold targets Codex only. No Claude files are proposed.
The active CODEX_PERMISSION_PROFILE is issue_flow at scaffold creation.
The session already grants write access to .agents/. No permission changes are proposed.

## Hooks
No hook is proposed. Verification belongs in the test skill and CI.
No .codex/ file needs an edit. Project hooks would require the operator to trust that configuration layer.

## Skills
- run: Start the local DNS service and two CLI peers.
- test: Run coverage and the relevant concurrency or integration checks.

## Proposed files

### AGENTS.md

```text
# Wingman

Wingman exchanges WebRTC SDP through DNS or HTTPS. It then carries text through WebRTC.
The inherited baseline has known defects. Built feature records describe observed behavior, not production readiness.

## Specification
Read docs/specs/spec.md. Read the feature file for the active issue.
Keep planned behavior distinct from existing behavior.

## Layout and stack
Go 1.25.0; Pion WebRTC, Pion TURN, and miekg/dns.
cmd/wingman owns the CLI. internal/ owns transport, peer, ICE, TURN, and metrics packages.
Tests live beside their source. scripts/ owns verification tools.

## Commands
- Build: go build ./cmd/wingman
- Run: go run ./cmd/wingman serve
- Test and coverage: bash scripts/coverage.sh
- Race check: go test -race ./...
- Format inspection: gofmt -l cmd internal
- Spec render: python3 docs/specs/render-spec.py

Set GOCACHE to a writable temporary directory when the normal cache is restricted.
The race check currently fails in StubTransport. Foundation repairs that defect before it becomes a required gate.

## Conventions
Use Go package names and colocated tests. Preserve error causes.
Use concise imperative commit subjects. Follow existing fix:, refactor:, and chore: prefixes when applicable.
Read docs/rules/quality.md before source or test changes.
Read docs/rules/ste.md before prose, comments, test names, issues, or PR text.
Read docs/rules/external-apis.md before external interface changes.

## Branch and release model
Merge batch PRs into dev. Promote dev to main only after explicit operator approval.
Keep specifications, issues, and PRs public. Do not commit credential values.

## Filing gate
A finding earns a tracker issue in five cases: behavior, user-visible output, a guard that guards nothing, a blocked epic, or a maintainer ruling.
A wrong product description under docs/specs/ earns a spec-update issue.
Repair every other finding in the change set that found it. File no separate issue.
If a repair changes behavior, stop that repair and file a behavior finding.

Route repairs as follows:
- During issue work: the issue PR, within files it already touches.
- During integration review: a documentation commit on the integration branch before the CI-trigger commit.
- With nothing open: a documentation-only PR into dev. Do not create a tracker issue for that repair.

A changelog entry records a decision at a batch gate.
Do not require a changelog row, spec edit, or accuracy pass per change set or batch member.
A batch that follows the approved plan needs no changelog entry.
```

### docs/rules/ste.md

```text
# STE — the writing standard for everything this plugin produces

Every document and every tracker issue this plugin writes uses **Simplified Technical
English (STE)**. This file is the standard. Load it before you write a spec, a feature
file, an issue body, an `AGENTS.md`, a project rule, or a user manual page.

STE exists because the readers of these artifacts — engineers, agents, and future
planning waves — must all extract the *same* meaning from a sentence. A requirement that
reads two ways gets built two ways.

> **About the source.** ASD-STE100 is the aerospace specification this standard derives
> from. Its approved-word dictionary is copyrighted and is **not** reproduced here. What
> follows is the enforceable subset: the writing rules, plus a per-project Terms list that
> does the job the dictionary does. Where this file and ASD-STE100 differ, this file wins
> for this plugin's output.

---

## 1 — What must be STE, and what must not

**Write in STE:**

| Artifact | Written by |
|---|---|
| `docs/specs/spec.md` and every `docs/specs/features/*.md` | project-planner |
| `docs/specs/spec.html` prose | project-planner |
| `AGENTS.md`, `docs/rules/*.md`, `skills/*/SKILL.md` | project-planner |
| `CLAUDE.md`, `.claude/rules/*.md`, `.claude/skills/*/SKILL.md` in a dual-harness project | project-planner |
| Every tracker issue title and body (epic, sub-issue, hotfix, spec-update, review finding) | spec-to-issues, issue-flow PM, project-review PM |
| Issue comments that state a decision, a plan, or a question | issue-flow PM |
| Spec `## Changelog` lines | issue-flow PM |
| `docs/manual/*.md` user manual pages | review-scribe |
| **Code comments and doc comments** — inline comments, docstrings, JSDoc/TSDoc, module headers, test descriptions | issue-worker, review-scribe |

**Never rewrite into STE — reproduce verbatim:**

- Anything quoted from a person: user answers, review comments, requirements the user
  dictated. Quote it, then restate it in STE underneath if it needs clarifying.
- Evidence: error messages, log excerpts, console output, test names, stack traces,
  command output, `file:line` references.
- Code, config, commands, JSON, file paths, identifiers, label names.
- Third-party product names and API field names.
- UI copy inside mockups — that is product content, and the design decides it.

Rewriting evidence destroys it. Rewriting a quote misrepresents the person.

---

## 2 — The rules

### Sentences

1. **A procedure sentence is 20 words or fewer. A description sentence is 25 or fewer.**
   Count the words. Over the limit, split it.
2. **One instruction per sentence.** "Create the branch and copy the env files" is two
   steps. Make it two.
3. **One topic per paragraph. Six sentences maximum.**
4. **Put the condition first, then the action.** "If the test fails, revert the commit" —
   not "Revert the commit if the test fails." The reader must know whether to keep reading
   before they act.
5. **Put the warning before the step it applies to**, never after. A caution the reader
   meets after acting is not a caution.

### Words

6. **One word, one meaning, one part of speech.** Choose the meaning the project needs and
   never use that word another way. If `batch` is a noun in this project, do not write
   "batch the issues" — write "group the issues into a batch".
7. **One concept, one word.** Never rotate synonyms for variety. If it is a "sub-issue",
   it is never also a "child issue", a "member", or a "task".
8. **Keep noun clusters to three words.** "The batch integration branch merge gate" is
   five. Write "the merge gate for the batch's integration branch".
9. **No metaphor, idiom, or slang.** Not "rebase storm", "burn CI", "the pipeline
   starves", "clobber", "stomp", "in the weeds". State the mechanism instead.
10. **No abbreviation the project has not defined**, and define each one once, on first
    use, in the Terms list.

### Grammar

11. **Active voice.** "The PM merges the batch PR" — not "The batch PR is merged."
    The reader must know who acts.
12. **Present tense** for behaviour; **imperative** for instructions.
13. **Keep the articles.** Write "the branch", not "branch". Dropped articles create
    genuine ambiguity about whether one or many are meant.
14. **No `-ing` form as a noun or as a heading.** "Batching" → "How to form a batch".
    "Grouping heuristics" → "How to group loose issues".
15. **Write positively.** State what to do. Keep a prohibition only when the wrong action
    is likely and costly, and then state it once, plainly.
16. **Use a vertical list** whenever a sentence would carry more than two conditions,
    parameters, or alternatives.

---

## 3 — Project Terms

Use the controlled vocabulary in `docs/specs/spec.md`, section `Terms`.

## 4 — Patterns for the artifacts

### A functional requirement

One testable statement, active voice, present tense, no conjunction.

~~~
Bad:  FR-auth-3 — Users should be able to reset their password and the system will
                  email them a link that expires after a while.
Good: FR-auth-3 — The project sends a password-reset link to the account's email address.
      FR-auth-4 — A password-reset link expires 60 minutes after the project sends it.
~~~

"should be able to" is not testable. "after a while" is not a value. Two requirements
were hiding in one sentence.

### An acceptance criterion

Write the observable result, not the implementation.

~~~
Bad:  Password reset works correctly.
Good: The account receives an email that contains a reset link.
      The reset link opens the reset form.
      The reset link shows an expiry message 60 minutes after the project sends it.
~~~

### An issue title

Imperative verb, one deliverable, 10 words or fewer, no ticket-speak.

~~~
Bad:  Auth stuff / password reset improvements (part 2)
Good: Send a password-reset email to the account
~~~

### An issue body

Each heading holds one kind of information. No prose that spans headings.

~~~markdown
## Context
The project has no way to recover an account. FR-auth-3 and FR-auth-4 define the flow.

## What to build
1. Add a reset-request form at `/account/reset`.
2. Send an email that contains a single-use reset link.
3. Expire the link 60 minutes after the project sends it.

## Acceptance criteria
- [ ] The account receives an email that contains a reset link.
- [ ] The reset link opens the reset form.
- [ ] The reset link shows an expiry message after 60 minutes.

## Out of scope
This issue does not change the sign-in form.
~~~

### A user manual step

Second person, present tense, one action per step, the screenshot after the step.

~~~
Bad:  After navigating to the settings area, you'll want to hit Save once you've made
      whatever changes you need.
Good: 1. Select **Settings**.
      2. Change the fields you want to change.
      3. Select **Save**. The project shows a confirmation message.
~~~

### A code comment

The rules in § 2 apply unchanged. Three of them do the most work in code, because a
comment is read in a hurry, out of context, months later.

**One sentence, one fact. Active voice. Say why, not what.**

~~~
Bad:  // Here we're basically just looping through all the users and, if they haven't
      // been seen in a while, we go ahead and clean them up (this was added because
      // of the memory thing).
Good: // Remove accounts that have been inactive for 90 days.
      // The session cache holds every account it has seen, so inactive accounts leak.
~~~

**Name the reason a reader cannot see.** Code already states what it does. A comment that
repeats the code is noise; a comment that states the constraint, the reason, or the
consequence is the one worth reading.

~~~
Bad:  // increment the counter
      counter += 1
Good: // The provider rejects a batch larger than 500 items.
      const MAX_BATCH = 500
~~~

**A docstring opens with one sentence that states the result.** Then the parameters, then
the failure modes. No metaphor, no `-ing` opener.

~~~
Bad:  /** Handles doing the thing with the user's stuff and returns whatever it finds. */
Good: /**
       * Return the account that owns the session token.
       *
       * @param token - A session token from the `Authorization` header.
       * @returns The account, or `null` when the token is expired or unknown.
       * @throws StoreError - The account store is unreachable.
       */
~~~

**Test names are sentences too.** One behaviour per name, present tense, active voice:
`rejects a reset link older than 60 minutes`, not `test reset link expiry stuff 2`.

Verbatim in comments, as everywhere: quoted error strings, URLs, identifiers, spec IDs
(`FR-auth-3`), issue references (`#123`), and any text copied from a specification.

Marker comments keep their conventional keyword — `TODO`, `FIXME`, `HACK` — because
tooling and the `code-auditor` match on it. Write the body of the marker in STE, and name
the issue: `// TODO(#412): Replace the fixed 60-minute expiry with the configured value.`

### A PM decision comment

State the decision, then the reason. One sentence each.

~~~
Good: Shipping batch #42 without sub-issue #57. #57 is parked on a product question,
      and the other four members do not depend on it. #57 moves to the next batch.
~~~

---

## 5 — Check before you write the file

- [ ] No sentence is longer than 25 words. No instruction is longer than 20.
- [ ] Every step holds one instruction.
- [ ] Every condition comes before its action; every warning comes before its step.
- [ ] Every domain word is in the Terms list, used with one meaning and one part of speech.
- [ ] No synonym rotation: one concept, one word, throughout the document.
- [ ] No noun cluster longer than three words.
- [ ] No metaphor, idiom, or undefined abbreviation.
- [ ] Active voice, present tense, articles present, no `-ing` nouns or headings.
- [ ] Lists carry anything with more than two conditions or parameters.
- [ ] Quotes, evidence, code, paths and identifiers are verbatim and unedited.
- [ ] Code comments state the reason, not the code. Docstrings open with the result.
      Test names read as one behaviour. Marker keywords (`TODO`, `FIXME`) kept, bodies
      written to the standard and carrying an issue number.
```

### docs/rules/quality.md

```text
# Go quality rules

Read these rules before changes to cmd/, internal/, scripts/, or tests.

- Keep Go source formatted with gofmt.
- Return errors with the failed operation. Preserve causes with %w.
- Handle every new error result unless the code states why loss is acceptable.
- Guard every shared mutable field with one documented synchronization rule.
- Make the owner close every connection, listener, timer, and background goroutine.
- Bound every request body and every retained collection reachable from network input.
- Keep credentials and raw SDP out of default logs.
- Use a regression test for each repaired behavior.
- Keep new functions at cyclomatic complexity 20 or below.
- Use the existing package boundary until a second caller needs an abstraction.
- Add configuration only when a caller needs a different value.
- Remove dead code and commented-out code in changed regions.
- State the reason in comments. Remove comments that restate the code.
- Avoid recovery that logs an error and continues with corrupt state.
- Make failed scripts exit nonzero with a cause on stderr.
- Keep tests independent from public internet services.
- Apply format, complexity, vet, race, and coverage checks once Foundation provides them.

Reviewers assign pass or fail per applicable rule. Existing violations outside the changed scope do not authorize unrelated refactors.
```

### docs/rules/external-apis.md

```text
# External interface rules

Read these rules before code uses an external library, service, or CLI.

Read the pinned documentation before the first call. Record the version and source in the PR.
Verify request fields, response fields, errors, limits, timeouts, and authentication.
If an interface contradicts the spec, report the conflict before a behavior change.
Treat third-party examples as examples, rather than proof of this project's required behavior.

Pinned interfaces:
- miekg/dns v1.1.73: https://pkg.go.dev/github.com/miekg/dns@v1.1.73
- Pion WebRTC v4.2.26: https://pkg.go.dev/github.com/pion/webrtc/v4@v4.2.26
- Pion TURN v5.1.3: https://pkg.go.dev/github.com/pion/turn/v5@v5.1.3
- DNS UPDATE: https://www.rfc-editor.org/rfc/rfc2136
- TSIG: https://www.rfc-editor.org/rfc/rfc8945

Verify BIND image version and update-policy documentation before the integration fixture.
No infrastructure or marketplace change follows from documentation access.
```

### .agents/skills/run/SKILL.md

```text
---
name: run
description: Start Wingman locally for manual checks.
---

# Run Wingman

1. Read docs/specs/spec.md for configuration.
2. Run go run ./cmd/wingman serve in one terminal.
3. Copy the printed host and port.
4. Run go run ./cmd/wingman offer --addr HOST:PORT --room demo in another terminal.
5. Run go run ./cmd/wingman answer --addr HOST:PORT --room demo in a third terminal.
6. Send a line after the channel opens.
7. Stop each process with Ctrl-C.
```

### .agents/skills/test/SKILL.md

```text
---
name: test
description: Run Wingman verification and report results.
---

# Test Wingman

1. Read the test strategy in docs/specs/spec.md.
2. Run bash scripts/coverage.sh.
3. Run go test -race ./... for concurrency changes.
4. Run the relevant integration smoke path for transport or peer changes.
5. Report command results and any skipped prerequisites.

The inherited race suite fails in StubTransport. Do not report that failure as a pass.
After Foundation lands, use scripts/verify.sh as the primary command.
```

### docs/external.md

```text
# External configuration

No application environment variables exist in the inherited configuration.
Use CLI help for current flags. Use docs/specs/spec.md for planned configuration.

Credential names: signaling secret, TURN username, TURN password, DNS TSIG key name, DNS TSIG secret.
Planned file references: --signaling-secret-file, --dns-tsig-key-file, --tls-cert, --tls-key.
Keep credential values outside the repository. Examples use placeholders.

No hosted endpoint, deployment account, test account, or owned domain has been supplied.
The local demo uses loopback services. New integration tests create disposable rooms and credentials.
Support and project changes use the public Crank-Git/wingman GitHub repository.
```

### .gitignore additions

```text
.worktrees/
.issue-flow.local.json
```

### docs/adr/0000-template.md

```text
# Decision template

Date: 2026-11-04

Status: proposed

## Context
State the constraint.

## Decision
State the decision.

## Consequences
State the consequences.
```

### docs/adr/0001-go-and-pion.md

```text
# Go and Pion

Date: 2026-11-04

Status: accepted

## Context
Reconstructed from go.mod and internal/peer on 2026-11-04. The original rationale is unknown.

## Decision
Retain Go with Pion WebRTC and TURN.

## Consequences
The CLI needs no separate application runtime. Library upgrades require network regression tests.
```

### docs/adr/0002-ephemeral-storage.md

```text
# Ephemeral room storage

Date: 2026-11-04

Status: accepted

## Context
Reconstructed from the DNS and HTTPS maps on 2026-11-04. No persistent database exists.

## Decision
Record the existing choice of in-memory storage. Planned limits repair resource handling.

## Consequences
Restarts lose custom-server rooms. DynUpdate records belong to an external authoritative server.
```

### docs/adr/0003-service-trust.md

```text
# Service trust and credentials

Date: 2026-11-04

Status: accepted

## Context
Reconstructed from HTTPS Secret and TURN AuthHandler on 2026-11-04. DNS authentication is absent today.

## Decision
Record existing optional service credentials. Planned direct DNS transactions use TSIG.

## Consequences
Peers trust the configured service. DNS payloads remain visible without a separate encrypted transport.
```
