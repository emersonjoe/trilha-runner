---
id: TASK-010
title: "RM-007: prontidão do Postgres por TCP e -count=20 verde"
status: ready
spec: 004-fase-0-coerencia
agent: coder
depends_on: []
acceptance:
  - go test ./sandbox -run TestDockerPostgresAndTeardown -count=20 passa
  - README, teste e catálogo usam pg_isready -h localhost
checks:
  - make test
created: "2026-10-21T02:17:21Z"
updated: "2026-10-21T02:17:21Z"
---
