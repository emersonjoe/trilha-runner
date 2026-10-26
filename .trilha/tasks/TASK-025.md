---
id: TASK-025
title: "B3 DV-07: caça só-leitura com achados, falha policy se mudar algo"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - caça done com achados válidos; mudança vira policy sem achados; Docker monta readonly
checks:
  - "go test ./runner ./worktree ./sandbox ./cmd/trilha-runner -run 'TestHuntRun|TestReadOnlyRun|TestDetachedCheckoutCreatesNoBranch' -count=1"
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
