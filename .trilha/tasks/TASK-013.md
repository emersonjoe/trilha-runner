---
id: TASK-013
title: "RM-014: falha de ambiente não queima tentativas"
status: ready
spec: 004-fase-0-coerencia
agent: coder
depends_on: []
acceptance:
  - check com ferramenta ausente termina failed em uma tentativa com error_code=environment_missing_tool e zero chamadas ao driver
  - exit 127 classificado como environment, sem retry
checks:
  - make test
created: "2026-10-21T02:17:21Z"
updated: "2026-10-21T02:17:21Z"
---
