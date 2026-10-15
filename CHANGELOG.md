# Changelog

This project follows semantic versioning.

## Unreleased

### Fixed

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
