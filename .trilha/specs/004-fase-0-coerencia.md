---
id: 004-fase-0-coerencia
title: "Fase 0 do roadmap da stack: coerência do runner"
status: approved
issue: "13, 14 (emersonjoe/trilha-runner); RM-002, RM-003, RM-004, RM-007, RM-008, RM-014 de trilha-cloud/docs/ROADMAP-STACK.md"
assets:
  - cmd/trilha-runner
  - runner/
  - sandbox/
  - queue/
  - .github/workflows
trust_boundaries:
  - worker → trilha-cloud
  - runner → sandbox Docker
  - GitHub Actions → GitHub Releases
controls:
  - ASVS V14.2 (dependências e integridade de build)
  - ASVS V7.1 (log e evidência sem segredo)
evidence:
  - make ci
  - make conformance
  - go test ./sandbox -run TestDockerPostgresAndTeardown -count=20
---

# Fase 0 do roadmap da stack: coerência do runner

## Por quê

O roadmap da stack (`trilha-cloud/docs/ROADMAP-STACK.md`, §2 "Fase 0") lista o que
quebra confiança no runner hoje, antes de qualquer feature nova:

- **RM-007** — `main` ficou vermelho em 2026-10-15: `TestDockerPostgresAndTeardown`
  falhou com `db:5432 - no response` depois de a prontidão ter passado. A prontidão
  `pg_isready -U postgres` dentro do container do serviço usa o socket Unix e responde
  "pronto" ainda durante o servidor temporário do `initdb`, que escuta só no socket; o
  agente, em outro container, chega por TCP no intervalo em que esse servidor desce e o
  definitivo ainda não aceita conexão.
- **RM-002** — o CHANGELOG anuncia 0.4.0 e `git tag` para em `v0.3.2`; quem não tem Go
  não instala; não há workflow de release e nada confere que a tag bate com a versão
  compilada.
- **RM-003** — o fixture `testdata/contracts/execution-v1.json` diverge do canônico do
  `trilha-spec` (faltam `provider`, `tokens_in`, `tokens_out`, `cost`, `currency` na
  tentativa); `make conformance` não roda em CI, então runner e cloud divergem em silêncio.
- **RM-014** (#13, #14) — uma ferramenta ausente no worker (`python` não instalado, exit
  127) é classificada como `execution` e queima as três tentativas de IA; o caso real
  `acervo run-000007` gastou três chamadas ao modelo por isso.
- **RM-008** — `driver_versions` no claim e no heartbeat repete a versão do runner para
  todos os drivers, então a frota não sabe qual `claude` ou `codex` cada worker tem.
- **RM-004** — a spec 002 está `approved` embora entregue em 0.2.0 e a TASK-007
  "Container sandbox" está `idea` embora o Docker tenha saído em 0.3.0: duas verdades.

## O que muda

- **Sandbox (RM-007):** a prontidão de Postgres nos exemplos, no teste e no catálogo passa
  a ser `pg_isready -h localhost -U postgres`, isto é, pela mesma via (TCP) que o agente
  usa. O README explica por quê. Evidência: `-count=20` verde.
- **Release (RM-002):** `version` vira `var` sobrescrevível por `-ldflags -X`;
  `.github/workflows/release.yml` dispara em tags `v*`, confere que a tag é igual à versão
  (`TestVersionMatchesTag` lê `RELEASE_TAG`), compila linux/amd64, linux/arm64,
  darwin/amd64, darwin/arm64 com `-trimpath -ldflags "-s -w -X main.version=…"`, gera
  `sha256sums.txt` e cria a release com os binários e a seção do CHANGELOG. Ações
  fixadas por SHA como no `ci.yml`. O README ganha o caminho por binário.
- **Contrato (RM-003):** fixture e schema copiados byte a byte do `trilha-spec`;
  `queue.Attempt` e `runner.Attempt` carregam `provider`, `tokens_in`, `tokens_out`,
  `cost`, `currency`; o teste de contrato prova a ida e volta; job `conformance` no
  `ci.yml` faz checkout do `emersonjoe/trilha-spec` (`ref: main`) como irmão e roda
  `make conformance`.
