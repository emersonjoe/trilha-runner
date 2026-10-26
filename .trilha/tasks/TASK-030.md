---
id: TASK-030
title: "B8 DV-13c: código de fork com sandbox obrigatório, sem segredos, verify do branch padrão"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - sem Docker é policy; ai sem credencial no ambiente; checks do branch padrão
checks:
  - go test ./runner -run TestForeignCodeNeedsDockerGetsNoSecretAndVerifiesFromTheDefaultBranch -count=1
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
