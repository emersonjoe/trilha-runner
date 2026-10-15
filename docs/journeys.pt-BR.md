# Jornadas de usuário e casos de teste

> [🇺🇸 English](journeys.md) · 🇧🇷 Português

O que as pessoas fazem com o `trilha-runner`, o que quebra para elas se falhar, e o teste que
prova cada caso. Em ordem de risco. Como os níveis funcionam está em
[testing.pt-BR.md](testing.pt-BR.md).

Níveis: **U** unidade · **I** integração · **E** ponta a ponta (binário da CLI).
Caso marcado **lacuna** ainda não tem teste automatizado; o motivo está ao lado.

---

## J1 — Rodar uma task pronta localmente · crítica

*Quem mantém o projeto roda `trilha-runner next` (ou `run TASK-NNN`) e recebe um branch com o
trabalho do agente, verificado, com evidência, e a task em `review`.*
Pior impacto: trabalho cai na cópia do mantenedor, ou uma task chega a `review` sem ter rodado
os checks.

**J1.1 Caminho feliz até review** — E `TestRunEcho`, I `TestRunPipeline`
- Pré-condições: repositório git com um commit; `.trilha/` com uma task `ready` cujo check é
  `test -f TRILHA_RUN.md`.
- Dados: task `TASK-001`, driver `echo`.
- Passos: `trilha-runner next --driver echo`.
- Esperado: saída `TASK-001 → review` e `branch trilha/task-001`; o arquivo existe só em
  `.trilha/runs/TASK-001/wt`; três registros de evidência (dois checks, um run com commit e
  driver); `next` de novo responde que não há o que rodar; `run` de novo recusa task que não
  está `ready`.

**J1.2 A cópia do mantenedor nunca muda** — I `TestCreateCommitLeavesTheCheckoutAlone`,
`TestRunPipeline`
- Pré-condições: repositório em `main` no commit *H*.
- Passos: criar o worktree da task, escrever um arquivo, commitar.
- Esperado: o commit está em `trilha/task-001`, com autor `trilha-runner`; `main` continua
  em *H*; o arquivo não existe na cópia.

**J1.3 Rodar de novo reaproveita o branch** — I `TestCreateReusesWorktreeAndBranch`, `TestRunFailsChecks`
- Pré-condições: task que já rodou e tem commit no branch.
- Passos: rodar de novo; ou `worktree clean` e rodar de novo.
- Esperado: o mesmo caminho de worktree; depois do clean o branch mantém o commit e o novo
  checkout parte dele.

**J1.4 Limpeza** — E `TestRunEcho`, I `TestListOnlyCheckouts`, `TestRemoveDirectoryGitDoesNotKnow`
- Passos: `trilha-runner worktree list`, depois `worktree clean TASK-001`.
- Esperado: list mostra `trilha/task-001` (e só diretórios que são checkouts); clean
  responde `removed`, mantém o branch e também limpa diretório de execução que o git não
  conhece.

## J2 — Execução que falha ainda deixa evidência · crítica

*O agente quebra, os checks falham ou o provedor é proibido. Quem mantém vê o porquê.*
Pior impacto: execução falha sem rastro, ou falha reportada como sucesso.

**J2.1 O agente sai com código ≠ 0** — E `TestRunFailingAgentLeavesEvidence`, I `TestExecDriverFailureIsRecorded`
- Pré-condições: uma task `ready`.
- Dados: `--driver exec --cmd 'sh -c "echo boom; exit 9"' --attempts 1 --json`.
- Esperado: a CLI sai com código ≠ 0; o JSON diz `status: failed`,
  `failure_class: execution`; o arquivo da task diz `failed`; um registro `run` com estágio
  `execution`; `agent.log` tem `boom`; nada fora de `.trilha/` mudou na cópia.

**J2.2 Os checks falham** — I `TestRunFailsChecks`
- Dados: check `sh -c "exit 1"`.
- Esperado: `failed`, `passed=false`.

