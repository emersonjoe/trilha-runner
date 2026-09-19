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

```bash
go install github.com/emersonjoe/trilha-runner/cmd/trilha-runner@latest

# Num repositório git com .trilha/ (trilha-spec init):
trilha-runner next                          # primeira task executável, agente do manifesto
trilha-runner run TASK-003 --driver exec --cmd "claude -p -"
trilha-runner run TASK-003 --driver claude-code
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner next --repo trilha=../trilha # resolve depends_on: trilha:TASK-NNN
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # remove o checkout, mantém o branch
```

## Drivers

O manifesto do agente (`.trilha/agents/<nome>.md`) nomeia um driver; `--driver` sobrepõe.

| Driver | O que faz |
|---|---|
| `exec` | inicia um agente de linha de comando (`claude -p -`, `codex exec -`, `aider --message-file -`…) com o pacote de contexto no stdin, dentro do worktree; `TRILHA_TASK` e `TRILHA_WORKTREE` vão no ambiente |
| `claude-code` | preset fixo `claude -p -`; um item remoto pode injetar apenas `CLAUDE_CODE_OAUTH_TOKEN` ou `ANTHROPIC_API_KEY`, e o valor é censurado da saída capturada |
| `ai` | roda o loop de agente do [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) em qualquer modelo com protocolo OpenAI, com `read_file`, `list_files` e — só quando o manifesto lista `write` / `run` — `write_file` e `run`, tudo restrito ao worktree; com `trilha-spec` no PATH o modelo recebe também as ferramentas MCP só-leitura do protocolo |
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

Uma partida que falha (sem comando, sem repositório git, agente saiu com erro) é um registro
`run` com o estágio que falhou, e a task vai para `failed`.

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
pode implementá-lo. Heartbeat e claim carregam labels, capacidade, runs ativas e versões do
runner e dos drivers. Cada run pode carregar um objeto `ai` transitório com provedor, URL base,
modelo, credencial e hosts permitidos; a credencial nunca é persistida.

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

Perfis podem declarar `steps` ordenados, um gate `migrate` antes do passo `switch` e vários
endpoints em `health`. Migração com falha aborta antes da troca; health com falha dispara o
rollback permitido. Migração não reversível é reportada como rollback apenas da imagem.

## Sandbox Docker

Um manifesto de agente pode declarar sandbox Docker usando o mapa de um nível do protocolo (o
valor de `services` é JSON):

```yaml
sandbox:
  image: ghcr.io/acme/app-ci:latest
  services: '[{"name":"db","image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"test"},"ready":["pg_isready","-U","postgres"]}]'
```

O runner cria uma rede privada, inicia os serviços declarados, monta somente o worktree em
`/workspace` e executa drivers externos e checks via `docker exec`. Limites de CPU, memória,
PIDs, raiz somente leitura e `no-new-privileges` são fixos no runner. O socket Docker nunca é
montado no container do agente, e o teardown remove containers e rede em todos os caminhos.

## Pacotes

| Pacote | O que é |
|---|---|
| `runner` | o pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `ai`, `claude-code`, `echo`; `driver.Register` para o seu |
| `worktree` | worktrees git por task, commit, diff stat |
| `queue` | `Local` (o grafo) e `Remote` (cliente do trilha-cloud) |
| `sandbox` | `None` e o sandbox Docker local com serviços privados |
| `cmd/trilha-runner` | a CLI |

Depende do `trilha-spec` (o protocolo) e dos pacotes `ai` e `ai/mcp` do framework `trilha` —
nada fora da biblioteca padrão do Go além desses dois módulos.

## Licença

MIT.
