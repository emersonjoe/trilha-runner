# Testing

> 🇺🇸 English · [🇧🇷 Português](testing.pt-BR.md)

How `trilha-runner` is tested, and what a change has to bring with it. The user journeys
and their test cases live in [journeys.md](journeys.md).

## Start from the user and the risk

Before writing code, name the journey the change touches (see the catalog) and the worst
thing that happens if it breaks. The order of risk in this project:

1. **The maintainer's checkout is written to.** Lost or mixed work in someone's repository.
2. **A secret leaks** — onto a command line, into evidence, a log or the cloud.
3. **A run ends without evidence**, or with evidence that says it passed when it did not.
4. **A task moves to the wrong status**, so a reviewer merges work that never ran its checks.
5. **The worker stops reporting** — the cloud shows a run that is stuck or lost.

A change to one of these gets its case in the catalog before the code is written.

## The pyramid

| Level | What it covers here | Where | How it runs |
|---|---|---|---|
| Unit | rules, edges, exceptions: retry classification, dependency parsing, repository flags, eval comparators, redaction | `*_test.go` beside the code | `go test`, no network, no key |
| Integration | the borders: real `git` repositories (`worktree`, `runner`), the cloud HTTP contract with `httptest` (`queue`, worker), the protocol through `trilha-spec`, Docker with real containers (`sandbox`) | same packages | `go test`; the Docker case needs Docker and skips without it |
| End to end | the CLI binary as a user runs it, in a fresh git repository: `cmd/trilha-runner/e2e_test.go` | `cmd/trilha-runner` | `TestMain` builds the binary; `go test` runs it |

Many unit tests, integration at every border, few end-to-end cases — one per critical
journey. An edge case or an exception goes to the unit or integration level, never to the
end-to-end suite.

This project has no web interface, so there is no browser suite. If it ever gains one, its
end-to-end cases run in Chromium, Firefox and WebKit in CI.

## A production-like environment

- **Real git, not a fake.** Tests create repositories in `t.TempDir()` with `git init` and a
  commit, exactly as a maintainer's project looks. `t.TempDir()` removes them.
- **The `echo` driver** stands in for the model: the whole pipeline runs without a network or
  a key. The `exec` driver with `sh -c` stands in for a command-line agent.
- **The cloud is `httptest.Server`** speaking the same contract as trilha-cloud; the
  `execution-v1` fixture is compared byte for byte with `trilha-spec` (`make conformance`).
- **Docker runs real containers** (`pgvector/pgvector:pg16`, `alpine`) on a private network,
  torn down on every path.

## Test data

Synthetic only. Task titles, project names, tokens and credentials in fixtures are made up
(`secret`, `t@t`, `gateway.example.br`). **Never** real personal data, a real token or a real
repository's content in a fixture, in the repository or in a log. A test that creates
something outside `t.TempDir()` — a container, a network, a file — removes it with
`t.Cleanup` or `defer`.

## Commands

One command runs everything CI runs:

```bash
make ci
```

| Command | What it does |
|---|---|
| `make test` | gofmt, `go vet`, `go test ./...` — before every commit |
| `make race` | `go test -race ./...` |
| `make flaky` | the suite three times, shuffled, with `-race` — before calling a flaky test fixed |
| `make e2e` | only the CLI end-to-end cases |
| `make cover` | per-package coverage into `coverage.out` |
| `make ci` | `test` + `race`, what CI runs |

CI (`.github/workflows/ci.yml`) runs `make test` on Go 1.22 and 1.25 and `make race` on every
push and pull request.

## Rules for a change

- **A bug fix starts with a failing test.** Write the regression test, see it fail on the
  current code, then fix. The commit carries both.
- **New behavior lands with its tests** at the lowest level that can catch it, and with its
  case in [journeys.md](journeys.md) when it touches a journey.
- **Run the whole suite before saying a change is done**, and report the numbers: passed,
  failed, skipped (`go test -json ./... | ...` or the `ok`/`FAIL` lines of `make ci`).
- **A flaky test is never silenced.** No `t.Skip` to make CI green, no retry loop around an
  assertion. Fix the cause, or open an issue labelled `bug` with an owner and link it from
  the test. A skip is only for a missing platform capability (Windows without `sh`, a machine
  without Docker), and it says which.
- **Say what was not tested and why** in the pull request, in words the business reads.
