---
id: TASK-023
title: "B1 DV-05b: variantes em trilha/task-nnn-vN com prova de criação"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - variantes 1 e 2 em branches distintos, -v2 alheio intocado, pacote sem nada da irmã
checks:
  - go test ./worktree ./runner -run TestVariant -count=1
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
