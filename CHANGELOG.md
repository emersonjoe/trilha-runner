# Changelog

This project follows semantic versioning.

## 0.3.1 — 2026-09-19

### Fixed

- The `claude-code` preset is selected for `claude-code-oauth` as well (the provider name the control plane sends when the credential is an OAuth token); before, an OAuth project fell through to the manifest's `exec` driver and the credential was never injected.
- The preset's fixed argv is now `claude -p - --permission-mode acceptEdits`, plus `--model <model>` from the claim when the model id is well-formed; print mode has nobody to answer a permission prompt, so without `acceptEdits` the agent could not write a file.

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
