---
id: TASK-028
title: "B6 DV-13a: doctor --worktrees só relatório, poda com flag, saída 12"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - sem flag não apaga; poda só o pedido; não mesclado pulado com saída 12
checks:
  - "go test ./worktree ./cmd/trilha-runner -run 'TestDoctor' -count=1"
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
