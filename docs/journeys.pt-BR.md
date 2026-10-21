# Jornadas de usuário e casos de teste

> [🇺🇸 English](journeys.md) · 🇧🇷 Português

O que as pessoas fazem com o `trilha-runner`, o que quebra para elas se falhar, e o teste que
prova cada caso. Em ordem de risco. Como os níveis funcionam está em
[testing.pt-BR.md](testing.pt-BR.md).

Níveis: **U** unidade · **I** integração · **E** ponta a ponta (binário da CLI).
Todo caso diz de que **recursos** precisa (`git`, `sh`, `httptest`, `Docker`, o binário
compilado, ou nenhum), suas **pré-condições**, **dados**, **passos** e o resultado
**esperado**. O `internal/catalog` reprova a suíte quando um caso está incompleto, cita um
teste que não existe, quando um teste não pertence a caso nenhum — e quando as duas línguas
discordam.

---

## J1 — Rodar uma task pronta localmente · crítica

*Quem mantém o projeto roda `trilha-runner next` (ou `run TASK-NNN`) e recebe um branch com o
trabalho do agente, verificado, com evidência, e a task em `review`.*
Pior impacto: trabalho cai na cópia do mantenedor, ou uma task chega a `review` sem ter rodado
os checks.

**J1.1 Caminho feliz até review** — E `TestRunEcho`, I `TestRunPipeline`, U `TestLocal`
- Recursos: git, sh, binário compilado.
- Pré-condições: repositório git com um commit; `.trilha/` com uma task `ready` cujo check é
  `test -f TRILHA_RUN.md`.
- Dados: task `TASK-001`, driver `echo`.
- Passos: `trilha-runner next --driver echo`.
- Esperado: saída `TASK-001 → review` e `branch trilha/task-001`; o arquivo existe só em
  `.trilha/runs/TASK-001/wt`; três registros de evidência (dois checks, um run com commit e
  driver); `next` de novo responde que não há o que rodar; `run` de novo recusa task que não
  está `ready`; sem task `ready`, a fila está vazia.

**J1.2 A cópia do mantenedor nunca muda** — I `TestCreateCommitLeavesTheCheckoutAlone`,
`TestRunPipeline`, U `TestBranchAndPath`
- Recursos: git.
- Pré-condições: repositório em `main` no commit *H*.
- Dados: task `TASK-001`; um arquivo `work.txt`.
- Passos: criar o worktree da task, escrever o arquivo, commitar.
- Esperado: o worktree é `.trilha/runs/TASK-001/wt` em `trilha/task-001`; o commit tem autor
  `trilha-runner`; `main` continua em *H*; o arquivo não existe na cópia; sem mudança, nenhum
  commit é feito.

**J1.3 Rodar de novo reaproveita o branch** — I `TestCreateReusesWorktreeAndBranch`, `TestRunFailsChecks`
- Recursos: git, sh.
- Pré-condições: task que já rodou e tem commit no branch.
- Dados: a mesma `TASK-001`.
- Passos: rodar de novo; ou `worktree clean` e rodar de novo.
- Esperado: o mesmo caminho de worktree; depois do clean o branch mantém o commit e o novo
  checkout parte dele.

**J1.4 Limpeza** — E `TestRunEcho`, I `TestListOnlyCheckouts`, `TestRemoveDirectoryGitDoesNotKnow`
- Recursos: git, binário compilado.
- Pré-condições: uma task que rodou; um diretório de execução só com evidência; um diretório
  solto que o git não conhece.
- Dados: `TASK-001`, `TASK-002`, `TASK-003`.
- Passos: `trilha-runner worktree list`, depois `worktree clean TASK-001`.
- Esperado: list mostra `trilha/task-001` (e só diretórios que são checkouts); clean
  responde `removed`, mantém o branch e também limpa diretório de execução que o git não
  conhece.

## J2 — Execução que falha ainda deixa evidência · crítica

*O agente quebra, os checks falham ou o provedor é proibido. Quem mantém vê o porquê.*
Pior impacto: execução falha sem rastro, ou falha reportada como sucesso.

