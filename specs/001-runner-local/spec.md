# Spec 001 — Runner local (Fase 0)

- **Issue**: — (primeira spec)
- **Branch**: `001-runner-local`
- **Versão**: 0.1.0

## Por quê

O protocolo (`trilha-spec`) diz o que fazer e deixa evidência, mas alguém precisa pegar a
task `ready`, dar a ela um lugar para rodar, chamar o agente, rodar os checks e gravar o que
aconteceu — sem que o agente escreva na cópia de trabalho de quem mantém o projeto. Hoje isso
é feito à mão: abre-se o Claude Code, cola-se o contexto, roda-se o teste, marca-se a task.

## O que muda

- `runner.Runner.Run(ctx, id)`: `ready` → `running` → worktree → driver → commit → `verify` →
  checks → evidência `run` → `review` | `failed`.
- `worktree.Manager`: `Create`, `Remove`, `List`, `Commit`, `DiffStat`; branch `trilha/task-nnn`.
- `driver`: `exec` (agente de linha de comando, prompt no stdin), `ai` (loop `trilha/ai` com
  `read_file`, `list_files`, `write_file`, `run` restritos ao worktree e ao manifesto; ferramentas
  MCP do `trilha-spec` quando instalado), `echo` (determinístico).
- `queue`: `Local` (grafo) e `Remote` (contrato HTTP do trilha-cloud: `GET /api/runs/next`,
  `POST /api/runs/{id}/result`, `POST /api/workers/heartbeat`).
- `sandbox.Sandbox` com `None`; a costura para o cloud.
- CLI `trilha-runner run | next | worker | worktree | drivers`.

## Fora de escopo

- Sandbox com container/VM: trilha-cloud (spec própria; a interface já existe).
- Push do branch e abertura de PR: próxima spec do runner (`--push`, `--pr`).
- Paralelismo local (N workers no mesmo checkout): precisa de lock no store do protocolo.
- Driver Anthropic nativo: o `exec` com `claude -p -` cobre; o `ai` cobre protocolo OpenAI.

## Constitution Check

| Princípio | Como respeita |
|---|---|
| I — protocolo manda | só `trilha-spec` toca `.trilha/`; runner grava `run` com `meta` |
| II — cópia sagrada | `worktree.Manager` em `.trilha/runs/`; teste confere que a raiz não muda |
| III — evidência na falha | `fail()` em `Runner.Run` grava `run` com `stage` |
| IV — driver extensão | `driver.Register`; cerca em `inside()` e no manifesto |
| V — reaproveitar | `trilha/ai` e `trilha/ai/mcp` importados, não copiados |
| VI — sem rede | `echo` + `Local`; e2e da CLI usa `echo` |

## Tarefas

- [x] T001 `worktree.Manager` com branch por task e reuso
- [x] T002 `driver` exec/ai/echo com ferramentas cercadas
- [x] T003 `runner.Run` completo com evidência em toda saída
- [x] T004 `queue.Local` e `queue.Remote` (contrato do cloud)
- [x] T005 CLI + worker + e2e
- [x] T006 README nas duas línguas

## Aceitação

- **SC-001** `trilha-runner next --driver echo` num repositório com uma task `ready` termina com a task em `review`, um commit em `trilha/task-001` e três registros de evidência.
- **SC-002** A raiz do repositório não ganha arquivo nenhum durante a execução.
- **SC-003** Um agente `exec` que sai com código ≠ 0 leva a task a `failed` com registro `run` de estágio `execution`.
- **SC-004** `queue.Remote` fala o contrato HTTP com `Authorization: Bearer` e trata 204 como fila vazia.
