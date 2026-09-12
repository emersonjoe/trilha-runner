---
id: TASK-005
title: CLI and worker loop
status: done
spec: 001-runner-local-fase-0
depends_on:
  - TASK-003
  - TASK-004
acceptance:
  - next, run, worker --once, worktree list/clean
checks:
  - go test ./cmd/...
created: "2026-09-12T23:19:29Z"
updated: "2026-09-12T23:19:32Z"
---
