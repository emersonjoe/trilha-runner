---
id: TASK-021
title: "A6 DV-04: notas, overview e contexto citado"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - note e overview vão para o resultado com fonte ai; nota maliciosa chega prefixada por > dentro do bloco rotulado
checks:
  - go test ./runner -run TestTaskMemoryAndAnswerAreQuotedIntoTheContextPack|TestAgentQuestionEndsTheRunNeedingInput -count=1
created: "2026-10-26T00:31:21Z"
---
