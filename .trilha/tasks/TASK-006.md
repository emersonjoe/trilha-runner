---
id: TASK-006
title: Push branch and open a PR after review
status: idea
depends_on:
  - TASK-005
acceptance:
  - trilha-runner run --push publishes trilha/task-nnn
checks: []
created: "2026-09-12T23:19:29Z"
---

Ideia viva, mantida de propósito: é o **RM-030 — PR automática quando o run entra em
`review`** do roadmap da stack (`trilha-cloud/docs/ROADMAP-STACK.md`, §4 Fase 2). O que já
saiu dela é só o `--push` do worker (publica `trilha/task-nnn`); abrir a PR é o que falta e
depende do RM-030, não desta task sozinha.