**J2.1 O agente sai com código ≠ 0** — E `TestRunFailingAgentLeavesEvidence`, I `TestExecDriverFailureIsRecorded`
- Recursos: git, sh, binário compilado.
- Pré-condições: uma task `ready`.
- Dados: `--driver exec --cmd 'sh -c "echo boom; exit 9"' --attempts 1 --json`.
- Passos: `trilha-runner run TASK-001` com os dados acima.
- Esperado: a CLI sai com código ≠ 0; o JSON diz `status: failed`,
  `failure_class: execution`; o arquivo da task diz `failed`; um registro `run` com estágio
  `execution`; `agent.log` tem `boom`; nada fora de `.trilha/` mudou na cópia.

**J2.2 Os checks falham** — I `TestRunFailsChecks`
- Recursos: git, sh.
- Pré-condições: uma task `ready`; driver `echo`.
- Dados: check `sh -c "exit 1"`.
- Passos: rodar a task.
- Esperado: depois das tentativas permitidas, `failed`, `passed=false`.

**J2.3 Eval abaixo do limite reprova mesmo com o comando saindo zero** —
I `TestVerificationRecordsEvalAndFailsThreshold`
- Recursos: git, sh.
- Pré-condições: uma task `ready` cujo check imprime uma linha de métrica e sai zero.
- Dados: `{"metric":…,"value":…,"threshold":…,"comparator":">="}` abaixo do limite.
- Passos: rodar a task.
- Esperado: registro `eval` reprovado; a task não chega a `review`.

**J2.4 Provedor proibido para antes de qualquer chamada, sem retry** — I `TestPolicyFailureIsRecordedWithoutRetry`,
U `TestAIRejectsForbiddenHostBeforeCallingProvider`
- Recursos: git.
- Pré-condições: uma task `ready`; driver `ai` com política de provedor.
- Dados: base URL `https://outside.example/v1`, host permitido `gateway.example.br`,
  credencial `secret`.
- Passos: rodar a task.
- Esperado: uma tentativa, classe de falha `policy`, nenhuma chamada ao provedor, evidência
  sem a credencial.

**J2.5 `exec` sem comando** — I `TestExecWithoutCommandIsRecorded`
- Recursos: git.
- Pré-condições: uma task `ready`, sem manifesto de agente, sem `--cmd`.
- Dados: nenhum.
- Passos: rodar a task.
- Esperado: erro `no command`; uma tentativa, sem retry; task `failed`; um registro `run`
  com estágio `execution` e driver `exec`.

**J2.6 Não é repositório git / driver desconhecido** — I `TestNotAGitRepositoryIsRecorded`,
`TestUnknownDriverIsRecorded`, `TestIsRepo`, U `TestPrintResultWithoutWorktree`
- Recursos: git (para o segundo caso).
- Pré-condições: uma task `ready` num `.trilha/` que não é repositório git (ou repositório
  sem commit); ou uma task cujo manifesto de agente diz `driver: nope`.
- Dados: `TASK-001`.
- Passos: `trilha-runner run TASK-001`.
- Esperado: a CLI sai com código ≠ 0 e o motivo; a task vai `ready → running → failed`; um
  registro `run` com estágio `worktree` (sem git) ou `driver` (driver desconhecido) e o nome
  do driver, com a nota citando o estágio uma vez só; nenhum worktree é criado; a saída não
  tem linha `branch`, porque não há branch.

**J2.7 Task bloqueada não é execução que falhou** — I `TestBlockedTaskIsNotFailed`
- Recursos: git.
- Pré-condições: uma task `ready` que depende de outra que não está `done`.
- Dados: `TASK-002` depende de `TASK-001`.
- Passos: rodar `TASK-002`.
- Esperado: erro citando a dependência; a task continua `ready`; nenhuma evidência.

## J3 — Segredos nunca vazam · crítica

*A credencial do projeto chega ao agente e a mais nenhum lugar.*
Pior impacto: token visível no `ps`, na evidência, num log ou enviado ao cloud.

