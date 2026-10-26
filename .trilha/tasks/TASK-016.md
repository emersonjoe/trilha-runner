---
id: TASK-016
title: "A1 RM-013: heartbeat a cada 30 s enquanto houver run ativa"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - run simulada de 100 s tem ao menos 3 heartbeats running 1/1 além das transições
checks:
  - go test ./cmd/trilha-runner -run TestWorkerHeartbeatsWhileARunIsActive -count=1
created: "2026-10-26T00:31:21Z"
---
