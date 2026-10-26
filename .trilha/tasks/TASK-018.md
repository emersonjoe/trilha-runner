---
id: TASK-018
title: "A3 DV-01: estado do agente a partir dos hooks"
status: review
spec: 005-fase-a-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - claude falso com --settings mostra working → idle no servidor; hook sai 0 em menos de 1 s; settings fora do git status da worktree
checks:
  - go test ./cmd/trilha-runner ./internal/agentlink ./runner ./driver -run TestClaudeHooksReachTheCloudAsAgentState|TestHookAlwaysExitsZeroWithinASecond|TestFromClaudeHookMapsEveryEvent|TestSocketDeliversMessagesAndFailsFast|TestAgentStateIsReportedOnChangeAtMostOncePerInterval|TestClaudeCodeNamesItsSessionAndGetsHooksOutsideTheWorktree|TestAIDriverReportsStateAndAsks -count=1
created: "2026-10-26T00:31:21Z"
---
