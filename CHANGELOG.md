# Changelog

This project follows semantic versioning.

## 0.2.0 — 2026-09-13

### Added

- Versioned Cloud work bundles materialized as Trilha Spec files in isolated repository checkouts.
- Optional publication of specification and implementation branches.
- Local allow-listed deployment profiles with deploy, rollback and health-check execution.
- Hardened eoslab `systemd` installation examples and self-hosted CI support.

### Security

- Git credentials remain on the runner host and never transit through Cloud.
- Deployment processes receive a minimal environment and returned logs redact supplied secret values.
