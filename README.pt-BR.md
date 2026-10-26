# trilha-runner

> [🇺🇸 English](README.md) · 🇧🇷 Português
>
> **Capítulo hands-on no site do Trilha:** <https://emersonjoe.github.io/trilha/pt/aprender/agentico-runner>

**Executa tasks do [protocolo Trilha](https://github.com/emersonjoe/trilha-spec): uma task, um
agente, um worktree, verificado, com evidência.**

```
Task → Agent → Worktree → Execução → Verificação → Evidência
```

`trilha-spec` diz *o que* fazer; `trilha-runner` é *como* roda numa máquina sua. Lê `.trilha/`,
escolhe a task `ready` com toda dependência `done`, faz checkout de um branch para ela
(`trilha/task-nnn` em `.trilha/runs/TASK-NNN/wt`), entrega ao agente nomeado no manifesto o
pacote de contexto, commita o que o agente mudou, roda os checks da task no worktree, grava
cada resultado como evidência e move a task para `review` — ou `failed`. Sua cópia de trabalho
nunca é tocada.

## Instalar e rodar

Com Go:

```bash
go install github.com/emersonjoe/trilha-runner/cmd/trilha-runner@latest
```

Sem Go, toda release traz um binário estático por plataforma (`linux`/`darwin` ×
`amd64`/`arm64`) e um `sha256sums.txt`:

```bash
v=0.6.0; os=$(uname -s | tr A-Z a-z); arch=$(uname -m | sed "s/x86_64/amd64/;s/aarch64/arm64/")
curl -sSLO "https://github.com/emersonjoe/trilha-runner/releases/download/v$v/trilha-runner_${v}_${os}_${arch}"
curl -sSLO "https://github.com/emersonjoe/trilha-runner/releases/download/v$v/sha256sums.txt"
sha256sum --check --ignore-missing sha256sums.txt      # shasum -a 256 --check no macOS
install -m 0755 "trilha-runner_${v}_${os}_${arch}" /usr/local/bin/trilha-runner
trilha-runner version
```

Depois, num projeto:

```bash
# Num repositório git com .trilha/ (trilha-spec init):
trilha-runner next                          # primeira task executável, agente do manifesto
trilha-runner run TASK-003 --driver exec --cmd "claude -p -"
trilha-runner run TASK-003 --driver claude-code
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner next --repo trilha=../trilha # resolve depends_on: trilha:TASK-NNN
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # para o que roda dentro, remove o checkout, mantém o branch
trilha-runner doctor --worktrees            # o que vive em .trilha/runs/; não apaga nada sem uma flag --prune
```

## Drivers

O manifesto do agente (`.trilha/agents/<nome>.md`) nomeia um driver; `--driver` sobrepõe.

| Driver | O que faz |
|---|---|
| `exec` | inicia um agente de linha de comando (`claude -p -`, `codex exec -`, `aider --message-file -`…) com o pacote de contexto no stdin, dentro do worktree; `TRILHA_TASK` e `TRILHA_WORKTREE` vão no ambiente |
| `claude-code` | preset fixo `claude -p -`; um item remoto pode injetar apenas `CLAUDE_CODE_OAUTH_TOKEN` ou `ANTHROPIC_API_KEY`, e o valor é censurado da saída capturada. O runner acrescenta `--session-id <uuid>` (ou `--resume <uuid>` para continuar uma sessão que fez uma pergunta) e `--settings .trilha/runs/TASK-NNN/claude-settings.json` — hooks que reportam o estado do agente, escritos ao lado do worktree, nunca dentro dele |
| `ai` | roda o loop de agente do [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) em qualquer modelo com protocolo OpenAI, com `read_file`, `list_files` e — só quando o manifesto lista `write` / `run` — `write_file` e `run`, tudo restrito ao worktree; mais `ask`, `note`, `overview`, `artifact`, e `finding` / `verdict` / `reply` em runs de revisão, caça e correção; uma run só-leitura não recebe `write_file` nem `ask`; com `trilha-spec` no PATH o modelo recebe também as ferramentas MCP só-leitura do protocolo |
| `echo` | sem modelo; escreve a primeira linha do prompt num arquivo. É o que os testes usam |

Seja qual for o driver, a cerca é a mesma: o worktree, as ferramentas do manifesto e os checks.

## Evidência

Cada execução deixa em `.trilha/evidence/TASK-NNN/`:

- um registro `check` por comando em `checks:` da task (e `project.verify`) — código de saída,
  saída, sha256, rodado no worktree;
- um registro `eval` por linha JSON de métrica emitida por um check, por exemplo
  `{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}`;
  comparador reprovado falha a verificação mesmo com código de saída zero. Um dataset pode
  indicar `{ "id": "triage-v3", "manifest": "eval/golden/manifest.json" }`; somente o
  manifesto é hasheado;
- um registro `run` — driver, branch, commit, diff stat, modelo/turnos/tokens quando o driver
  sabe, o fim da saída do agente e um ponteiro para `.trilha/runs/TASK-NNN/agent.log`.

Uma partida que falha (sem repositório git, driver desconhecido, sem comando, agente saiu com
erro) é um registro `run` com o estágio que falhou, e a task vai para `failed`. Uma task que não
está `ready`, ou ainda espera uma dependência, não roda e fica como estava.

Uma ferramenta que os checks precisam mas o worker não tem é falha de **ambiente**, não três
tentativas de um modelo: antes da primeira tentativa o runner resolve o programa de cada check —
`exec.LookPath` no host, `command -v` pelo sandbox quando há um — e um que falte encerra a execução
na hora com `failure_class: environment`, `error_code: environment_missing_tool`, um registro `run`
citando a ferramenta e o check, e nenhuma chamada ao driver. Um check que sai com 127 em tempo de
execução, ou um comando de agente que não está instalado, recebe a mesma classe e nunca é repetido.

## Quando o agente precisa de alguém (protocolo 0.4)

Durante uma run o runner escuta num socket Unix, `.trilha/runs/TASK-NNN/agent.sock`, e dá ao
agente `TRILHA_RUN_SOCKET`, `TRILHA_RUN_ID` e um `PATH` onde `trilha-runner` é encontrado. O
agente pode então rodar:

| Comando | O que acontece |
|---|---|
| `trilha-runner ask "<pergunta>"` | quando o agente encerra o turno, a run termina **`needs_input`** com a pergunta: o trabalho até ali é commitado no branch, nenhum check roda, nenhuma tentativa falhou, nada é repetido, e a task fica `blocked` localmente até chegar uma resposta |
| `trilha-runner note "<texto>"` | uma nota (fonte `ai`) no resultado, para a próxima run da task |
| `trilha-runner overview "<texto>"` | uma ou duas frases sobre o que está sendo feito e onde está |
| `trilha-runner attention "<motivo>"` / `--clear` | levanta ou limpa o selo que diz que alguém é necessário, na hora |
| `trilha-runner hook claude` | o que os hooks do Claude Code chamam, com o JSON do hook na entrada padrão |

Todos saem com 0 em menos de um segundo, mesmo sem ninguém ouvindo: o runner nunca derruba nem
segura um agente. O pacote de contexto sempre diz ao agente para fazer uma pergunta objetiva e
parar quando faltar informação. Perguntas, notas, overviews e motivos têm limite de tamanho e
passam pela varredura de segredos antes de sair: uma pergunta com segredo chega ao cloud como um
aviso fixo, e as palavras ficam em `.trilha/runs/TASK-NNN/question.txt` (modo 0600).

O estado do agente — `working`, `idle`, `waiting_permission`, `asked`, `stopped_error` — vem dos
hooks do claude-code (`PreToolUse`, `PostToolUse`, `Stop`, `StopFailure`) ou do próprio loop do
`ai`, e é reportado como `agent_state` só quando muda, no máximo uma vez a cada 2 s.

A resposta volta como uma run cuja reivindicação traz `continuation`. Quando a pergunta veio de
uma sessão claude-code, essa mesma sessão é retomada (`--resume`) com a resposta, citada, como
prompt; senão — outro driver, ou uma sessão que esta máquina não tem mais — uma sessão nova lê a
pergunta e a resposta, citadas, no pacote de contexto. O `context` da reivindicação (overview e
notas recentes) também entra citado no pacote, rotulado como informação, nunca instrução. O
resultado traz `question`, `session`, `overview`, `notes` e `diff_stats` (arquivos, inserções,
remoções de `git diff --numstat` contra o HEAD do checkout — contagens, nunca conteúdo).

## Revisão, caça, variantes e comentários de revisão (protocolo 0.4)

O `kind` da reivindicação diz para que serve a run. `implement` (ou nada) é tudo o que está acima.

| Reivindicação | O que o runner faz |
|---|---|
| `kind: review`, `review_mode: report` (o padrão) | uma revisão **só-leitura** do branch da task (`trilha/task-nnn`, ou `-v<N>` quando a reivindicação traz `variant_index`) num checkout destacado próprio, `.trilha/runs/TASK-NNN-review-<run>/wt`; o prompt aponta para o merge base com o branch padrão e pede problemas de correção, segurança e lacunas de teste; o resultado traz `review {verdict, findings}` e status `done` |
| `kind: review`, `review_mode: fix` | a mesma revisão no próprio branch da task: o agente pode corrigir, o runner faz commit e roda os checks da task, a task volta a `review`; o resultado traz o veredito e os achados |
| `kind: hunt` | uma caça a bugs **só-leitura** apontada para a área (portas de entrada, caminhos de erro, camada de dados…) e o estilo (pessimista, seguidor de rastro…) de `hunt_seed`, com escopo `git diff --name-only $(git merge-base <base> HEAD) HEAD`; o resultado traz `findings[]` carimbados com a área e o estilo |
| `variant_index: N` (N ≥ 1) | uma run de implementação no próprio branch, `trilha/task-nnn-v<N>`, em `.trilha/runs/TASK-NNN-vN/`; o runner grava `created-branch` ali e nunca adota um branch que não consegue provar que criou (um `-vN` existente que ele não fez leva a run para `-vN-2`, `-vN-3`…); o pacote de contexto não tem evidência anterior da task nem nota ou overview escritos por agente — nada de uma irmã |
| `review_comments[]` | uma run de correção no branch da task: o prompt ganha um bloco `<reviews>` com as linhas para as quais cada comentário aponta, lidas da worktree (±3 linhas, no máximo 200), todo texto com escape de XML, os comentários citados como dados de revisores, nunca como instruções; o resultado traz `review_replies[]` |

Uma revisão ou caça que precisa de um branch que este worker não tem o busca no origin; ele nunca
é cortado do HEAD. **Só-leitura é verificado, não pedido:** quando uma caça ou uma revisão
`report` termina, a worktree tem de estar limpa (`git status --porcelain --untracked-files=all`),
o `HEAD` tem de ser o commit do início e o branch revisado não pode ter se movido; qualquer outra
coisa reprova a run com `failure_class: policy` (`error_code: read_only_violated`) e seus achados
e artefatos são descartados. Com o sandbox Docker a worktree também é montada só-leitura. Uma run
só-leitura nunca move a task, não para por perguntas e nunca é publicada.

O agente relata por mais quatro comandos (e as mesmas ferramentas no driver `ai`), que saem com 0
como os outros:

| Comando | O que acontece |
|---|---|
| `trilha-runner finding --severity critical\|high\|medium --path P --lines A-B --title T [--why W] [--repro R]` | um achado: validado (`execution.Finding.Validate`), com segredos censurados, no máximo 100 por run; um caminho e um intervalo de linhas, nunca código |
| `trilha-runner verdict pass\|changes` | o veredito da revisão; sem ele, `changes` quando há achados, `pass` caso contrário |
| `trilha-runner reply <id-do-comentário> "<texto>"` | a resposta a um comentário de revisão da reivindicação (ids desconhecidos são descartados), censurada, no máximo 4000 bytes |
| `trilha-runner artifact <arquivo> [--name N]` | publica um relatório: `.html`, `.md`, `.png` ou `.jpg`, até 2 MiB e cinco por run, da worktree ou de `$TRILHA_ARTIFACT_DIR` (fora da worktree, para runs só-leitura); copiado para `.trilha/runs/<run>/artifacts/`; um relatório de texto com segredo é **recusado**, não censurado, e uma imagem tem de ser imagem de verdade |

O worker envia cada artefato **antes** de postar o resultado — `PUT /api/runs/{id}/artifacts/{name}`
com os bytes crus, seu `Content-Type` e `X-Artifact-SHA256` — e lista em `artifacts[]` do
resultado só os que o cloud aceitou (201). Um projeto que não permite artefatos responde 403
`ARTIFACTS_DISABLED`: o runner descarta todos e avisa uma vez. Um 4xx nunca é repetido.

## Doctor de worktrees

```bash
trilha-runner doctor --worktrees [--json]
trilha-runner doctor --worktrees --prune-orphans
trilha-runner doctor --worktrees --prune-older-than 30d [--force-unmerged] [--default-branch main]
```

O doctor classifica cada diretório `.trilha/runs/*` — `live` (um processo do runner, ou qualquer
processo cujo diretório atual está dentro da worktree), `finished-kept`, `orphaned` (o git não
lista a worktree, ou o arquivo da task sumiu) ou `metadata-only` (sem worktree) — com idade
(`<14d`, `<30d`, `>=30d`) e tamanho. **Sem uma flag `--prune` ele nunca apaga nada.** Com uma,
nunca toca numa run viva, pula uma worktree cujo branch não está mesclado em `origin/<padrão>` ou
que tem mudanças não commitadas, a não ser com `--force-unmerged`, e apaga um branch só quando o
arquivo `created-branch` da run prova que o runner o criou.

Remover uma worktree — `worktree clean` ou uma poda — é um encerramento ordenado: os processos
cujo diretório atual está dentro dela (`/proc/*/cwd` no Linux, `lsof` no macOS; pulado com uma
nota quando nenhum existe) recebem SIGTERM, 1,5 s, depois SIGKILL; então a worktree sai. Cada
etapa é registrada e nenhuma delas impede a remoção.

| Código de saída | Significado |
|---|---|
| 0 | o relatório, e toda poda pedida, feitos |
| 1 | o comando não pôde rodar (flag inválida, não é um projeto Trilha) |
| 12 | algo que as flags pediram para remover foi pulado (trabalho não mesclado ou não commitado) ou falhou |

## Worker: a ponte para o trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.exemplo --token "$TOKEN" --project meu-app \
  --label docker --label region:br --capacity 2 \
  --workspace-root /var/lib/trilha-runner/workspaces \
  --repo git@github.com:acme/meu-app.git --default-branch main --push
```

Um worker é um processo num checkout. Pede ao control plane a próxima execução
(`POST /api/runs/next`), executa localmente exatamente como acima e reporta resultado e
evidência (`POST /api/runs/{id}/result`). O código não sai da máquina; o cloud vê status,
branch, commit e evidência. `queue.Remote` é o contrato inteiro, então outro control plane
pode implementá-lo. Heartbeat e claim carregam labels, capacidade, runs ativas, a versão do
runner e `driver_versions`: o que `claude --version` e `codex --version` respondem neste worker,
resolvido uma vez na subida com timeout de 3 s (`unknown` quando o binário falta ou não
responde; `echo` e `ai` são o próprio runner). Cada run pode carregar um objeto `ai` transitório com provedor, URL base,
modelo, credencial e hosts permitidos; a credencial nunca é persistida.

Enquanto houver run ativa o worker manda um heartbeat a cada 30 s (`running n/N`), para que uma
run longa não o deixe parecendo offline e o cloud possa manter o lease da run. O resultado é
escrito em `.trilha/runs/<run-id>/result.json` antes de ser enviado e removido quando o cloud o
tem; um cloud fora do ar é tentado de novo com backoff (até 30 s entre tentativas) em vez de
parar o worker, e resultados deixados por um worker que morreu são enviados na próxima subida,
antes de qualquer reivindicação. O cloud guarda um resultado por run e tentativa, então um
resultado enviado duas vezes continua sendo um. Uma task deixada em `running` por um runner que
morreu (nenhum processo vivo por trás de `.trilha/runs/TASK-NNN/runner.pid`) volta a `ready` com
uma nota na próxima vez que for rodada.

Dependências entre repositórios usam `depends_on: alias:TASK-NNN`. A execução local resolve os
aliases com flags repetíveis `next --repo alias=caminho`. Workers remotos consultam
`GET /api/projects/{alias}/tasks/{id}` e reportam `blocked` com
`waiting:alias:TASK-NNN` quando a dependência não existe ou ainda não está `done`.

Quando a execução nasce de uma spec criada na interface, o worker baixa um bundle versionado,
sincroniza a spec e as tasks em `.trilha/`, cria o branch de implementação e o publica. Para
deploy e rollback, `--delivery-config /etc/trilha-runner/delivery.json` habilita apenas perfis
locais com argumentos fixos; o Cloud nunca envia comandos. Segredos são entregues como variáveis
de ambiente apenas durante a operação e não entram nos logs. Exemplos de `systemd`, ambiente e
perfis para eoslab estão em `deploy/eoslab/`.

**Código de fork.** Uma reivindicação com `foreign_code: true` (o head do branch vem de um fork)
roda só no sandbox Docker — sem ele, ou com o Docker indisponível, a run falha com
`failure_class: policy` (`sandbox_required`) antes de o agente começar. O agente e os checks não
recebem segredo nenhum: nenhum token herdado, nenhum segredo de deploy, e a credencial de IA só
como a variável de que um driver de linha de comando precisa para chamar o modelo (o driver `ai`
a guarda no processo, e os checks dele rodam dentro do sandbox). O `project.verify` é lido do
branch padrão, nunca do branch do fork, e o branch nunca é publicado.

Perfis podem declarar `steps` ordenados, um gate `migrate` antes do passo `switch` e vários
endpoints em `health`. Migração com falha aborta antes da troca; health com falha dispara o
rollback permitido. Migração não reversível é reportada como rollback apenas da imagem.

## Sandbox Docker

Um manifesto de agente pode declarar sandbox Docker usando o mapa de um nível do protocolo (o
valor de `services` é JSON):

```yaml
sandbox:
  image: ghcr.io/acme/app-ci:latest
  services: '[{"name":"db","image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"test"},"ready":["pg_isready","-h","localhost","-U","postgres"]}]'
```

`ready` roda dentro do container do serviço até sair zero. Sonde o serviço pela via que o agente
vai usar — TCP, `pg_isready -h localhost` — porque uma sonda pelo socket Unix responde enquanto o
Postgres ainda roda o servidor temporário do `initdb`, e o agente então encontra
`db:5432 - no response`.

O runner cria uma rede privada, sobe os serviços declarados, monta só o worktree em
`/workspace` e executa drivers externos e checks por `docker exec`. CPU, memória, PIDs, raiz
somente leitura, `cap-drop ALL` e `no-new-privileges` são fixos no runner. O socket Docker nunca é
montado no container do agente, e o teardown remove containers e rede em todo caminho. Um
container que não fica de pé é reportado com a saída dele mesmo, em vez de aparecer depois como
uma falha inexplicada de `docker exec`.

Duas propriedades merecem ser ditas com clareza, porque são o que torna o sandbox utilizável e
seguro ao mesmo tempo:

- **O agente não é root.** Ele roda como o usuário dono do worktree. Com todas as capabilities
  derrubadas não existe `CAP_DAC_OVERRIDE` de reserva, então é exatamente isso que mantém
  gravável o único caminho gravável — e faz do agente algo menor que root enquanto está lá.
- **Nenhum segredo vai para linha de comando.** O ambiente da execução, credencial do projeto
  incluída, entra no container uma vez por um arquivo `0600` no `docker run`. O `docker exec` não
  carrega nenhum `KEY=VALUE`, porque argumento de `docker exec` é argumento de um processo no
  *host*, e o `ps` mostra isso para qualquer usuário da máquina.

## Pacotes

| Pacote | O que é |
|---|---|
| `runner` | o pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `ai`, `claude-code`, `echo`; `driver.Register` para o seu |
| `worktree` | worktrees git por task e por run (própria, variante, existente, destacada), commit, diff stat, doctor, encerramento |
| `queue` | `Local` (o grafo) e `Remote` (cliente do trilha-cloud) |
| `sandbox` | `None` e o sandbox Docker local com serviços privados |
| `cmd/trilha-runner` | a CLI |

Depende do `trilha-spec` (o protocolo) e dos pacotes `ai` e `ai/mcp` do framework `trilha` —
nada fora da biblioteca padrão do Go além desses dois módulos.

## Testes

`make ci` roda o que o CI roda: gofmt, `go vet`, a suíte inteira e o detector de corrida —
sem rede nem chave, com o driver `echo` e repositórios git reais em diretórios temporários.
Como a suíte se organiza, e as jornadas de usuário que cada teste prova, estão em
[docs/testing.pt-BR.md](docs/testing.pt-BR.md) e [docs/journeys.pt-BR.md](docs/journeys.pt-BR.md).

Casos ao vivo rodam um modelo real e são opcionais, nunca no CI: `TRILHA_E2E_OLLAMA=1`
conduz o driver `ai` contra o Ollama em `localhost:11434` (`TRILHA_E2E_OLLAMA_MODEL`, padrão
`qwen2.5:7b`), e `TRILHA_E2E_CLAUDE=1` conduz o `claude-code` com o login local do Claude Code
(`TRILHA_E2E_CLAUDE_MODEL`, padrão `haiku`; custa dinheiro). Os dois fazem uma pergunta,
respondem numa continuação e conferem o resultado; o conjunto do Ollama também roda uma caça
só-leitura num erro de uma posição plantado e espera um achado válido. Cada um tenta até três
vezes (`TRILHA_E2E_TRIES` reduz): `TRILHA_E2E_OLLAMA=1 go test ./cmd/trilha-runner -run TestLive -v`.

## Licença

MIT.
