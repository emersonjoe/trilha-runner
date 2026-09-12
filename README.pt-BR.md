# trilha-runner

> [🇺🇸 English](README.md) · 🇧🇷 Português

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
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # remove o checkout, mantém o branch
```

## Drivers

O manifesto do agente (`.trilha/agents/<nome>.md`) nomeia um driver; `--driver` sobrepõe.

| Driver | O que faz |
|---|---|
| `exec` | inicia um agente de linha de comando (`claude -p -`, `codex exec -`, `aider --message-file -`…) com o pacote de contexto no stdin, dentro do worktree; `TRILHA_TASK` e `TRILHA_WORKTREE` vão no ambiente |
| `ai` | roda o loop de agente do [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) em qualquer modelo com protocolo OpenAI, com `read_file`, `list_files` e — só quando o manifesto lista `write` / `run` — `write_file` e `run`, tudo restrito ao worktree; com `trilha-spec` no PATH o modelo recebe também as ferramentas MCP só-leitura do protocolo |
| `echo` | sem modelo; escreve a primeira linha do prompt num arquivo. É o que os testes usam |

Seja qual for o driver, a cerca é a mesma: o worktree, as ferramentas do manifesto e os checks.

## Evidência

Cada execução deixa em `.trilha/evidence/TASK-NNN/`:

- um registro `check` por comando em `checks:` da task (e `project.verify`) — código de saída,
  saída, sha256, rodado no worktree;
- um registro `run` — driver, branch, commit, diff stat, modelo/turnos/tokens quando o driver
  sabe, o fim da saída do agente e um ponteiro para `.trilha/runs/TASK-NNN/agent.log`.

Uma partida que falha (sem comando, sem repositório git, agente saiu com erro) é um registro
`run` com o estágio que falhou, e a task vai para `failed`.

## Worker: a ponte para o trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.exemplo --token $TOKEN --project meu-app
```

Um worker é um processo num checkout. Pede ao control plane a próxima execução
(`POST /api/runs/next`), executa localmente exatamente como acima e reporta resultado e
evidência (`POST /api/runs/{id}/result`). O código não sai da máquina; o cloud vê status,
branch, commit e evidência. `queue.Remote` é o contrato inteiro, então outro control plane
pode implementá-lo.

## Pacotes

| Pacote | O que é |
|---|---|
| `runner` | o pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `ai`, `echo`; `driver.Register` para o seu |
| `worktree` | worktrees git por task, commit, diff stat |
| `queue` | `Local` (o grafo) e `Remote` (cliente do trilha-cloud) |
| `sandbox` | a costura para containers e VMs; `None` roda no worktree |
| `cmd/trilha-runner` | a CLI |

Depende do `trilha-spec` (o protocolo) e dos pacotes `ai` e `ai/mcp` do framework `trilha` —
nada fora da biblioteca padrão do Go além desses dois módulos.

## Licença

MIT.