**J3.1 A credencial do claude-code é redigida e roteada** — U `TestClaudeCodeUsesFixedArgvAndRedactsCredential`,
`TestClaudeCodeOAuthProviderSelectsTokenAndDropsBadModel`
- Recursos: sh (um `claude` falso no `PATH`).
- Pré-condições: uma reivindicação com provedor `claude-code` ou `claude-code-oauth`.
- Dados: uma credencial inventada; um id de modelo malformado.
- Passos: executar o driver preset; o `claude` falso ecoa argv e ambiente.
- Esperado: argv é o fixo `claude -p - --permission-mode acceptEdits`; chave de API vai para
  `ANTHROPIC_API_KEY`, token OAuth para `CLAUDE_CODE_OAUTH_TOKEN`; o modelo malformado nunca
  chega ao argv; a credencial aparece como `[REDACTED]` na saída capturada.

**J3.2 Nenhum segredo em linha de comando do host no sandbox** — I `TestSandboxedCommandCarriesNoCredential`,
`TestSecretNeverReachesACommandLine`, `TestEnvironmentValueWithANewlineIsRefused`
- Recursos: sh, binário docker falso.
- Pré-condições: uma execução em sandbox; um `docker` falso que registra cada chamada.
- Dados: uma credencial no ambiente da execução; um valor com quebra de linha.
- Passos: preparar o sandbox e executar o agente por ele.
- Esperado: nenhuma chamada `docker` leva a credencial ou qualquer `KEY=VALUE`; o ambiente
  entra uma vez por um `--env-file` `0600` removido no teardown; valor com quebra de linha é
  recusado.

**J3.3 Segredos de deploy em lista permitida e redigidos** — I `TestExecuteUsesAllowListAndRedactsSecrets`
- Recursos: sh.
- Pré-condições: um perfil cujo script de deploy imprime a revisão e `DATABASE_URL`.
- Dados: segredo `DATABASE_URL=private-value`, revisão `abc123`.
- Passos: executar o deploy.
- Esperado: passa; o log tem `[REDACTED]` e nunca `private-value`.

**J3.4 Só variáveis de credencial conhecidas são repassadas** — U `TestCredentialEnv`
- Recursos: nenhum.
- Pré-condições: nenhuma.
- Dados: sem configuração; configuração sem credencial; uma chave de API; um token OAuth.
- Passos: pedir o ambiente de credencial.
- Esperado: nada, nada, `ANTHROPIC_API_KEY=…`, `CLAUDE_CODE_OAUTH_TOKEN=…`.

## J4 — Dependências entre repositórios · alta

*Uma task depende de `trilha:TASK-001` num repositório irmão.*
Pior impacto: a task roda antes do que depende dela estar pronto, ou espera para sempre.

**J4.1 Não resolvida, depois resolvida** — E `TestNextResolvesSiblingRepository`, I `TestLocalResolvesCrossRepositoryDependency`
- Recursos: git, sh, binário compilado.
- Pré-condições: um `.trilha/` irmão onde `TASK-001` está `done`; a task principal depende de
  `trilha:TASK-001`.
- Dados: `--repo trilha=<caminho>`.
- Passos: `next --driver echo` sem `--repo`; depois com ele.
- Esperado: primeiro saída ≠ 0 citando `waiting:trilha:TASK-001`; depois `TASK-001 → review`.

**J4.2 Regras** — U `TestParseRepository`, `TestBlocker`, `TestParseKeepsCrossRepositoryDependencies`,
`TestParseLocalOnlyIsTheProtocolParse`, `TestGetAndList`, `TestMoveWithCrossRepositoryDependencyKeepsIt`,
`TestRemoteDependencyStatus`
- Recursos: nenhum (arquivos de task num diretório temporário).
- Pré-condições: tasks com dependências locais e `alias:`.
- Dados: valores de `--repo`; arquivos de task cujo nome e id discordam.
- Passos: ler flags e tasks, pedir os bloqueios, mover uma task.
- Esperado: `--repo` precisa ser `alias=caminho` sem `:` no alias; dependência local fora de
  `done` bloqueia; alias que não aponta para nada espera; mover a task mantém a dependência
  remota no arquivo; arquivo cujo nome discorda do id é recusado.

