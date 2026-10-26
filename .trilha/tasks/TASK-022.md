---
id: TASK-022
title: "A7 DV-12: diff_stats no resultado"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - files, insertions e deletions de git diff --numstat contra o HEAD do checkout; binário conta 0/0
checks:
  - go test ./worktree -run TestNumStatCountsWhatTheBranchChanged -count=1
created: "2026-10-26T00:31:21Z"
---
