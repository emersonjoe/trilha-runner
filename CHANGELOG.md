# Changelog

This project follows semantic versioning.

## Unreleased

### Fixed

- **`claude-code` runs reach the control plane with their model, tokens and cost.** The preset
  asks Claude Code for `--output-format json` and reads its result line: the final message is
  the report, the model it resolved, input tokens (cache writes and reads included), output
  tokens and `total_cost_usd`. The reported cost wins over the per-token estimate. Before,
  every `claude-code` run showed no model, tokens or cost. An older Claude Code that prints
  plain text keeps working, with nothing invented.

## 0.6.0 — 2026-10-25

Protocol 0.4 (trilha-spec v0.4.0, spec 016) on the runner side: phase A of the dev-3.0 plan
(`trilha-cloud/docs/PLANO-DEV3.md`, DV-01 to DV-04 and DV-12) and roadmap RM-012/RM-013; phase B
(DV-05b, DV-06, DV-07, DV-08, DV-13a–c and report artifacts).

### Added

- **Review runs** (`kind: review`, DV-06). `review_mode: report` (the default) reviews the task's
  branch read-only in a detached checkout of its own; `fix` works on the branch, commits and runs
  the task's checks. The prompt points at the merge base with the default branch; the result
  carries `review {verdict, findings}` (verdict `changes` when there are findings and the agent
  gave none, `pass` otherwise).
- **Bug hunts** (`kind: hunt`, DV-07): read-only, pointed at `hunt_seed`'s area and style, scoped
  to the files the branch changed since its merge base; the result carries `findings[]` stamped
  with the area and the style.
- **Variants** (`variant_index`, DV-05b) work on `trilha/task-nnn-v<N>` in `.trilha/runs/TASK-NNN-vN/`,
  prove the branch they created in `created-branch`, never adopt one they cannot prove
  (`-vN-2`, `-vN-3`…), and read nothing a sibling left in the context pack.
- **Review comments** (`review_comments[]`, DV-08) become a `<reviews>` block built on the runner
  from the worktree's lines, XML-escaped and quoted as data; the result carries `review_replies[]`.
- Agent commands `trilha-runner finding`, `verdict`, `reply` and `artifact`, and the same tools in
  the `ai` driver. A read-only `ai` run gets neither `write_file` nor `ask`.
- **Artifacts**: `trilha-runner artifact <file>` publishes `.html`, `.md`, `.png` or `.jpg` reports
  (≤ 2 MiB, ≤ 5 per run), copied to `.trilha/runs/<run>/artifacts/`; the worker uploads them with
  `PUT /api/runs/{id}/artifacts/{name}` before the result and lists only the accepted ones; 403
  `ARTIFACTS_DISABLED` drops them all.
- **`trilha-runner doctor --worktrees`** (DV-13a) classifies `.trilha/runs/*` as live,
  finished-kept, orphaned or metadata-only, with age buckets and sizes; deletes only with
  `--prune-orphans` or `--prune-older-than`, never unmerged or uncommitted work without
  `--force-unmerged`, a branch only when `created-branch` proves the runner made it; **exit code
  12** when something was skipped or failed.
- Opt-in live hunt against Ollama on a planted off-by-one.

- **The agent can ask.** `trilha-runner ask "<question>"`, on the agent's PATH during a run,
  ends the run `needs_input` with the question when the agent ends its turn: the work so far is
  committed, no check runs, no attempt failed, nothing is retried, and the task is `blocked`
  locally. The `ai` driver has an `ask` tool that does the same. The context pack tells every
  agent to ask one objective question and stop when information is missing.
- **Answers continue the work.** A claim with `continuation` resumes the same claude-code session
  (`--resume <session_id>`) with the answer, quoted, as the prompt; any other case starts a fresh
  session with the question and the answer quoted in the context pack. claude-code sessions are
  named up front (`--session-id <uuid>`) and the result reports `session`.
- **Agent state.** `trilha-runner hook claude` turns Claude Code's `PreToolUse`, `PostToolUse`,
  `Stop` and `StopFailure` hooks — configured in `.trilha/runs/TASK-NNN/claude-settings.json`,
  beside the worktree and passed with `--settings` — into `agent_state` progress reports
  (`working`, `idle`, `asked`, `stopped_error`…), sent only on change and at most once every 2 s;
  the `ai` driver reports the same from its loop. `trilha-runner attention "<reason>"` and
  `--clear` raise and clear the attention badge. The agent commands talk to the run over a Unix
  socket (`TRILHA_RUN_SOCKET`, `TRILHA_RUN_ID`) and always exit 0 within a second.