## J5 — Worker e trilha-cloud · alta

*Um worker reivindica execuções, executa e reporta progresso, resultado e evidência.*
Pior impacto: execuções travadas no cloud, reivindicação acima da capacidade, ou resultado
que o cloud não lê.

**J5.1 O contrato HTTP** — I `TestRemote`, U `TestExecutionV1FixtureMatchesRemoteResult`
- Recursos: httptest.
- Pré-condições: um control plane falso.
- Dados: uma reivindicação, um resultado, um heartbeat; a fixture `execution-v1`.
- Passos: reivindicar, reportar, heartbeat.
- Esperado: `Authorization: Bearer`; 204 é fila vazia; o resultado bate com a fixture
  `execution-v1` do `trilha-spec` (copiada byte a byte, `make conformance`, também um job de CI),
  volta campo a campo depois de uma ida e volta, e uma tentativa mantém seu custo sob os nomes do
  protocolo: `provider`, `tokens_in`, `tokens_out`, `cost`, `currency`.

**J5.2 Capacidade** — I `TestWorkerCapacityClaimsThirdOnlyAfterOneFinishes`
- Recursos: httptest.
- Pré-condições: worker com `--capacity 2`; control plane com três execuções.
- Dados: execuções com dependência em espera (terminam na hora).
- Passos: rodar o laço do worker.
- Esperado: as reivindicações informam `running` 0, depois 1; a terceira só acontece depois
  de um resultado publicado; três resultados ao todo.

**J5.3 Progresso é best effort** — I `TestWorkerReportsProgressBestEffort`
- Recursos: git, sh, httptest.
- Pré-condições: projeto com uma task `ready`; control plane cuja rota de progresso responde 500.
- Dados: uma execução reivindicada sem bundle.
- Passos: rodar o worker.
- Esperado: `POST …/progress` com `consulting_ai` e attempt 1 é enviado; a execução termina e
  o `POST …/result` ainda é enviado.

**J5.4 Worker que não consegue começar avisa no heartbeat** — I `TestWorkerReportsStartupFailureAsHeartbeat`
- Recursos: httptest.
- Pré-condições: um diretório sem `.trilha/`.
- Dados: `trilha-runner worker` contra um control plane falso.
- Passos: iniciar o worker.
- Esperado: heartbeat com status `failed` e erro citando `no .trilha/`; o comando devolve erro.

**J5.5 Bundle em que o worker não pode confiar é recusado** — I `TestRemoteBundle`
- Recursos: httptest.
- Pré-condições: uma execução reivindicada.
- Dados: bundles de outra task, outro projeto, versão desconhecida, ou que não são JSON; um 404.
- Passos: pedir o bundle da execução.
- Esperado: `invalid work bundle` nos três primeiros; 404 é `ErrNoBundle` (execução de antes
  das specs gerenciadas pelo Cloud, executada como está); 5xx cita o status.

**J5.6 Progresso chega à sua rota** — I `TestRemoteProgress`
- Recursos: httptest.
- Pré-condições: uma execução reivindicada.
- Dados: um evento com fase, tentativa, branch e modelo; um 502.
- Passos: reportar progresso.
- Esperado: `POST /api/runs/{id}/progress` com o evento como enviado; 502 é um erro que o
  worker ignora (J5.3).

**J5.7 Dependências de outro projeto num bundle** — I `TestRemoteDependencyBlocker`
- Recursos: httptest.
- Pré-condições: control plane que conhece `trilha:TASK-001` (`done`) e `trilha:TASK-002` (`running`).
- Dados: listas de dependência misturando ids locais e remotos; o control plane caindo.
- Passos: perguntar se a task do bundle pode rodar.
- Esperado: só dependências `alias:TASK-NNN` são consultadas, com `requesting_project`; a
  primeira que não está `done` — ou é desconhecida, ou o cloud está fora — responde
  `waiting:alias:TASK-NNN` e para de consultar.

