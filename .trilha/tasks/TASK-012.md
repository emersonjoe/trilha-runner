---
id: TASK-012
title: "RM-003: contrato de execução conforme e em CI"
status: review
spec: 004-fase-0-coerencia
agent: coder
depends_on: []
acceptance:
  - check-execution-contract.sh sai 0 para o runner
  - job conformance no ci.yml
checks:
  - make test
  - make conformance
created: "2026-10-21T02:17:21Z"
updated: "2026-10-21T02:38:51Z"
---
