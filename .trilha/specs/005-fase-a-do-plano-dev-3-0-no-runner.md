---
id: 005-fase-a-do-plano-dev-3-0-no-runner
title: Fase A do plano dev-3.0 no runner
status: approved
assets:
  - cmd/trilha-runner
  - runner/
  - driver/
  - queue/
  - worktree/
  - internal/agentlink
  - internal/redact
trust_boundaries:
  - agente → runner (socket Unix)
  - worker → trilha-cloud
  - trilha-cloud → runner (continuation, context)
  - runner → Claude Code (settings, hooks)
controls:
  - NIST SSDF PW.1, PW.7, PW.8
  - ASVS V5 (validação de entrada)
  - ASVS V7.1 (log e evidência sem segredo)
  - ASVS V14.2 (dependências)
evidence:
  - make ci
  - make report
  - make conformance
issue: RM-012, RM-013 (trilha-cloud/docs/ROADMAP-STACK.md); DV-01, DV-02, DV-03, DV-04, DV-12 (trilha-cloud/docs/PLANO-DEV3.md)
---

# Fase A do plano dev-3.0 no runner

## Por quê

O plano `trilha-cloud/docs/PLANO-DEV3.md` traz para a stack o "laço curto" entre a pessoa e o
agente que o dev-3.0 tem e o trilha não tinha: saber o que o agente está fazendo, deixá-lo
perguntar em vez de chutar, responder sem recomeçar do zero e guardar memória da task entre
runs. O protocolo 0.4 (`trilha-spec`, spec 016, commit `9cb760d`) já define os campos; esta spec
é o lado do runner da fase A (DV-01, DV-02, DV-03, DV-04, DV-12), junto com o que o roadmap
da stack pede antes disso: heartbeat durante a run (RM-013) e o lado do runner do lease (RM-012).

Hoje: o worker só manda heartbeat nas transições, e uma run de 20 min o deixa "offline"; se
`POST /result` falha o worker sai e o resultado se perde; uma task deixada em `running` por um
worker que caiu nunca mais roda; o cloud só vê a fase genérica do run; o agente sem informação
chuta ou falha; cada run recomeça sem saber o que a anterior descobriu.

## O que muda

- **A1 — Heartbeat periódico (RM-013):** enquanto houver run ativa, `running n/N` a cada 30 s,
  com relógio injetável.
- **A2 — Outbox e retomada (RM-012, lado runner):** o resultado vai para
  `.trilha/runs/<run-id>/result.json` no checkout da run **antes** do `POST /result`; 2xx e 409
  limpam, 4xx definitivo vira `result.rejected.json`, o resto é repetido com backoff sem
  derrubar o worker; pendências são enviadas na subida, antes de reivindicar. A run marca a task
  com `runner.pid`; task em `running` sem processo vivo volta a `ready` com nota.
- **A3 — Estado do agente (DV-01):** `trilha-runner hook claude` lê o JSON do hook, traduz para
  `execution.AgentEvent` e entrega ao runner por socket Unix (`.trilha/runs/<task>/agent.sock`,
  `TRILHA_RUN_SOCKET`, `TRILHA_RUN_ID`); sai 0 em menos de 1 s sempre. Para `claude-code`, um
  `claude-settings.json` ao lado do worktree com hooks `PreToolUse`, `PostToolUse`, `Stop`,
  `StopFailure` e `--settings` no argv fixo; para `ai`, eventos no próprio loop. O runner aplica
  `execution.NextAgentState` e manda `Progress{agent_state}` só na mudança, no máximo um a cada 2 s.
- **A4 — Pergunta e atenção (DV-02):** `trilha-runner ask` (no PATH do agente) e a ferramenta
  `ask` do `ai`: ao fim do turno a run termina `needs_input` com `question` (limite e varredura
  de segredos; com segredo, aviso fixo), sem tentativa falha nem retry, task `blocked` local.
  `trilha-runner attention "<motivo>"` / `--clear` mandam `Progress.attention`. O pacote de
  contexto ganha o parágrafo fixo de como perguntar.
- **A5 — Continuação (DV-03):** com `continuation` da mesma sessão claude-code,
  `claude -p --resume <id>` com a resposta citada; senão, sessão nova com pergunta e resposta
  citadas no pacote. Sessão nomeada com `--session-id <uuid>` e devolvida em `session`.
- **A6 — Memória (DV-04):** `trilha-runner note` / `overview` → `notes[]` (fonte `ai`) e
  `overview` no resultado; o `context` da reivindicação entra no pacote com `QuoteContext`.
- **A7 — Contagens (DV-12):** `diff_stats` de `git diff --numstat <HEAD do checkout>...HEAD`.
- **Contrato:** fixture `needs_input` copiada byte a byte, `make conformance` compara as três.

## Fora de escopo

- Fase B: artefatos de relatório, revisão por IA (fix vs report), caçadores, variantes
  (`review_of`, `hunt_seed`, `variant_*` são ignorados na decodificação).