**J5.8 Deploys: reivindicar e reportar** — I `TestRemoteNextDeployment`, `TestRemoteDoneDeploymentSendsNoSecret`
- Recursos: httptest.
- Pré-condições: control plane com trabalho de deploy.
- Dados: trabalho deste projeto, de outro, sem id ou perfil; um segredo na reivindicação.
- Passos: reivindicar, depois reportar um resultado.
- Esperado: a reivindicação leva worker e projeto; 204 é fila vazia; trabalho de outro
  projeto, ou sem id ou perfil, é recusado; o relatório sempre leva este worker e projeto
  (valor posto por quem chama é sobrescrito) e nunca os segredos da reivindicação.

## J6 — Trabalho criado na UI (bundles) · alta

*O worker baixa um bundle, escreve spec e tasks em `.trilha/` e publica o branch.*
Pior impacto: spec ou task do próprio mantenedor sobrescrita, ou uma rodada que para de
publicar depois da primeira task.

**J6.1 Clonar e materializar** — I `TestEnsureRepositoryAndMaterializeBundle`
- Recursos: git (um repositório bare como remoto).
- Pré-condições: remoto com um commit em `main`; raiz de workspace vazia.
- Dados: bundle da spec `001-user-access`, rodada 1, `TASK-001`.
- Passos: garantir o checkout, materializar com push.
- Esperado: branch `trilha/spec-spec-1-round-001` com `project.md`, a spec e a task; o remoto
  tem o mesmo commit nesse branch.

**J6.2 O checkout é do runner, e só para o seu remoto** — I `TestEnsureRepositoryReusesAndGuardsTheCheckout`
- Recursos: git.
- Pré-condições: checkout de uma reivindicação anterior, deixado sujo em outro branch.
- Dados: o mesmo nome de projeto com outro remoto; nomes com `../`, maiúsculas, quebra de
  linha, `~1`, espaços.
- Passos: garantir o checkout de novo, depois com cada entrada ruim.
- Esperado: sobras voltam ao branch padrão do remoto; o mesmo nome de projeto apontando para
  outro remoto é recusado; nomes de projeto que escapam do workspace, e remotos ou branches
  com quebra de linha ou sintaxe de revisão, são recusados.

**J6.3 Arquivos divergentes são recusados, nada é publicado** — I `TestMaterializeRefusesDivergentFiles`
- Recursos: git.
- Pré-condições: na main do remoto, uma spec com outro `cloud_id`; uma `TASK-001` escrita à
  mão; ou uma `TASK-001` de outra especificação do Cloud.
- Dados: o bundle de exemplo.
- Passos: materializar com push.
- Esperado: o bundle é recusado com o motivo; o remoto não ganha branch novo.

**J6.4 O mesmo bundle de novo é aceito** — I `TestMaterializeSameBundleTwice`
- Recursos: git.
- Pré-condições: o bundle foi materializado e seus arquivos entraram na main do remoto.
- Dados: o mesmo bundle.
- Passos: materializar de novo.
- Esperado: nenhum erro — os arquivos têm o mesmo `cloud_id`.

**J6.5 Toda task da rodada publica** — I `TestMaterializeSecondTaskOfTheRoundPushes`,
`TestPushBranch`, `TestCheckoutFromBeforeTrackingRecovers`
- Recursos: git.
- Pré-condições: worker com `--push`; rodada com duas tasks.
- Dados: `TASK-001` e `TASK-002`; um segundo clone fazendo o papel de outra pessoa.
- Passos: materializar e publicar para TASK-001; reiniciar o checkout; materializar e publicar
  para TASK-002. À parte: publicar o branch da task, reescrevê-lo, publicar de novo; depois
  outra pessoa publica nele, e o runner publica mais uma vez.
