---
id: TASK-009
title: Progresso estruturado e heartbeat de saude do worker
status: done
spec: 003-progresso-e-saude-do-worker
agent: coder
depends_on: []
acceptance:
  - runner.Runner emite Progress(Event) nos pontos que hoje só logam texto
  - queue.Remote.Progress posta o evento em /api/runs/{id}/progress, best-effort
  - executeWorkerItem liga o hook Progress dos dois caminhos (local e bundle)
  - cmdWorker monta queue.Remote antes de newRunner e reporta falha de arranque no heartbeat
  - queue.Remote.Heartbeat aceita um texto de erro opcional
  - runRemoteWorker manda heartbeat "stopping" best-effort ao cancelar o contexto
checks:
  - gofmt -l runner driver worktree queue sandbox deployer syncer cmd
  - go vet ./...
  - make test
created: "2026-09-19T00:00:00Z"
updated: "2026-09-19T00:00:00Z"
---