- O lado do cloud (lease, reclaim, raia "Precisa de você", resposta com papel) — outro agente,
  em `trilha-cloud`.
- O capítulo do site em `emersonjoe/trilha` — outro passo.
- Socket e hooks dentro do sandbox Docker: o agente em container não alcança o socket do host;
  ali o estado fica nulo e `ask` não está disponível.

## Aceitação

- **SC-001** Run simulada de 100 s: ≥ 3 heartbeats `running 1/1` além das transições
  (`TestWorkerHeartbeatsWhileARunIsActive`).
- **SC-002** `POST /result` 500, 500, 202: resultado em disco antes do primeiro POST, um único
  aceito, outbox limpa; pendências enviadas na subida (`TestWorkerResultSurvivesTwoFailedDeliveries`,
  `TestWorkerResendsPendingResultsAtStart`); task órfã retomada (`TestRunResumesATaskLeftRunningWithNoLiveProcess`).
- **SC-003** Claude falso que lê `--settings`: o servidor vê `working → idle`; settings fora do
  `git status` da worktree (`TestClaudeHooksReachTheCloudAsAgentState`); hook sai 0 em < 1 s com
  socket fechado (`TestHookAlwaysExitsZeroWithinASecond`); tabela de eventos e limitação a 2 s.
- **SC-004** Agente que chama `ask` termina `needs_input` com a pergunta, sem tentativa falha
  (`TestAgentQuestionEndsTheRunNeedingInput`, `TestAgentCommandsReachTheCloudAndTheAnswerContinues`);
  token sintético na pergunta chega como aviso fixo (`TestAgentQuestionWithASecretIsRedacted`).
- **SC-005** Continuação do mesmo driver usa `--resume` com o id devolvido; outro driver ou
  resume recusado cai no prompt normal com pergunta e resposta citadas
  (`TestContinuationResumesTheSameClaudeSession`, `TestContinuationWithoutAResumableSessionStartsFresh`).
- **SC-006** Nota maliciosa sintética chega ao pacote prefixada por `> ` dentro do bloco rotulado
  (`TestTaskMemoryAndAnswerAreQuotedIntoTheContextPack`).
- **SC-007** `diff_stats` com valores conhecidos; binário conta 0/0 (`TestNumStatCountsWhatTheBranchChanged`).
- **SC-008** `make ci`, `make report` (cobertura ≥ piso) e `make conformance` verdes.
- **SC-009** Opcional: `TRILHA_E2E_OLLAMA=1` com `qwen2.5:7b` mostra pergunta → resposta → check.

## Impacto de segurança

- **Ativos:** a credencial do projeto e quaisquer segredos que o agente veja; o checkout do
  mantenedor; o contrato de fio runner ↔ cloud; a evidência.
- **Fronteiras de confiança:** agente → runner (socket Unix local, modo 0600, mensagens de no
  máximo 64 KiB e 10 000 por run, confirmadas); runner → cloud (pergunta, nota, overview e motivo
  limitados e varridos; `diff_stats` só contagens; nada de código); cloud → runner (`continuation`
  e `context` citados e rotulados como informação, nunca instrução; `session_id` só vai para o
  argv se for UUID); runner → Claude Code (settings fora do worktree, nunca commitado; libera só
  `Bash(trilha-runner ask|note|overview|attention:*)`; o comando do hook é o caminho absoluto do
  binário entre aspas simples).
- **Controles NIST SSDF 1.1:** PW.1 (ameaças acima registradas antes do código), PW.5/PW.7
  (revisão e testes de unidade, integração e E2E por nível), PW.8 (testes de regressão: ambiente
  com `USER`, outbox, retomada), RV.1 (falhas encontradas no caso ao vivo viraram teste).
- **Controles OWASP ASVS 5.0 L2 tocados:** V5 (validação de entrada: limites do protocolo,
  UUID, tipos de mensagem conhecidos), V7.1 (logs e evidência sem segredo: varredura e aviso
  fixo; a pergunta original fica só em `question.txt` 0600), V13 (o canal local não aceita
  comando, só dados), V14.2 (sem dependência nova; `trilha-spec` fixado por pseudo-versão).
- **Risco residual:** a varredura de segredos reconhece a credencial da run e formatos comuns, não
  todo segredo possível; o socket aceita qualquer processo do mesmo usuário durante a run (o
  pior caso é uma nota ou pergunta falsa, que chega citada e limitada); o `git status` do
  checkout do mantenedor continua mostrando nada porque `.trilha/runs/` é ignorado.

## Evidência

- `make ci` — gofmt, vet, suíte inteira e detector de corrida.
- `make report` — números e cobertura contra `testdata/coverage-floor.txt`.
- `make conformance` — as três fixtures idênticas às do `trilha-spec` (branch `protocol-0.4`).
- `TRILHA_E2E_OLLAMA=1 go test ./cmd/trilha-runner -run TestLiveOllama -v` — caso ao vivo opcional.
