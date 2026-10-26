---
id: TASK-024
title: "B2 DV-06: revisão por IA em report (só-leitura verificado) ou fix"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - report dá veredito e fica done na ponta; fix commita, roda checks e volta a review; branch do origin ou recusado
checks:
  - "go test ./runner ./worktree -run 'TestReviewReportGivesAVerdict|TestReviewFixModeCommitsRunsChecksAndReports|TestExistingBranchComesFromOriginOrIsRefused' -count=1"
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