**J2.3 Eval abaixo do limite reprova mesmo com o comando saindo zero** —
I `TestVerificationRecordsEvalAndFailsThreshold`
- Dados: check que imprime `{"metric":…,"value":…,"threshold":…,"comparator":">="}` abaixo
  do limite.
- Esperado: registro `eval` reprovado; a task não chega a `review`.

**J2.4 Provedor proibido para antes de qualquer chamada, sem retry** — I `TestPolicyFailureIsRecordedWithoutRetry`,
`TestAIRejectsForbiddenHostBeforeCallingProvider`
- Dados: base URL fora dos hosts permitidos.
- Esperado: uma tentativa, classe de falha `policy`, evidência sem a credencial.

**J2.5 `exec` sem comando** — I `TestExecWithoutCommandIsRecorded`
- Pré-condições: uma task `ready`, sem manifesto de agente, sem `--cmd`.
- Esperado: erro `no command`; uma tentativa, sem retry; task `failed`; um registro `run`
  com estágio `execution` e driver `exec`.

**J2.6 Não é repositório git / driver desconhecido** — I `TestNotAGitRepositoryIsRecorded`,
`TestUnknownDriverIsRecorded`, U `TestPrintResultWithoutWorktree`
- Pré-condições: uma task `ready` num `.trilha/` que não é repositório git; ou uma task cujo
  manifesto de agente diz `driver: nope`.
- Passos: `trilha-runner run TASK-001`.
- Esperado: a CLI sai com código ≠ 0 e o motivo; a task vai `ready → running → failed`; um
  registro `run` com estágio `worktree` (sem git) ou `driver` (driver desconhecido) e o nome
  do driver, com a nota citando o estágio uma vez só; nenhum worktree é criado; a saída não
  tem linha `branch`, porque não há branch.

**J2.7 Task bloqueada não é execução que falhou** — I `TestBlockedTaskIsNotFailed`
- Pré-condições: uma task `ready` que depende de outra que não está `done`.
- Esperado: erro citando a dependência; a task continua `ready`; nenhuma evidência.

## J3 — Segredos nunca vazam · crítica

*A credencial do projeto chega ao agente e a mais nenhum lugar.*
Pior impacto: token visível no `ps`, na evidência, num log ou enviado ao cloud.

**J3.1 A credencial do claude-code é redigida da saída** — U `TestClaudeCodeUsesFixedArgvAndRedactsCredential`,
`TestClaudeCodeOAuthProviderSelectsTokenAndDropsBadModel`
**J3.2 Nenhum segredo em linha de comando do host no sandbox** — I `TestSandboxedCommandCarriesNoCredential`,
`TestSecretNeverReachesACommandLine`, `TestEnvironmentValueWithANewlineIsRefused`
**J3.3 Segredos de deploy em lista permitida e redigidos** — I `TestExecuteUsesAllowListAndRedactsSecrets`
**J3.4 Só variáveis de credencial conhecidas são repassadas** — U `TestCredentialEnv`

## J4 — Dependências entre repositórios · alta

*Uma task depende de `trilha:TASK-001` num repositório irmão.*
Pior impacto: a task roda antes do que depende dela estar pronto, ou espera para sempre.

**J4.1 Não resolvida, depois resolvida** — E `TestNextResolvesSiblingRepository`, I `TestLocalResolvesCrossRepositoryDependency`
- Passos: `next --driver echo` sem `--repo`; depois com `--repo trilha=<caminho>` onde a
  dependência está `done`.
- Esperado: primeiro saída ≠ 0 citando `waiting:trilha:TASK-001`; depois `TASK-001 → review`.

**J4.2 Regras** — U `TestParseRepository`, `TestBlocker`, `TestParseKeepsCrossRepositoryDependencies`,
`TestMoveWithCrossRepositoryDependencyKeepsIt`, `TestRemoteDependencyStatus`
- Esperado: `--repo` precisa ser `alias=caminho` sem `:` no alias; dependência local fora de
  `done` bloqueia; alias que não aponta para nada espera; mover a task mantém a dependência
  remota no arquivo.

## J5 — Worker e trilha-cloud · alta

