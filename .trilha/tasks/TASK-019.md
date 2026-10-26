---
id: TASK-019
title: "A4 DV-02: pergunta (needs_input) e atenção"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - agente que chama ask termina needs_input com a pergunta, sem tentativa falha; segredo vira aviso fixo; attention chega ao cloud
checks:
  - go test ./cmd/trilha-runner ./runner ./internal/redact -run TestAgentQuestionEndsTheRunNeedingInput|TestAgentQuestionWithASecretIsRedacted|TestAgentCommandsReachTheCloudAndTheAnswerContinues|TestScanRemovesKnownAndCredentialShapedSecrets -count=1
created: "2026-10-26T00:31:21Z"
---
