---
id: TASK-026
title: "B4 DV-08: comentários de revisão citados da worktree e respondidos"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - bloco reviews com linhas locais e escape; respostas só para ids conhecidos
checks:
  - go test ./runner -run TestReviewCommentsAreQuotedFromTheWorktreeAndAnswered -count=1
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