*Um worker reivindica execuções, executa e reporta progresso, resultado e evidência.*
Pior impacto: execuções travadas no cloud, reivindicação acima da capacidade, ou resultado
que o cloud não lê.

**J5.1 O contrato HTTP** — I `TestRemote`, U `TestExecutionV1FixtureMatchesRemoteResult`
- Esperado: `Authorization: Bearer`; 204 é fila vazia; o resultado bate com a fixture
  `execution-v1` do `trilha-spec`.

**J5.2 Capacidade** — I `TestWorkerCapacityClaimsThirdOnlyAfterOneFinishes`
**J5.3 Progresso é best effort** — I `TestWorkerReportsProgressBestEffort`
**J5.4 Worker que não consegue começar avisa no heartbeat** — I `TestWorkerReportsStartupFailureAsHeartbeat`

**J5.5 Bundle em que o worker não pode confiar é recusado** — I `TestRemoteBundle`
- Dados: bundles de outra task, outro projeto, versão desconhecida, ou que não são JSON; um 404.
- Esperado: `invalid work bundle` nos três primeiros; 404 é `ErrNoBundle` (execução de antes
  das specs gerenciadas pelo Cloud, executada como está); 5xx cita o status.

**J5.6 Progresso chega à sua rota** — I `TestRemoteProgress`
- Esperado: `POST /api/runs/{id}/progress` com o evento como enviado; 502 é um erro que o
  worker ignora (J5.3).

**J5.7 Dependências de outro projeto num bundle** — I `TestRemoteDependencyBlocker`
- Esperado: só dependências `alias:TASK-NNN` são consultadas, com `requesting_project`; a
  primeira que não está `done` — ou é desconhecida, ou o cloud está fora — responde
  `waiting:alias:TASK-NNN` e para de consultar.

**J5.8 Deploys: reivindicar e reportar** — I `TestRemoteNextDeployment`, `TestRemoteDoneDeploymentSendsNoSecret`
- Esperado: a reivindicação leva worker e projeto; 204 é fila vazia; trabalho de outro
  projeto, ou sem id ou perfil, é recusado; o relatório sempre leva este worker e projeto
  (valor posto por quem chama é sobrescrito) e nunca os segredos da reivindicação.

## J6 — Trabalho criado na UI (bundles) · alta

*O worker baixa um bundle, escreve spec e tasks em `.trilha/` e publica o branch.*

Pior impacto: spec ou task do próprio mantenedor sobrescrita, ou uma rodada que para de
publicar depois da primeira task.

**J6.1 Clonar e materializar** — I `TestEnsureRepositoryAndMaterializeBundle`

**J6.2 O checkout é do runner, e só para o seu remoto** — I `TestEnsureRepositoryReusesAndGuardsTheCheckout`
- Esperado: sobras (arquivo sujo, outro branch) voltam ao branch padrão do remoto; o mesmo
  nome de projeto apontando para outro remoto é recusado; nomes de projeto que escapam do
  workspace, e remotos ou branches com quebra de linha ou sintaxe de revisão, são recusados.

**J6.3 Arquivos divergentes são recusados, nada é publicado** — I `TestMaterializeRefusesDivergentFiles`
- Dados: na main do remoto, uma spec com outro `cloud_id`; uma `TASK-001` escrita à mão; uma
  `TASK-001` de outra especificação do Cloud.
- Esperado: o bundle é recusado com o motivo; o remoto não ganha branch novo.

**J6.4 O mesmo bundle de novo é aceito** — I `TestMaterializeSameBundleTwice`

**J6.5 Toda task da rodada publica** — I `TestMaterializeSecondTaskOfTheRoundPushes`,
`TestPushBranch`, `TestCheckoutFromBeforeTrackingRecovers`
- Pré-condições: worker com `--push`; rodada com duas tasks.
- Passos: materializar e publicar para TASK-001; reiniciar o checkout; materializar e publicar
  para TASK-002. À parte: publicar o branch da task, reescrevê-lo, publicar de novo; depois
  outra pessoa publica nele, e o runner publica mais uma vez.
