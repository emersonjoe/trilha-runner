---
id: TASK-011
title: "RM-002: release por tag com binários e checksums"
status: ready
spec: 004-fase-0-coerencia
agent: coder
depends_on: []
acceptance:
  - release.yml compila 4 alvos, publica sha256sums.txt e a seção do CHANGELOG
  - TestVersionMatchesTag falha quando RELEASE_TAG difere da versão
checks:
  - make test
created: "2026-10-21T02:17:21Z"
updated: "2026-10-21T02:17:21Z"
---
