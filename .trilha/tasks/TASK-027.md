---
id: TASK-027
title: "B5: artefatos de relatório verificados, copiados e enviados antes do resultado"
status: review
spec: 006-fase-b-do-plano-dev-3-0-no-runner
agent: coder
depends_on: []
acceptance:
  - segredo recusado, limite 5, só aceitos listados, ARTIFACTS_DISABLED descarta todos
checks:
  - "go test ./runner ./queue ./cmd/trilha-runner ./driver -run 'TestArtifacts|TestUploadArtifact|TestReportCommandsParseAndRefuse|TestAIDriverReportsFindings|TestClaimAndResult' -count=1"
created: "2026-10-26T01:11:20Z"
updated: "2026-10-26T01:12:20Z"
---