- Esperado: o segundo push do mesmo branch funciona; branch que outra pessoa moveu não é
  sobrescrito; checkout clonado por versão anterior adota os branches já publicados na primeira
  vez que é reaproveitado, e o refspec é adicionado uma vez só.

Questão em aberto: uma spec com o mesmo id e **sem** `cloud_id` (escrita à mão) é substituída
no branch da spec, enquanto uma task escrita à mão é recusada. A assimetria não é testada em
nenhum sentido até ser decidida.

## J7 — Sandbox Docker · alta

*Agente e checks rodam num container com os serviços do projeto.*
Pior impacto: o agente escapa do worktree, roda como root, ou containers ficam para trás.

**J7.1 Serviços, prontidão e remoção** — I `TestDockerPostgresAndTeardown` (pulado sem Docker)
**J7.2 Menor privilégio** — I `TestAgentDropsCapabilitiesAndRunsAsTheWorktreeOwner`
**J7.3 Container que morre é reportado com a própria saída** — I `TestContainerThatDidNotStayUpIsReported`,
`TestServiceThatExitsIsCaughtBeforeReadiness`
**J7.4 Parse do manifesto e sem sandbox** — U `TestFromAgent`, `TestNoneRunsInPlace`

## J8 — Deploy e rollback · alta

*Um perfil de entrega faz deploy, migra, checa saúde e reverte.*
Pior impacto: troca de versão depois de migração falha, ou nenhum rollback depois de health
check falho.

**J8.1 Migração falha → aborta antes da troca** — I `TestMigrationFailureAbortsBeforeSwitch`
**J8.2 Health falha → rollback só da imagem** — I `TestHealthFailureRollsBackImageOnly`
**J8.3 Perfil desconhecido recusado** — I `TestExecuteRejectsUnknownProfile`
**J8.4 O arquivo de perfis diz exatamente o que o operador quis** — U `TestLoadShippedExample`, `TestLoadRefuses`
- Esperado: `deploy/eoslab/delivery.json.example` carrega como publicado; campo com erro de
  digitação, arquivo que não é JSON, ou sem perfis, é recusado na partida.

## J9 — Falhas transitórias do provedor · média

*O provedor do modelo responde 429/503 ou a conexão cai; a execução tenta de novo e segue.*

**J9.1 Classificação e backoff** — U `TestClassifyRetry`, `TestRetryDelay`, `TestWaitRetryStopsOnCancel`
- Esperado: timeout, reset, 429 e 5xx tentam de novo; 401/403/quota/permission não;
  `Retry-After` até 300 s é respeitado; o backoff dobra até 16× a base.

**J9.2 O checkpoint leva o que a próxima tentativa precisa** — U `TestCheckpointPreservesRecoveryStateAndDiff`

**J9.3 A execução tenta de novo e segue** — I `TestTransientProviderFailureIsRetried`,
`TestPermanentProviderFailureIsNotRetried`, `TestTransientFailuresExhaustAttempts`,
`TestFailedChecksGetACorrectiveAttempt`
- Dados: driver roteirizado que responde `HTTP 503` uma vez e depois funciona; `HTTP 401`;
  `connection reset` em toda tentativa; checks que falham só na primeira tentativa.
- Esperado: 503 → evento `retry_wait`, segunda tentativa que recebe attempt 2 e um checkpoint
  citando o 503, review com tentativas `recovered`/`passed` e evidência das duas; 401 → uma
  tentativa, `provider_permanent`; resets → tantas tentativas quanto permitido, depois
  `failed`; checks falhos → tentativa corretiva que chega a review.

## J10 — Drivers · média

**J10.1 `echo`, `exec`, ferramentas cercadas no worktree** — U/I `TestEcho`, `TestExec`, `TestToolsStayInside`
**J10.2 `drivers` lista todos** — E `TestRunEcho`

## J11 — Release · baixa

**J11.1 O binário reporta a versão que foi publicada** — U `TestVersionMatchesTheNewestChangelogEntry`