- Esperado: o segundo push do mesmo branch funciona; branch que outra pessoa moveu não é
  sobrescrito; checkout clonado por versão anterior adota os branches já publicados na
  primeira vez que é reaproveitado, e o refspec é adicionado uma vez só.

Questão em aberto: uma spec com o mesmo id e **sem** `cloud_id` (escrita à mão) é substituída
no branch da spec, enquanto uma task escrita à mão é recusada. A assimetria não é testada em
nenhum sentido até ser decidida.

## J7 — Sandbox Docker · alta

*Agente e checks rodam num container com os serviços do projeto.*
Pior impacto: o agente escapa do worktree, roda como root, ou containers ficam para trás.

**J7.1 Serviços, prontidão e remoção** — I `TestDockerPostgresAndTeardown`
- Recursos: Docker (obrigatório no CI via `TRILHA_REQUIRE_DOCKER=1`; fora dele, sem Docker,
  é pulado dizendo por quê).
- Pré-condições: um daemon Docker acessível.
- Dados: imagem `postgres:16-alpine` com serviço `db` e prontidão `pg_isready -h localhost`
  (por TCP, a via pela qual o agente chega).
- Passos: preparar o sandbox, rodar comandos pelo prefixo, liberar.
- Esperado: `pg_isready` responde; o agente roda como o usuário do host; só o worktree é
  gravável; nenhum container ou rede sobra depois da liberação.

**J7.2 Menor privilégio** — I `TestAgentDropsCapabilitiesAndRunsAsTheWorktreeOwner`
- Recursos: sh, binário docker falso.
- Pré-condições: um `docker` falso que registra a linha `docker run`.
- Dados: um worktree do usuário atual.
- Passos: preparar o sandbox.
- Esperado: `--cap-drop ALL`, `--user` o dono do worktree, raiz somente leitura e
  `no-new-privileges`; o socket do Docker nunca é montado.

**J7.3 Container que morre é reportado com a própria saída** — I `TestContainerThatDidNotStayUpIsReported`,
`TestServiceThatExitsIsCaughtBeforeReadiness`
- Recursos: sh, binário docker falso.
- Pré-condições: um `docker` falso cujo container do agente ou serviço sai na hora.
- Dados: a própria saída do container.
- Passos: preparar o sandbox.
- Esperado: erro levando essa saída; a prontidão nunca é esperada num serviço morto; o
  container ainda é removido.

**J7.4 Parse do manifesto e sem sandbox** — U `TestFromAgent`, `TestNoneRunsInPlace`
- Recursos: nenhum.
- Pré-condições: um manifesto de agente com `sandbox:`; ou sem sandbox.
- Dados: imagem e JSON de serviços.
- Passos: montar o sandbox a partir do manifesto; preparar o `None`.
- Esperado: a imagem e o serviço `db` são lidos como escritos; o `None` roda no lugar e
  devolve o ambiente sem mudança.

## J8 — Deploy e rollback · alta

*Um perfil de entrega faz deploy, migra, checa saúde e reverte.*
Pior impacto: troca de versão depois de migração falha, ou nenhum rollback depois de health
check falho.

**J8.1 Migração falha → aborta antes da troca** — I `TestMigrationFailureAbortsBeforeSwitch`
- Recursos: sh.
- Pré-condições: perfil com passos `pull`, `switch` e um portão `migrate`.
- Dados: migração que sai com 7.
- Passos: executar um deploy.
- Esperado: ordem `pull`, `migrate` — sem `switch`; erro `switch aborted`.

**J8.2 Health falha → rollback só da imagem** — I `TestHealthFailureRollsBackImageOnly`
- Recursos: sh, httptest.
- Pré-condições: perfil com migração irreversível, rollback e health check.
- Dados: endpoint de saúde respondendo 503.
- Passos: executar um deploy.
- Esperado: ordem `pull`, `migrate`, `switch`, `rollback`; sem downgrade; o log diz
  `image-only rollback`.

