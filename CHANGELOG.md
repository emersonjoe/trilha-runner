# Changelog

This project follows semantic versioning.

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
