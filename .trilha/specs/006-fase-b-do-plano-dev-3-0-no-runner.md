---
id: 006-fase-b-do-plano-dev-3-0-no-runner
title: Fase B do plano dev-3.0 no runner
status: approved
issue: DV-05b, DV-06, DV-07, DV-08, DV-13a, DV-13b, DV-13c (trilha-cloud/docs/PLANO-DEV3.md)
assets:
  - cmd/trilha-runner
  - runner/
  - driver/
  - queue/
  - worktree/
  - sandbox/
  - internal/agentlink
trust_boundaries:
  - trilha-cloud → runner (kind, review_mode, hunt_seed, variant_index, review_comments, foreign_code)
  - pessoas no GitHub → agente (comentários de revisão)
  - "agente → runner (socket Unix: finding, verdict, reply, artifact)"
  - runner → trilha-cloud (achados, respostas, artefatos)
  - branch de fork → worker (sandbox Docker)
controls:
  - NIST SSDF PW.1, PW.4, PW.5, PW.7, PW.8, PO.5, RV.1
  - ASVS V1/V5 (validação e codificação de saída)
  - "ASVS V8 (autorização no recurso: só-leitura verificado)"
  - ASVS V12 (arquivos e envio)
  - ASVS V13/V14 (segredos e configuração)
  - ASVS V16 (registro de segurança)
evidence:
  - make ci
  - make report
  - make conformance
---

# Fase B do plano dev-3.0 no runner

## Por quê

A fase A deu ao runner o laço curto com a pessoa (estado, pergunta, continuação, memória). A
fase B do plano `trilha-cloud/docs/PLANO-DEV3.md` traz a segunda opinião sobre um branch e a
higiene das worktrees: revisão por IA disparada pela etapa (DV-06), caçadores de bugs
só-leitura com achados estruturados (DV-07), variantes da mesma task em branches separados
(DV-05b), comentários de revisão do PR voltando ao agente (DV-08), artefatos de relatório e as
operações de worktree — doctor, encerramento ordenado e código de fork (DV-13a, 13b, 13c). O
protocolo 0.4 (`trilha-spec`, spec 016, `9cb760d`) já define `Kind`, `ReviewMode`, `HuntSeed`,
`Finding`, `ReviewVerdict`, `ReviewComment`, `ReviewReply`, `Artifact` e os limites; esta spec é
o lado do runner, com a interface de fio combinada com o cloud.

Decisões do dono: a revisão por IA pode corrigir ou só relatar, por projeto, e chega ao runner
como `review_mode` (`report` | `fix`); artefatos de relatório podem ir ao cloud quando o projeto
permite.

## O que muda

- **B1 — Variantes (DV-05b):** `variant_index ≥ 1` trabalha em `trilha/task-nnn-v<N>`, em
  `.trilha/runs/TASK-NNN-vN/`; o runner grava `created-branch` e nunca adota nem apaga um branch
  que não provou ter criado (um `-vN` existente leva a `-vN-2`, `-vN-3`…); o pacote de contexto
  da variante não tem evidência anterior da task nem nota ou overview escritos por agente; o
  resultado informa o branch.
- **B2 — Revisão (DV-06):** `kind: review` abre o branch revisado (o da task, ou `-v<N>` quando a
  reivindicação traz `variant_index`; busca no origin quando o worker não o tem, nunca corta do
  HEAD). `report` (padrão) é só-leitura num checkout destacado próprio e, no fim, o runner
  verifica worktree limpa, `HEAD` igual ao do início e branch parado; senão `failure_class:
  policy` e os achados são descartados. `fix` trabalha no branch, commita e roda os checks. O
  prompt (adaptado do `DEFAULT_REVIEW_PROMPT` do dev-3.0) compara com o merge base do branch
  padrão e pede correção, segurança e lacunas de teste. Achados por `trilha-runner finding` e
  `trilha-runner verdict` (e ferramentas no driver `ai`); sem veredito, `changes` se houver
  achado, `pass` senão.
- **B3 — Caça (DV-07):** `kind: hunt` sempre só-leitura, com área e estilo do `hunt_seed` e
  escopo `git diff --name-only $(git merge-base <base> HEAD) HEAD`; mesma verificação, mesma
  falha `policy`; no Docker a worktree é montada só-leitura; `findings[]` (≤ 100) com área e
  estilo.
- **B4 — Comentários de revisão (DV-08):** o runner monta localmente um bloco `<reviews>` no
  formato do dev-3.0 com as linhas de cada comentário lidas da worktree (±3, no máximo 200),
  escape de XML em todo nó de texto e atributo, comentários citados como dados; respostas por
  `trilha-runner reply <id> "<texto>"`; `review_replies[]` só para ids da reivindicação, até
  4000 bytes, censuradas.
- **B5 — Artefatos:** `trilha-runner artifact <arquivo> [--name N]` (.html .md .png .jpg .jpeg,
  ≤ 2 MiB, ≤ 5 por run, da worktree ou de `$TRILHA_ARTIFACT_DIR`), copiado para
  `.trilha/runs/<run>/artifacts/`; texto com segredo é recusado, não censurado; o worker envia
  antes do resultado.