- **Task memory.** `trilha-runner note` and `trilha-runner overview` (and `note`/`overview` tools
  in the `ai` driver) put `notes` (source `ai`) and `overview` in the result; the claim's
  `context` is quoted into the context pack with `execution.QuoteContext`, labelled as
  information, never instructions.
- **Diff counts.** Results carry `diff_stats` (files, insertions, deletions) from
  `git diff --numstat <checkout HEAD>...HEAD`; a binary file counts as a file with 0/0.
- **Heartbeat during a run.** While any run is active the worker sends `running n/N` every 30 s
  (RM-013).
- **Result outbox.** The result is written to `.trilha/runs/<run-id>/result.json` before
  `POST /result`, retried with backoff instead of stopping the worker, set aside as
  `result.rejected.json` on a final 4xx, and resent at the next start (RM-012, runner side).
- Opt-in live tests against a real model: `TRILHA_E2E_OLLAMA=1` and `TRILHA_E2E_CLAUDE=1`.

### Changed

- The wire types follow protocol 0.4 (`github.com/emersonjoe/trilha-spec` at `9cb760d`): the claim
  decodes `kind`, `continuation` and `context`; progress carries `agent_state` and `attention`;
  the result carries `question`, `session`, `overview`, `notes`, `diff_stats` and may have status
  `needs_input`. `make conformance` also compares the `needs_input` fixture.
- A task left `running` by a runner that died (no live process behind
  `.trilha/runs/TASK-NNN/runner.pid`) goes back to `ready` with a note instead of blocking forever
  (RM-012).
- A failed `POST /result` no longer stops the worker (see the outbox above).
- `worktree clean` first stops the processes whose current directory is inside the worktree —
  SIGTERM, 1.5 s, SIGKILL; `/proc` on Linux, `lsof` on macOS — then removes it (DV-13b). It also
  accepts a run directory (`TASK-001-v2`).
- The runner records `created-branch` when it cuts a task's branch, and `checkpoint.json` and
  `agent.log` live in the run's own directory (`.trilha/runs/<task or run key>/`).

### Fixed

- The agent's environment now carries `USER` and `LOGNAME`: Claude Code on macOS finds a local
  login in the keychain by user and answered "Not logged in" without them.

### Security

