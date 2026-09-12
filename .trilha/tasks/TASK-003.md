---
id: TASK-003
title: Run pipeline with evidence on every exit
status: done
spec: 001-runner-local-fase-0
depends_on:
  - TASK-001
  - TASK-002
acceptance:
  - review or failed, never running left behind
checks:
  - go test ./runner/...
created: "2026-09-12T23:19:29Z"
updated: "2026-09-12T23:19:31Z"
---
