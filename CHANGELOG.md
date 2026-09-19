# Changelog

This project follows semantic versioning.

## 0.3.0 — 2026-09-19

### Added

- `eval` evidence from checks that print one-line metric JSON; a comparator that is not
  satisfied fails the verification even when every command exited 0, and the referenced
  dataset manifest is hashed (never the data).
- Worker capabilities in the heartbeat and the claim: `--label` (repeatable) and `--capacity`,
  with the runs in flight, the runner version and the available drivers. A worker executes up
  to `--capacity` runs at once, each in its own worktree.
- Per-project model access delivered on the claim: provider, base URL, model, credential and
  `allowed_hosts`, plus the `claude-code` driver preset with argv fixed by the runner.
- Delivery profiles with `steps[]`, a `migrate` gate that runs before the step marked `switch`,
  and `health[]` per service, with rollback semantics that depend on whether the migration
  declared itself reversible. A `compose` example ships in `deploy/eoslab/delivery.json.example`.
- Cross-repository dependencies: `trilha-runner next --repo alias=path` resolves
  `<alias>:TASK-NNN` in sibling checkouts, and `queue.Remote` resolves the same against the
  control plane.
- `sandbox.Docker`: the agent and the checks run in a container with the services the agent
  manifest declares, on a network of their own, selected with `--sandbox docker`.

### Changed

- `sandbox.Sandbox.Prepare` takes a `Request` and answers an `Env` that says how to start a
  command inside the sandbox, so the driver and the checks run in the same place. `None` is
  unchanged in behaviour.
- `deployer.Load` validates every profile before a deployment can reach it.

### Security

- A run's credential reaches the agent only as process environment — through a 0600 env file
  inside a sandbox, never on a command line — and is redacted from captured output.
- A base URL outside a project's `allowed_hosts` is refused before the first request; the
  refusal is a `run` record with `stage: policy` and the task goes to `failed`.
- A run whose requirements a worker does not meet is refused and reported, never executed.
- The Docker sandbox reports a container that did not stay up, with its own output, instead of
  waiting out the readiness timeout on something that is already gone.
- The Docker sandbox never mounts the Docker socket, keeps the root filesystem read-only with
  the worktree as the only writable path that survives, and fixes cpu, memory, pid,
  `no-new-privileges` and `cap-drop` limits in the runner rather than the manifest.

## 0.2.0 — 2026-09-13

### Added

- Versioned Cloud work bundles materialized as Trilha Spec files in isolated repository checkouts.
- Optional publication of specification and implementation branches.
- Local allow-listed deployment profiles with deploy, rollback and health-check execution.
- Hardened eoslab `systemd` installation examples and self-hosted CI support.

### Security

- Git credentials remain on the runner host and never transit through Cloud.
- Deployment processes receive a minimal environment and returned logs redact supplied secret values.
