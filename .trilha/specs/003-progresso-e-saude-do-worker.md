---
id: 003-progresso-e-saude-do-worker
title: Progresso ao vivo e saude do worker
status: done
issue: "22, 25 (emersonjoe/trilha-cloud)"
---

# Progresso ao vivo e saude do worker

## Why

Duas issues do `emersonjoe/trilha-cloud` (#22 e #25) pedem mudancas no
`trilha-runner` que precisam sair juntas na mesma `v0.4.0`, porque os dois
lados do contrato remoto mudam ao mesmo tempo.

A #22 dá ao portal do Cloud a visão de uma run `running`: hoje o worker só
fala com o Cloud no fim (`POST .../result`), então uma run em andamento não
mostra fase, tentativa nem branch — só "aguardando resultado" até terminar
ou falhar. O `runner.Runner` já loga cada mudança de fase em `Log`, mas como
texto solto; não há como o Cloud usar isso sem parsear string.

A #25 dá ao portal a visão de um worker que nunca chegou a rodar nada. Em
2026-09-19 o `trilha-runner-acervo.service` reiniciou em loop duas vezes
(permissão negada, depois `.trilha/` ausente) e a "Frota de workers" mostrou
só o último heartbeat bom, como se estivesse tudo normal — porque
`cmdWorker` monta o `runner.Runner` (que falha sem `.trilha/`) antes mesmo
de existir um `queue.Remote` para avisar o Cloud, e o heartbeat existente
não carrega erro nenhum.

## What changes

### Progresso estruturado (issue #22)

- `runner.Runner` ganha `Progress func(Event)` ao lado do `Log func(string)`
  existente; `nil` descarta os eventos. `Event` tem `Phase`, `Message`,
  `Attempt`, `MaxAttempts`, `Branch`, `Model`, `AttemptStatus`,
  `FailureClass`, `ErrorCode` — o mesmo evento nunca é derivado de uma
  string de `Log`, os dois são emitidos lado a lado nos mesmos pontos.
- Fases emitidas: `creating_worktree` (worktree criado, com `Branch`),
  `consulting_ai` (início de cada tentativa, com `Attempt`/`MaxAttempts`/
  `Model`), `running_checks` (commit e verificação), `retry_wait`
  (tentativa transitória ou corretiva que vai repetir, com
  `AttemptStatus: "failed"` e `FailureClass`) e `integrating` (checks
  passaram, run vai para `review`, com `AttemptStatus: "passed"`).
- `queue.Progress` espelha `runner.Event` campo a campo, com tags JSON
  (`phase`, `message`, `attempt`, `max_attempts`, `branch`, `model`,
  `attempt_status`, `failure_class`, `error_code`) combinando com o corpo
  que a rota `POST /api/runs/{id}/progress` do Cloud espera.
- `queue.Remote.Progress(ctx, item, e)` posta esse corpo com timeout
  próprio de 5s, independente do timeout do cliente HTTP compartilhado.
  Toda chamada ignora o erro (`_ = remote.Progress(...)`): progresso é
  best-effort e nunca atrasa nem aborta uma run.
- `executeWorkerItem` liga `active.Progress` (e `created.Progress` no
  caminho do bundle materializado) a um adaptador que converte
  `runner.Event` em `queue.Progress` e chama `remote.Progress`.

### Saude do worker (issue #25)

- `cmdWorker` agora monta `queue.Remote` logo depois de validar
  `--cloud`/`--token`/`--project`/capacidade, **antes** de `newRunner`.
  Qualquer erro de arranque a partir daí — `newRunner` (tipicamente
  `.trilha/` ausente) ou `deployer.Load` — dispara
  `_ = q.Heartbeat(ctx, "failed", err.Error())` antes de devolver o erro; o
  processo ainda sai com código não-zero e o systemd reinicia como hoje.
- `queue.Remote.Heartbeat` ganha um parâmetro variádico
  `errorText ...string`, carregado como `"error"` no corpo JSON quando
  presente. A assinatura variádica mantém todo chamador existente
  (`Heartbeat(ctx, status)`) compilando sem alteração.
- `runRemoteWorker` manda um heartbeat `stopping` best-effort (timeout de
  3s, contexto próprio desligado do contexto já cancelado) sempre que sai
  porque o contexto foi cancelado — o `SIGTERM` do systemd, hoje o único
  caso — via um `defer` que verifica `ctx.Err()` na saída da função, o que
  cobre todo caminho de retorno disparado por cancelamento sem duplicar a
  chamada em cada `return`.
- Falhas de infraestrutura dentro de uma run (bundle, `EnsureRepository`,
  `PushBranch`) continuam só em `Result.Error`; o heartbeat de erro cobre
  apenas o arranque do processo e o desligamento, para não duplicar sinal.

## Out of scope

- Qualquer mudança do lado `trilha-cloud` (rota `/progress`, heartbeat com
  `error`, cálculo de online/offline, remoção de worker no portal) — coberta
  pelas issues #22 e #25 no repositório `emersonjoe/trilha-cloud`.
- Streaming do stdout do driver (o `Log` de saída completa continua só em
  `POST .../result`).
- Alertas (e-mail/Prometheus) e reinício automático — são do systemd e de
  fora deste escopo.

## Acceptance

- **SC-005 (issue #22)** Um teste em `cmd/trilha-runner` com
  `httptest.Server` no papel do Cloud: ao processar um item, o servidor
  recebe `POST /api/runs/run-1/progress` com `phase: consulting_ai` e
  `attempt: 1`; se o servidor responder 500 a esse POST, a run ainda
  termina normalmente e `POST .../result` ainda é enviado.
  (`TestWorkerReportsProgressBestEffort`)
- **SC-006 (issue #25)** `cmdWorker` rodado num diretório temporário sem
  `.trilha/` contra um `httptest.Server`: o servidor recebe
  `POST /api/workers/heartbeat` com `status: failed` e `error` contendo
  `no .trilha/`, e o comando devolve um erro não-nulo.
  (`TestWorkerReportsStartupFailureAsHeartbeat`)
- `gofmt -l runner driver worktree queue sandbox deployer syncer cmd` vazio,
  `go vet ./...` limpo, `make test` verde.

## Security impact

- Nenhum segredo novo trafega: `Progress` e o heartbeat de erro carregam
  apenas fase, mensagem, branch, modelo e texto de erro — nunca código,
  credencial ou variável de ambiente.
- Ambas as chamadas são best-effort e limitadas por timeout próprio (5s
  para progresso, 3s para o heartbeat de parada), então uma rota lenta ou
  fora do ar no Cloud não trava nem atrasa a execução de uma task.
