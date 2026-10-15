# trilha-runner — executes tasks of the Trilha protocol

<!-- SPECKIT START -->
For additional context about technologies to be used, project structure,
shell commands, and other important information, read the current plan
<!-- SPECKIT END -->

## Commands

- `make test` — gofmt + `go vet ./...` + `go test ./...` (runner tests create real git repos; the CLI e2e builds the binary).
- `make build` — `bin/trilha-runner`.
- `make ci` — what CI runs (`test` + `race`). `make flaky` (shuffled ×3 with `-race`), `make e2e`, `make host` (what the macOS job runs), `make report` (markdown summary; fails below `testdata/coverage-floor.txt`), `make cover`.

## Testing (constitution VII, `docs/testing.md`)

- Name the journey and its worst impact first; add or update its case in `docs/journeys.md` **and** `docs/journeys.pt-BR.md` in the same change, pointing at the test that automates it. Every case has Resources, Preconditions, Data, Steps, Expected; every `func Test…` must be cited by a case — `internal/catalog` fails the suite otherwise.
- Fixtures: e-mails `t@t` / `…@example…`; a credential-shaped string carries `FAKE`, `EXAMPLE` or `SECRET`; tests name only `example`, loopback or `.local` hosts (`TestFixturesAreSynthetic`).
- Coverage floor only rises: when `make report` says to raise `testdata/coverage-floor.txt`, raise it in the same change.
- Pyramid: rules and edges in unit tests; git, the cloud HTTP contract, the protocol and Docker in integration tests; the CLI binary (`cmd/trilha-runner/e2e_test.go`) only for critical journeys.
- A bug fix starts with a regression test that fails on the current code.
- Synthetic data only. No `t.Skip` to hide a flaky test — fix it or open a `bug` issue with an owner.
- Before saying a change is done, run `make ci` and report passed/failed/skipped, plus what was left untested and why.

## Structure

- `runner/` the pipeline. `driver/` exec, ai, echo. `worktree/` git per task. `queue/` local graph + remote client. `sandbox/` seam. `cmd/trilha-runner/` CLI.
- Protocol types come from `github.com/emersonjoe/trilha-spec`; the model client and agent loop from `github.com/emersonjoe/trilha/ai`. Do not duplicate either.

## Rules (constitution in `.specify/memory/constitution.md`)

- The maintainer's checkout is never written to; every change lands on `trilha/task-nnn` in a worktree.
- Every run ends with evidence, including the runs that fail to start.
- No shell: commands are program + arguments (`task.SplitCommand`). No new dependency outside `trilha-spec` and `trilha`.
- The `echo` driver keeps the whole pipeline testable without a network or a key; tests use it.
- Public text in English with pt-BR in the same commit; specs in pt-BR.
- **The hands-on chapter follows the code.** The Trilha site teaches this tool in
  `https://emersonjoe.github.io/trilha/learn/agentic-runner` (pt: `/pt/aprender/agentico-runner`). A change that alters what a user types or
  sees — a command, a flag, a file format, an output — updates that chapter in the same session:
  `site/internal/docs/content/en/learn/agentic-runner.md` and `site/internal/docs/content/pt/aprender/agentico-runner.md` in the `emersonjoe/trilha` repository, both locales, before the spec closes.

## Agent skills

### Issue tracker

Work is tracked in this repository's GitHub Issues. See `docs/agents/issue-tracker.md`.

### Triage labels

Use the canonical five-label triage vocabulary. See `docs/agents/triage-labels.md`.

### Domain docs

Use the single-context domain documentation layout. See `docs/agents/domain.md`.
