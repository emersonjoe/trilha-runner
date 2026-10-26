---
id: TASK-020
title: "A5 DV-03: continuação na mesma sessão ou em sessão nova citada"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - mesma sessão claude-code usa --resume com o id; outro driver ou resume recusado cai no pacote com pergunta e resposta citadas; session no resultado
checks:
  - go test ./runner -run TestContinuationResumesTheSameClaudeSession|TestContinuationWithoutAResumableSessionStartsFresh -count=1
created: "2026-10-26T00:31:21Z"
---