- **Falha de ambiente (RM-014):** antes da tentativa 1 o runner resolve o primeiro
  token de cada comando de `checks:` e de `project.verify` no mesmo ambiente em que o
  check vai rodar (`exec.LookPath` no host; `command -v` pelo prefixo do sandbox). Se
  faltar, a run termina `failed` com `failure_class=environment`,
  `error_code=environment_missing_tool`, zero chamadas ao driver e um registro de
  evidência dizendo qual ferramenta falta. Exit 127 e "command not found" /
  "executable file not found" durante a execução recebem a mesma classificação. A
  política de retry nunca repete uma falha `environment`. `runner.Result` e
  `queue.Result` ganham `error_code`.
- **Versões de driver (RM-008):** o worker resolve `claude --version` e
  `codex --version` uma vez na subida (3 s de timeout) e cai para `unknown`; `echo` e
  `ai` mantêm a versão do runner.
- **Estado honesto (RM-004):** spec 002 → `done`; TASK-007 → `done` com nota apontando
  o CHANGELOG 0.3.0; TASK-006 continua `idea` e referencia o RM-030 do roadmap.

## Fora de escopo

- Homebrew, imagens Docker, assinatura cosign (RM-040).
- `failure_class` como enum no protocolo (RM-050); aqui o vocabulário é o recomendado
  pela spec 014 do protocolo, com `environment` como classe e
  `environment_missing_tool` como código, como o cloud (spec 024) já exibe.
- Cortar a tag `v0.4.0`, empurrar branch, abrir PR ou fechar issues: decisão humana.

## Aceitação

- **SC-001** `go test ./sandbox -run TestDockerPostgresAndTeardown -count=20` verde.
- **SC-002** `RELEASE_TAG=v9.9.9 go test ./cmd/trilha-runner -run TestVersionMatchesTag`
  falha; com a tag igual à versão, passa. O workflow só publica depois desse passo.
- **SC-003** `bash ../trilha-spec/scripts/check-execution-contract.sh` sai 0 para o runner;
  `TestExecutionV1FixtureMatchesRemoteResult` prova que os cinco campos de custo da
  tentativa sobrevivem à ida e volta.
- **SC-004** Uma task cujo check chama uma ferramenta ausente do PATH termina `failed`
  em uma tentativa, `failure_class=environment`, `error_code=environment_missing_tool`,
  sem nenhuma chamada ao driver; o mesmo vale para o sandbox Docker (`command -v`).
- **SC-005** `classifyRetry` devolve `environment`, sem retry, para exit 127 e para
  "command not found" / "executable file not found".
- **SC-006** Com um `claude` falso no PATH que responde `1.2.3`, o claim carrega
  `driver_versions.claude-code = 1.2.3`; sem `codex`, `unknown`; `echo` e `ai` com a
  versão do runner.
- **SC-007** `trilha-spec spec list` não mostra `approved` com todas as tasks `done`;
  `trilha-spec task list --status idea` mostra só a TASK-006, referenciando RM-030.

## Impacto de segurança

- **Ativos:** o binário publicado (integridade de build), o contrato de fio
  runner ↔ cloud, a evidência de execução, a credencial do projeto dentro do sandbox.
- **Fronteiras de confiança:** GitHub Actions → GitHub Releases (o workflow só tem
  `contents: write`, ações fixadas por SHA, `-trimpath` e checksums publicados);
  worker → cloud (`driver_versions` e `error_code` são texto curto sem segredo); runner →
  sandbox (o preflight entra no container pelo mesmo prefixo dos checks, sem `env` e sem
  colocar nenhum valor da run em argv do host).
- **Controles ASVS 5.0 L2 tocados:** V14.2 (build reproduzível, dependências fixadas,
  checksums); V7.1 (a evidência de ferramenta ausente registra o nome do programa, nunca
  o ambiente); V10.3 (o preflight não executa o comando do check, só o resolve).
- **Risco residual:** a saída de `claude --version` entra no heartbeat como texto; o
  runner a limita a uma linha curta e não a interpreta.

## Evidência

- `make ci` — gofmt, vet, suíte inteira e detector de corrida.
- `make conformance` — fixtures idênticos aos do `trilha-spec` (`../trilha-spec`).
- `go test ./sandbox -run TestDockerPostgresAndTeardown -count=20`.
- `RELEASE_TAG=v0.0.0 go test ./cmd/trilha-runner -run TestVersionMatchesTag` (deve falhar).
- `make report` — números e cobertura contra `testdata/coverage-floor.txt`.