**J8.3 Perfil desconhecido recusado** — I `TestExecuteRejectsUnknownProfile`
- Recursos: nenhum.
- Pré-condições: configuração sem o perfil que a reivindicação cita.
- Dados: perfil `missing`.
- Passos: executar.
- Esperado: não passa, com erro; nada roda.

**J8.4 O arquivo de perfis diz exatamente o que o operador quis** — U `TestLoadShippedExample`, `TestLoadRefuses`
- Recursos: nenhum.
- Pré-condições: o `deploy/eoslab/delivery.json.example` publicado; arquivos num diretório temporário.
- Dados: campo com erro de digitação (`rolback`), arquivo que não é JSON, sem perfis, arquivo
  ausente.
- Passos: carregar cada um.
- Esperado: o exemplo carrega como publicado; cada um dos outros é recusado na partida.

## J9 — Falhas transitórias do provedor · média

*O provedor do modelo responde 429/503 ou a conexão cai; a execução tenta de novo e segue.*
Pior impacto: execução abandonada por um soluço, ou insistir num provedor que recusou a
credencial.

**J9.1 Classificação e backoff** — U `TestClassifyRetry`, `TestRetryDelay`, `TestWaitRetryStopsOnCancel`
- Recursos: nenhum.
- Pré-condições: nenhuma.
- Dados: timeouts, resets, 429, 5xx, 401, 403, quota, permission; valores de `Retry-After`.
- Passos: classificar cada erro; calcular as esperas.
- Esperado: timeout, reset, 429 e 5xx tentam de novo; 401/403/quota/permission não;
  `Retry-After` até 300 s é respeitado; o backoff dobra até 16× a base; uma espera cancelada
  para na hora.

**J9.2 O checkpoint leva o que a próxima tentativa precisa** — U `TestCheckpointPreservesRecoveryStateAndDiff`
- Recursos: nenhum.
- Pré-condições: uma tentativa falha com estado de recuperação.
- Dados: turnos, arquivos analisados, chamadas de ferramenta, o diff stat.
- Passos: salvar o checkpoint e lê-lo de volta.
- Esperado: número da tentativa, diff e estado de recuperação são mantidos.

**J9.3 A execução tenta de novo e segue** — I `TestTransientProviderFailureIsRetried`,
`TestPermanentProviderFailureIsNotRetried`, `TestTransientFailuresExhaustAttempts`,
`TestFailedChecksGetACorrectiveAttempt`
- Recursos: git.
- Pré-condições: uma task `ready` cujo check precisa de `done.txt`; base de retry de 1 ms.
- Dados: driver roteirizado que responde `HTTP 503` uma vez e depois funciona; `HTTP 401`;
  `connection reset` em toda tentativa; checks que falham só na primeira tentativa.
- Passos: rodar a task.
- Esperado: 503 → evento `retry_wait`, segunda tentativa que recebe attempt 2 e um checkpoint
  citando o 503, review com tentativas `recovered`/`passed` e evidência das duas; 401 → uma
  tentativa, `provider_permanent`; resets → tantas tentativas quanto permitido, depois
  `failed`; checks falhos → tentativa corretiva que chega a review.

## J10 — Drivers · média

*O manifesto cita um driver; o runner inicia o agente por ele.*
Pior impacto: um agente que escreve ou executa fora do seu worktree.

**J10.1 `echo`, `exec`, ferramentas cercadas no worktree** — U `TestEcho`, I `TestExec`, `TestToolsStayInside`
- Recursos: sh.
- Pré-condições: um diretório de worktree.
- Dados: um prompt; um caminho `../escape`; um comando fora dos checks da task.
- Passos: rodar `echo`; rodar `exec` com o prompt no stdin; chamar as ferramentas do `ai`.
- Esperado: `echo` escreve a primeira linha do prompt e driver desconhecido é recusado; `exec`
  recebe o prompt e `TRILHA_TASK`; ferramentas de arquivo recusam caminhos fora do worktree;
  `run` recusa comandos fora da lista permitida.