- **B6 — Doctor (DV-13a):** `trilha-runner doctor --worktrees [--json]` classifica (live,
  finished-kept, orphaned, metadata-only), idade (<14 d, <30 d, mais) e tamanho; apaga só com
  `--prune-orphans` / `--prune-older-than`; nunca branch não mesclado em `origin/<padrão>` sem
  `--force-unmerged`; branch só com prova de criação; código de saída 12 quando algo é pulado ou
  falha.
- **B7 — Encerramento ordenado (DV-13b):** antes de remover uma worktree (`worktree clean`, poda
  do doctor), processos com diretório atual dentro dela (Linux `/proc/*/cwd`, macOS `lsof`, sem
  ele pula com nota) recebem SIGTERM, 1,5 s, SIGKILL; cada etapa registrada; nunca impede a
  remoção.
- **B8 — Código de fork (DV-13c):** com `foreign_code`, exige o sandbox Docker (senão `policy`),
  ambiente sem segredos (credencial de IA só para driver de linha de comando que precisa dela;
  nenhuma no ambiente do driver `ai`, cujos checks rodam no sandbox), `project.verify` do branch
  padrão, branch nunca publicado.

### Interface de fio com o cloud (fixa)

1. Reivindicação (nomes de `execution.Run`): `kind`, `review_of`, `review_mode`, `hunt_seed {seed,
   area, style}`, `variant_group`, `variant_index`, `review_comments[] {id, path, line_start,
   line_end, side, body, author}`, e `foreign_code` no nível da fila (`queue.Item`).
2. Resultado: `review {verdict, findings[]}` (review); `findings[]` (hunt); `review_replies[] {id,
   reply}`; `artifacts[] {name, content_type, size, sha256}` só com os aceitos. Status `done` para
   revisão `report` e caça bem-sucedidas; `review` para revisão `fix` (a task volta a revisão).
3. Upload antes do resultado: `PUT /api/runs/{id}/artifacts/{name}`, corpo cru, `Content-Type`,
   `X-Artifact-SHA256`, mesma chave; 201 aceito; 403 `ARTIFACTS_DISABLED` descarta todos e
   registra uma vez; 413/422 descarta aquele; 4xx nunca repetido (rede ou 5xx: até 3 tentativas).
4. Variantes: branch `trilha/task-nnn-v<N>`; `created-branch` prova a criação; apagar branch
   remoto de perdedora é do cloud.

## Fora de escopo

- O lado do cloud (fila de revisão, agregação e deduplicação de achados, armazenamento de
  artefatos, comparação de variantes, resposta no PR) — outro agente, em `trilha-cloud`.
- Clone copy-on-write (DV-13d) e GC automático.
- Agentes de linha de comando dentro do sandbox Docker não alcançam o socket do host: ali achados,
  veredito e respostas só chegam pelo driver `ai` (em processo).
- Buscar o head de um PR de fork (`refs/pull/N/head`): o runner usa o branch da task.
- O capítulo do site em `emersonjoe/trilha` (esta sessão não pode editar aquele repositório).

## Aceitação

- **SC-001** Variantes 1 e 2: branches `-v1` e `-v2-2` (com um `-v2` alheio intocado), prova em
  `created-branch`, pacote da 2 sem nada da 1 (`TestVariantBranchIsProvenAndNeverAdoptsAnother`,
  `TestVariantRunsOnItsOwnBranchAndReadsNothingOfItsSiblings`).
- **SC-002** Revisão `report` dá veredito e fica `done` na ponta; `fix` commita, roda checks e
  volta a `review`; branch vindo do origin ou recusado (`TestReviewReportGivesAVerdict`,
  `TestReviewFixModeCommitsRunsChecksAndReports`, `TestExistingBranchComesFromOriginOrIsRefused`).
- **SC-003** Caça só-leitura com achados válidos e censurados; arquivo gravado, commit ou branch
  movido viram `policy` sem achados; montagem `readonly`; ponta a ponta pelo binário
  (`TestHuntRunStaysReadOnlyAndReportsFindings`, `TestReadOnlyRunThatChangesTheRepositoryFailsWithPolicy`,
  `TestReadOnlyRunMountsTheWorktreeReadOnly`, `TestDetachedCheckoutCreatesNoBranch`,
  `TestHuntRunReportsFindingsAndUploadsArtifacts`).
- **SC-004** Bloco `<reviews>` com linhas da worktree e `<` escapado; caminho fora recusado;
  respostas só para ids conhecidos (`TestReviewCommentsAreQuotedFromTheWorktreeAndAnswered`).
- **SC-005** Artefatos: cópia 0600 com sha256, segredo recusado, PNG falso recusado, limite 5,
  envio só dos aceitos, 403 `ARTIFACTS_DISABLED` descarta todos (`TestArtifactsAreCopiedCheckedAndLimited`,
  `TestUploadArtifactSendsBytesTypeAndDigest`, `TestUploadArtifactsListsOnlyWhatTheCloudAccepted`).