- Questions, notes, overviews and attention reasons are size-limited (the protocol's limits) and
  secret-scanned before they leave the machine — the run's credential and credential-shaped
  strings (`sk-`, `ghp_`, `github_pat_`, `xox?-`, `AKIA`, private keys); a question with a secret
  becomes a fixed notice and its words stay in `question.txt` (mode 0600). The agent socket is
  owner-only; a session id reaches argv only as a UUID; the claude-code settings allow exactly the
  agent's own runner commands through Bash.
- A read-only run is verified, not trusted: a dirty worktree, a moved `HEAD` or a moved branch
  fails it with `failure_class: policy` and discards its findings and artifacts; Docker mounts the
  worktree read-only for it. Findings and replies are validated and secret-redacted; a text
  artifact holding a secret is refused, an image must start with its format's bytes, and a
  published file must come from the worktree or `$TRILHA_ARTIFACT_DIR`. Review comment bodies,
  authors and paths are XML-escaped, quoted as data, and read only inside the worktree (symbolic
  links included).
- **Foreign code** (`foreign_code: true`, DV-13c) runs only in the Docker sandbox (policy failure
  otherwise), gets no secret beyond the AI credential a command-line driver needs (none in the
  `ai` driver's environment; its checks run in the sandbox), reads `project.verify` from the
  default branch, and is never pushed.

## 0.5.0 — 2026-10-25

### Fixed

- `driver_versions` on the claim and the heartbeat repeated the runner's own version for every
  driver, so the fleet panel could not tell which `claude` or `codex` a worker had. The worker
  now runs `claude --version` and `codex --version` once at startup, with a 3-second timeout,
  and reports their first line (`unknown` when the binary is absent or silent); `echo` and `ai`
  keep the runner's version (roadmap RM-008).
- A tool the checks need but the worker does not have burned every attempt of the model
  (acervo `run-000007` spent three because `python` was missing; issues #13, #14). Before the
  first attempt the runner now resolves the program of every check — `exec.LookPath` on the
  host, `command -v` through the sandbox prefix when there is one — and a missing one ends the
  run at once: one attempt, no driver call, `failure_class: environment`,
  `error_code: environment_missing_tool`, a `run` record naming the tool and the check. Exit
  status 127, "command not found" and "executable file not found" during execution or
  verification get the same class, and the retry policy never retries an `environment` failure
  (roadmap RM-014).
- The Docker sandbox case `TestDockerPostgresAndTeardown` failed on `main` (2026-10-15) with
  `db:5432 - no response` after readiness had passed: `pg_isready -U postgres` inside the service
  container answers on the Unix socket while the postgres image still runs `initdb`'s temporary
  server, which listens on no TCP port, and the agent reached the service in the gap before the
  real server listened. The readiness example in the README, the test and the catalog now probe
  over TCP (`pg_isready -h localhost -U postgres`), the way the agent reaches the service; twenty
  consecutive runs pass (roadmap RM-007).
- A run that could not start because the project is not a git repository, or because the
  agent manifest names an unknown driver, returned an error and left the task `ready` with no
  evidence — against principle III and what the README promised. The task now goes
  `ready → running → failed` with a `run` record of stage `worktree` or `driver`, like a
  missing `exec` command already did. A task that is not `ready` or is blocked by a dependency
  is still left untouched. The note no longer repeats the stage (`worktree: worktree: …`), and
  `run`/`next` print no empty `branch` line when no worktree was created.
- A worker with `--push` could publish a branch only once. Its checkout is a single-branch
  clone, so a push left no remote-tracking ref, and the next `--force-with-lease` on the same
  branch expected it not to exist and was refused as stale: the second task of a round failed
  to publish the spec branch, and a rerun failed to publish its task branch. The checkout now
  tracks `refs/heads/trilha/*`, so each push records what the runner published and the next
  one is refused only when someone else moved the branch. A checkout cloned by an earlier
  version adopts the branches already on the remote the first time it is reused.

### Added

- `.trilha/` tells the truth about what shipped: spec 002 (Cloud bundles in real repositories,
  0.2.0) is `done`; TASK-007 "Container sandbox" (0.3.0) is `done` with an evidence note pointing at
  the 0.3.0 changelog; TASK-006 (open a PR after review) stays an idea and names roadmap RM-030
  (roadmap RM-004).
- The execution contract is conformant again: `testdata/contracts/execution-v1.json` and
  `execution-v1.schema.json` are byte for byte trilha-spec's canonical fixture and schema, and
  `queue.Attempt` / `runner.Attempt` carry an attempt's cost under the protocol's names
  (`provider`, `tokens_in`, `tokens_out`, `cost`, `currency`), filled from what the driver
  reports and proven by the contract test to survive a round trip. `error_code` now travels
  on the result and on each attempt. A `conformance` CI job checks out `emersonjoe/trilha-spec`
  (`main`) as a sibling and runs `make conformance`, so the fixtures cannot drift silently
  (roadmap RM-003).
- `.github/workflows/release.yml`: a tag `vX.Y.Z` builds static binaries for linux/amd64,
  linux/arm64, darwin/amd64 and darwin/arm64 with `-trimpath -ldflags "-s -w -X main.version=X.Y.Z"`,
  publishes them with `sha256sums.txt` and the CHANGELOG section of that version as the release
  notes. Nothing is built before `TestVersionMatchesTag` proves the tag is the version in
  `cmd/trilha-runner/main.go` (`version` is now a `var`, overridable at link time) and `make test`
  is green; actions are pinned by SHA like `ci.yml`. The README shows the install path without Go
  (roadmap RM-002).
- IBM's seven end-to-end testing practices, applied (`docs/testing.md`): every case in the
  journey catalog states resources, preconditions, data, steps and expected; `internal/catalog`
  fails the suite when a case is incomplete, cites a missing test, a test belongs to no case,
  the locales differ, or a fixture holds a real-looking e-mail, token or host. `make report`
  (`internal/testreport`) summarizes passed/failed/skipped, skip reasons, the slowest tests
  and coverage per package, and fails below `testdata/coverage-floor.txt`; CI shows it in the
  job summary. CI requires Docker on GitHub-hosted runners (`TRILHA_REQUIRE_DOCKER`), runs the
  binary and git packages on macOS (`make host`), and `nightly.yml` runs `make flaky` every
  night. A pull request template asks for journey, cases, tests per level, numbers, what was
  not tested and the acceptance in business words.
- Tests for the gaps the journey catalog listed: the Cloud client for bundles, progress and
  deployments; the worker's cross-project dependency check; the bundle checkout, divergent
  files and branch publishing; loading delivery profiles (including the shipped example); and
  the attempt loop retrying a transient provider failure through `Runner.Run`.
- A testing standard (constitution principle VII, `docs/testing.md`) and a catalog of user
  journeys with their test cases (`docs/journeys.md`); `make ci`, `make flaky`, `make e2e` and
  `make cover`. Tests for `worktree`, `internal/taskcompat`, cross-repository dependency rules,
  the retry policy and a failing agent through the CLI binary.

## 0.4.0 — 2026-09-19

### Added

- `runner.Runner` gained a `Progress func(Event)` hook, emitted alongside (never parsed from)
  the existing `Log` lines at the points a run changes phase: worktree creation, each attempt
  starting, verification, a retrying attempt and entering review. `queue.Remote.Progress`
  posts this best-effort to `POST /api/runs/{id}/progress` on trilha-cloud with its own 5-second
  timeout; the worker ignores its error so a Cloud outage never slows or aborts a run. Wired
  into `executeWorkerItem` for both the local and the Cloud-managed bundle path.
- `cmdWorker` now builds `queue.Remote` before `newRunner`, so a startup failure — most often
  a missing `.trilha/` — has somewhere to report itself: `_ = q.Heartbeat(ctx, "failed", err)`
  before returning the error. `queue.Remote.Heartbeat` takes an optional error string
  (`Heartbeat(ctx, status, errorText ...string)`) carried as `"error"` in the JSON body.
  `runRemoteWorker` also sends a best-effort `stopping` heartbeat (3-second timeout) when its
  context is cancelled, so a systemd `SIGTERM` shows up in the fleet panel instead of just
  going quiet.

## 0.3.2 — 2026-09-19

### Fixed

- `trilha-runner version`, the usage banner and the version a worker reports to the control
  plane said `0.3.0` while the binary was `0.3.1`: neither of the changes released as 0.3.1
  bumped the constant. A worker announces `RunnerVersion` and `DriverVersions` on every
  heartbeat and claim, so the whole fleet was identifying itself by a version it was not
  running — this is the constant catching up, and a test now fails when it drifts from the
  newest section of this file again.

## 0.3.1 — 2026-09-19

### Fixed

- The `claude-code` preset is selected for `claude-code-oauth` as well (the provider name the control plane sends when the credential is an OAuth token); before, an OAuth project fell through to the manifest's `exec` driver and the credential was never injected.
- The preset's fixed argv is now `claude -p - --permission-mode acceptEdits`, plus `--model <model>` from the claim when the model id is well-formed; print mode has nobody to answer a permission prompt, so without `acceptEdits` the agent could not write a file.

### Security

- The project's credential no longer reaches a command line. The run's environment enters the
  Docker sandbox once through a `0600` file at `docker run`, and `docker exec` carries no
  `KEY=VALUE`: an argument of `docker exec` is an argument of a host process, and `ps` showed it
  to every user on the machine.
- The sandboxed agent drops every capability (`--cap-drop ALL`) and runs as the user that owns
  the worktree rather than as root. The two go together: without `CAP_DAC_OVERRIDE` a root agent
  cannot write a worktree the operator owns, so running as the owner is what keeps the one
  writable path writable.

### Changed

- `sandbox.Sandbox.Prepare` takes the run's environment, so a sandbox can carry a credential
  into the container instead of each command carrying it.
- A container that does not stay up is reported with its own output, and a service that has
  exited is caught before its readiness command is waited on rather than after the timeout.
- `Prepare` answers a no-op release rather than nil on failure, so deferring it cannot panic.

## 0.3.0 — 2026-09-18

### Added

- Worker capability advertisements (`labels`, `capacity`, active runs and component versions) with concurrent execution up to the declared capacity.
- Per-project AI provider credentials, host allow-lists and the fixed `claude-code` driver preset.
- Numeric `eval` evidence parsed from check output, including threshold gates and hashed dataset manifests.
- Ordered delivery steps, migration gates, per-service health checks and migration-aware rollback reporting.
- Cross-repository dependency resolution through repeatable `next --repo alias=path` mappings and the remote dependency endpoint.
- Docker execution sandboxes with private networks, declared services, fixed resource limits and guaranteed teardown.

### Security

- Exec drivers now receive an allow-listed host environment; project credentials are injected only into the claimed process and redacted from output.
- Docker sandboxes never mount the Docker socket, use read-only roots and mount only the task worktree writable.

### Changed

- Requires Trilha Spec protocol 0.3 (`trilha-spec@56ddc07`): `eval` evidence now carries `metric`, `value`, `threshold`, `comparator` and `dataset` as first-class fields, read through `task.ParseMetricLine` and settled by `task.Eval`; the `>`, `<` and `=` comparators are no longer accepted as gates.

## 0.2.0 — 2026-09-13

### Added

- Versioned Cloud work bundles materialized as Trilha Spec files in isolated repository checkouts.
- Optional publication of specification and implementation branches.
- Local allow-listed deployment profiles with deploy, rollback and health-check execution.
- Hardened eoslab `systemd` installation examples and self-hosted CI support.

### Security

- Git credentials remain on the runner host and never transit through Cloud.
- Deployment processes receive a minimal environment and returned logs redact supplied secret values.