**J10.2 `drivers` lista todos** — E `TestRunEcho`
- Recursos: binário compilado.
- Pré-condições: nenhuma.
- Dados: nenhum.
- Passos: `trilha-runner drivers`.
- Esperado: `ai`, `claude-code`, `echo`, `exec`, um por linha.

## J11 — Release · baixa

**J11.1 O binário reporta a versão que foi publicada** — U `TestVersionMatchesTheNewestChangelogEntry`
- Recursos: nenhum.
- Pré-condições: `CHANGELOG.md` com uma seção publicada.
- Dados: a constante `version`.
- Passos: comparar as duas.
- Esperado: iguais; uma seção `Unreleased` não conta.

**J11.2 Uma tag só publica a versão que nomeia** — U `TestVersionMatchesTag`
- Recursos: nenhum (o `release.yml` roda o teste antes de compilar qualquer coisa).
- Pré-condições: `release.yml` disparado por uma tag `vX.Y.Z`; `RELEASE_TAG` com essa tag.
- Dados: `RELEASE_TAG=v9.9.9` contra `version` `0.4.0`; `RELEASE_TAG=v0.4.0`; sem `RELEASE_TAG`.
- Passos: rodar o teste com cada valor.
- Esperado: tag diferente falha citando os dois valores, então nenhum binário é compilado nem
  publicado; tag igual passa; sem a variável o teste só exige que `version` seja
  `MAJOR.MINOR.PATCH`. A release então leva quatro binários estáticos (`linux`/`darwin` ×
  `amd64`/`arm64`, compilados com `-trimpath -ldflags "-s -w -X main.version=X.Y.Z"`),
  `sha256sums.txt` e a seção do CHANGELOG daquela versão como notas.

## J12 — A suíte se mantém honesta · média

*Quem muda o runner confia que o catálogo, as fixtures e os números são verdadeiros.*
Pior impacto: catálogo prometendo testes que sumiram, dado real numa fixture, ou cobertura
que cai sem ninguém ver.

**J12.1 Catálogo e testes concordam** — U `TestCatalogReferencesExistingTests`,
`TestEveryTestBelongsToACase`, `TestCatalogLocalesAgree`, `TestEveryCaseIsComplete`,
`TestParseReadsCasesTestsAndFields`
- Recursos: nenhum (lê o repositório).
- Pré-condições: os dois catálogos e todo `*_test.go`.
- Dados: os ids de caso, os nomes de teste citados, as linhas de campo.
- Passos: `go test ./internal/catalog`.
- Esperado: reprova quando um teste citado não existe, um teste não pertence a caso nenhum,
  as línguas diferem em casos ou testes, ou um caso não tem recursos, pré-condições ou
  esperado.

**J12.2 As fixtures são sintéticas** — U `TestFixturesAreSynthetic`
- Recursos: nenhum (lê o repositório).
- Pré-condições: testes, `testdata/` e `deploy/*.example`.
- Dados: endereços de e-mail, textos com formato de credencial, hosts de URL nos testes.
- Passos: `go test ./internal/catalog`.
- Esperado: reprova endereço fora de `t@t`/`example`/`trilha.local`, qualquer coisa com
  formato de token real (`sk-`, `ghp_`, `github_pat_`, `xox?-`, `AKIA`), e teste chamando host
  que não é de exemplo, loopback ou `.local`.

**J12.3 Os números são reportados, e a cobertura não cai** — U `TestReportSummarizesARun`,
`TestReportFailsBelowTheFloor`, `TestCoverageByPackage`
- Recursos: nenhum.
- Pré-condições: um fluxo `go test -json` e um perfil de cobertura.
- Dados: aprovações, uma falha, um pulo com o motivo, testes lentos; piso acima e abaixo.
- Passos: rodar `internal/testreport` (`make report`).
- Esperado: markdown com passaram/falharam/pulados, os motivos dos pulos, os testes mais
  lentos e a cobertura por pacote; saída ≠ 0 quando um teste falhou ou o total ficou abaixo
  de `testdata/coverage-floor.txt`; sugestão de subir o piso quando está um ponto ou mais acima.
