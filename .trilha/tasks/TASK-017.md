---
id: TASK-017
title: "A2 RM-012: outbox do resultado e retomada de task órfã"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - resultado em disco antes do POST, um único aceito após 500, 500, 202; pendências na subida; task running sem processo volta a ready com nota
checks:
  - go test ./cmd/trilha-runner ./runner -run TestWorkerResultSurvivesTwoFailedDeliveries|TestWorkerResendsPendingResultsAtStart|TestRunResumesATaskLeftRunningWithNoLiveProcess -count=1
created: "2026-10-26T00:31:21Z"
---
