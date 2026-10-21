---
id: TASK-014
title: "RM-008: driver_versions com versões reais de claude e codex"
status: ready
spec: 004-fase-0-coerencia
agent: coder
depends_on: []
acceptance:
  - claude --version e codex --version resolvidos uma vez na subida com timeout de 3 s, fallback unknown
checks:
  - make test
created: "2026-10-21T02:17:21Z"
updated: "2026-10-21T02:17:21Z"
---
