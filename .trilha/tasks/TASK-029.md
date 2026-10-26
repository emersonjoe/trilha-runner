---
id: TASK-029
title: "B7 DV-13b: encerramento ordenado antes de remover a worktree"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - processo deixado na worktree morre antes da remoção (SIGTERM, 1,5 s, SIGKILL)
checks:
  - go test ./worktree -run TestCleanStopsProcessesInsideTheWorktreeBeforeRemovingIt -count=1
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