- **SC-006** Comandos e ferramentas do agente validam antes de enviar
  (`TestReportCommandsParseAndRefuse`, `TestAIDriverReportsFindingsVerdictRepliesAndArtifacts`,
  `TestClaimAndResultCarryReviewHuntVariantAndArtifacts`).
- **SC-007** Doctor sem flag não apaga (contagem de arquivos igual); poda só o pedido; saída 12
  (`TestDoctorWithoutFlagsNeverDeletes`, `TestDoctorPrunesOnlyWhatTheFlagsSay`,
  `TestDoctorReportsAndExitsTwelveWhenItSkips`).
- **SC-008** Processo filho deixado na worktree morre antes da remoção (SIGTERM e SIGKILL)
  (`TestCleanStopsProcessesInsideTheWorktreeBeforeRemovingIt`).
- **SC-009** Fork sem sandbox ou com Docker indisponível: `policy`; ambiente sem credencial no
  `ai`; checks do branch padrão (`TestForeignCodeNeedsDockerGetsNoSecretAndVerifiesFromTheDefaultBranch`).
- **SC-010** `make ci`, `make report` (cobertura ≥ piso, piso elevado a 75,8) e `make
  conformance` verdes.
- **SC-011** Opcional: `TRILHA_E2E_OLLAMA=1` com `qwen2.5:7b`, caça num erro de uma posição
  plantado, fica só-leitura e devolve ≥ 1 achado com caminho válido (`TestLiveOllamaHuntFindsThePlantedBug`).

## Impacto de segurança

- **Ativos:** o branch que uma pessoa vai mesclar; branches de outras pessoas; a credencial do
  projeto e segredos do worker; o checkout do mantenedor; o contrato de fio; os relatórios.
- **Fronteiras de confiança:** cloud → runner (`kind`, `review_mode`, `hunt_seed`,
  `variant_index`, `review_comments`, `foreign_code`: o `kind` é validado, o id de run só entra
  em caminho se casar com `run-NNNNNN`, o caminho de um comentário só é lido dentro da worktree,
  links simbólicos incluídos, e o corpo vai escapado e citado como dado); pessoas no GitHub →
  agente (corpo e autor de comentário: injeção de prompt contida por escape e rótulo); agente →
  runner (socket 0600; achado validado e censurado; artefato só da worktree ou de
  `$TRILHA_ARTIFACT_DIR`, tipo pela extensão e assinatura, texto com segredo recusado); runner →
  cloud (achados sem código, só caminho e linhas; artefatos só depois de varridos, e só os
  aceitos listados); branch de fork → worker (sandbox Docker obrigatório, sem segredo, checks do
  branch padrão, nada publicado).
- **Controles NIST SSDF 1.1:** PW.1 (ameaças acima antes do código), PW.4/PW.5 (reuso do
  `trilha-spec` para validar achados e artefatos; sem dependência nova), PW.7/PW.8 (testes por
  nível, com git e processos reais; regressão do vazamento de arquivos de teste no pacote), PO.5
  (ambiente isolado para código de terceiros), RV.1 (o caso ao vivo foi rodado e relatado).
- **Controles OWASP ASVS 5.0 L2 tocados:** V1/V5 (validação e codificação de saída: limites do
  protocolo, escape de XML no prompt), V8 (autorização no nível do recurso: só-leitura
  verificado, branch alheio nunca adotado nem apagado), V12 (arquivos: caminhos confinados,
  tipo verificado por assinatura, tamanho limitado, modo 0600), V13/V14 (segredos fora do
  ambiente de código de fork; segredo nunca publicado em relatório), V16 (cada etapa do
  encerramento e cada recusa registradas).
- **Risco residual:** a verificação de só-leitura não vê arquivos ignorados pelo `.gitignore`
  (um agente poderia gravar ali sem ser recusado; nada disso é commitado nem publicado); a
  varredura de segredos reconhece formatos comuns e a credencial da run, não todo segredo; HTML
  de relatório pode conter script — o cloud precisa servi-lo em origem opaca; um agente de linha
  de comando no Docker não relata achados (limitação documentada); o encerramento só alcança
  processos do mesmo usuário e, no macOS sem `lsof`, é pulado com nota.

## Evidência

- `make ci` — gofmt, vet, suíte inteira e detector de corrida.
- `make report` — 133 passaram, 0 falharam, 3 pulados (os casos ao vivo opcionais); cobertura
  75,9 % contra o piso, elevado de 74,9 para 75,8.
- `make conformance` — as três fixtures idênticas às do `trilha-spec` (branch `protocol-0.4`).
- `TRILHA_E2E_OLLAMA=1 go test ./cmd/trilha-runner -run TestLiveOllamaHuntFindsThePlantedBug -v`
  — caça ao vivo com `qwen2.5:7b`: passou na primeira tentativa, `done`, branch parado, 2 achados
  válidos (um em `calc.go` 3-5, severidade high; outro em `calc_test.go` com linhas além do fim do
  arquivo — caminho válido, intervalo impreciso).
