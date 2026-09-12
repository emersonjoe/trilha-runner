---
id: TASK-001
title: Worktree per task
status: done
spec: 001-runner-local-fase-0
depends_on: []
acceptance:
  - branch trilha/task-nnn in .trilha/runs; reused on rerun
checks:
  - go test ./runner/...
created: "2026-09-12T23:19:29Z"
updated: "2026-09-12T23:19:29Z"
---
