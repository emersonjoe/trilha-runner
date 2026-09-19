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
trilha-runner run TASK-003 --driver claude-code             # o preset: argv fixa, credencial da execução
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner run TASK-003 --sandbox docker                 # agente e checks num container com serviços
trilha-runner next --repo trilha=../trilha                  # respeita dependências num checkout vizinho
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # remove o checkout, mantém o branch
```

## Drivers

O manifesto do agente (`.trilha/agents/<nome>.md`) nomeia um driver; `--driver` sobrepõe.

| Driver | O que faz |
|---|---|
| `claude-code` | `exec` com a argv fixada pelo runner (`claude -p - --permission-mode acceptEdits`) e a credencial da execução, em `CLAUDE_CODE_OAUTH_TOKEN` ou `ANTHROPIC_API_KEY` |
| `exec` | inicia um agente de linha de comando (`claude -p -`, `codex exec -`, `aider --message-file -`…) com o pacote de contexto no stdin, dentro do worktree; `TRILHA_TASK` e `TRILHA_WORKTREE` vão no ambiente |
| `ai` | roda o loop de agente do [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) em qualquer modelo com protocolo OpenAI, com `read_file`, `list_files` e — só quando o manifesto lista `write` / `run` — `write_file` e `run`, tudo restrito ao worktree; com `trilha-spec` no PATH o modelo recebe também as ferramentas MCP só-leitura do protocolo |
| `echo` | sem modelo; escreve a primeira linha do prompt num arquivo. É o que os testes usam |

Seja qual for o driver, a cerca é a mesma: o worktree, as ferramentas do manifesto e os checks.

### Acesso ao modelo por projeto

A claim do control plane pode trazer o acesso ao modelo do próprio projeto — provedor, base
URL, modelo, credencial e os hosts em que essa credencial pode ser gasta. O worker nunca anota
isso: chega ao agente como ambiente do processo e em nenhum outro lugar, e é redigido da saída
que o driver guarda e reporta. Com `allowed_hosts` presente, uma base URL fora da lista é
recusada **antes da primeira requisição**: a recusa é um registro `run` com `stage: policy` e a
task vai para `failed`, não para `review`. Um projeto cujos dados não podem sair do país diz
isso uma vez, e a configuração do worker não sobrepõe.

## Sandbox: o agente e os checks num container

O worktree é sandbox do que o agente pode *tocar*; não é sandbox do que os checks *precisam*.
Um produto cuja suíte de API precisa de Postgres declara isso no manifesto do agente:

```markdown
---
name: coder
role: Implementa uma task no worktree dela e produz evidência.
driver: exec
command: claude -p -
tools: [read, write, run]
sandbox: {"image":"golang:1.22","services":[{"name":"postgres","image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"trilha","POSTGRES_DB":"acervo"},"ready":["pg_isready","-U","postgres"]}]}
---
```

`sandbox:` também aceita o caminho de um arquivo JSON relativo à raiz do repositório
(`sandbox: .trilha/sandboxes/api.json`). Com `--sandbox docker`, o runner sobe os serviços numa
rede própria, espera a argv `ready` de cada um e então roda o agente e os checks num container
nessa rede, onde um serviço responde pelo nome.

- O worktree é o único caminho gravável que sobrevive: o sistema de arquivos raiz é somente
  leitura e `/tmp` é um tmpfs que morre com o container.
- Os limites são do runner, não do manifesto — uma CPU, 1 GiB de memória, 256 PIDs,
  `no-new-privileges`, `cap-drop ALL` — porque uma declaração que levanta o próprio teto não
  limita nada.
- O agente não é root: roda como o usuário dono do worktree. Com todas as capabilities
  derrubadas não existe `CAP_DAC_OVERRIDE` de reserva, então é isso que torna o worktree
  gravável e todo o resto não.
- Nada monta o socket do Docker. Um sandbox que fala com o daemon não é sandbox, então isto
  existe só onde o worker roda, num host que o operador rotulou `docker`, nunca num control
  plane.
- O que o `Prepare` criou é removido depois, inclusive quando ele falhou no meio.
- A evidência registra o comando embrulhado: o que rodou é o que o registro diz.

## Dependências entre repositórios

Uma task de um repositório de produto pode esperar por uma task de outro. O protocolo escreve
isso com o alias na frente, mantém a dependência fora da ordem deste repositório e recusa
iniciar uma task cujo alias ninguém respondeu:

```markdown
---
id: TASK-005
title: Importar do framework
status: ready
depends_on:
  - TASK-004          # este repositório
  - trilha:TASK-004   # outro
---
```

O que o protocolo não decide é o que um alias *significa* — isso é assunto de máquina, não de
documento. Quem diz é o `--repo`:

```bash
trilha-runner next --repo trilha=../trilha --repo cloud=../trilha-cloud
# · TASK-005: trilha:TASK-004
```

A task não é oferecida enquanto a dependência está aberta, e o motivo a nomeia. Uma dependência
que ninguém conseguiu responder aparece diferente — `waiting:trilha:TASK-004` — porque
dependência que não se consegue ver não é dependência cumprida; o runner nunca a lê como pronta.
O resolvedor fica na store, então o `next` e a execução que vem depois dele não podem discordar
sobre uma task poder começar.

Um worker conectado ao Cloud resolve a mesma dependência contra o control plane
(`GET /api/projects/{alias}/tasks/{id}`) em vez de contra um caminho, então ele não precisa de
checkout do outro projeto.

## Evidência

Cada execução deixa em `.trilha/evidence/TASK-NNN/`:

- um registro `check` por comando em `checks:` da task (e `project.verify`) — código de saída,
  saída, sha256, rodado no worktree;
- um registro `eval` por linha de métrica que um check imprimiu (abaixo);
- um registro `run` — driver, branch, commit, diff stat, modelo/turnos/tokens quando o driver
  sabe, as métricas, o fim da saída do agente e um ponteiro para
  `.trilha/runs/TASK-NNN/agent.log`.

Uma partida que falha (sem comando, sem repositório git, agente saiu com erro) é um registro
`run` com o estágio que falhou, e a task vai para `failed`.

### Métricas: um check que mede em vez de afirmar

Uma bateria de qualidade — acurácia de triagem num conjunto rotulado, nota de tradução, p95 de
latência, violações de acessibilidade — prova o resultado com um número contra um limiar, e um
número enterrado na saída de um comando é invisível para quem revisa. Então a bateria imprime
uma linha de JSON por número e o runner transforma cada uma num registro `eval`, ao lado do
registro `check` que a carregou:

```json
{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}
{"metric":"p95_latency_ms","value":410,"threshold":300,"comparator":"<=","unit":"ms"}
```

Os comparadores são `>=`, `<=` e `==` — um gate é "pelo menos", "no máximo" ou "exatamente", e
qualquer coisa mais fina é teste estatístico, não gate. **Um comparador não satisfeito reprova a
verificação mesmo que todos os comandos tenham saído com 0**, então a task vai para `failed` e o
resumo do registro `run` lista as métricas. Uma métrica sem limiar é medição, não gate: fica
registrada para ser acompanhada e não reprova nada.

O `dataset` opcional diz contra o que se mediu. Ele carrega o hash do *manifesto* do conjunto,
nunca do conteúdo — um conjunto dourado guarda casos reais, e casos reais guardam dados
pessoais:

```json
{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">=",
 "dataset":{"id":"triage-v3","sha256":"9f86d081…"}}
```

Qualquer outra linha que um check imprima é saída comum e é ignorada. A varredura, os registros
e o gate são do protocolo (`task.RunChecks`); o que o runner acrescenta é rodá-los onde o
sandbox manda e colocar os números no resumo do registro `run`.

A task também pode declarar o gate como critério de aceitação, e aí o `trilha spec doctor`
confere se ele foi realmente evidenciado:

```markdown
acceptance:
  - "metric: triage_top1 >= 0.85"
```

## Worker: a ponte para o trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.exemplo --token "$TOKEN" --project meu-app \
  --workspace-root /var/lib/trilha-runner/workspaces \
  --repo git@github.com:acme/meu-app.git --default-branch main --push
```

Um worker é um processo num checkout. Pede ao control plane a próxima execução
(`POST /api/runs/next`), executa localmente exatamente como acima e reporta resultado e
evidência (`POST /api/runs/{id}/result`). O código não sai da máquina; o cloud vê status,
branch, commit e evidência. `queue.Remote` é o contrato inteiro, então outro control plane
pode implementá-lo.

### O que este host sabe fazer, e quanto

```bash
trilha-runner worker --cloud https://cloud.exemplo --token "$TOKEN" --project meu-app \
  --label docker --label gpu --label region:br --capacity 2
```

Uma frota tem mais de um tipo de host: um sem Docker, um com GPU, um numa região de onde os
dados de um projeto não podem sair. `--label` (repetível, `TRILHA_WORKER_LABELS`) e
`--capacity` (`TRILHA_WORKER_CAPACITY`) viajam no heartbeat e na claim, junto das execuções em
voo, da versão do runner e dos drivers que este host tem, para o control plane rotear uma
execução que precisa de Postgres nos checks para o host que tem Docker. Uma execução cujos
requisitos este worker não atende é recusada e reportada, nunca executada em silêncio.

Com `--capacity` acima de 1 o worker mantém essa quantidade de execuções em voo, cada uma no
seu worktree e reportada separadamente, e pega uma vaga antes de pegar a execução — um host sem
vaga deixa o trabalho para outro.

Quando a execução nasce de uma spec criada na interface, o worker baixa um bundle versionado,
sincroniza a spec e as tasks em `.trilha/`, cria o branch de implementação e o publica.

### Perfis de entrega

Para deploy e rollback, `--delivery-config /etc/trilha-runner/delivery.json` habilita apenas
perfis locais com argumentos fixos; o Cloud nunca envia comandos. Segredos são entregues como
variáveis de ambiente apenas durante a operação e não entram nos logs. Exemplos de `systemd`,
ambiente e perfis para eoslab estão em `deploy/eoslab/`.

Um perfil que é uma stack `compose` com banco precisa de uma sequência ordenada com uma trava no
meio, então ele declara essa sequência em vez de escondê-la num script embrulho:

```json
{
  "steps": [
    { "name": "pull",   "argv": ["docker", "compose", "pull"] },
    { "name": "switch", "argv": ["docker", "compose", "up", "-d", "--wait"], "switch": true },
    { "name": "prune",  "argv": ["docker", "image", "prune", "-f"], "on_fail": "continue" }
  ],
  "migrate": {
    "argv": ["docker", "compose", "exec", "-T", "api", "alembic", "upgrade", "head"],
    "downgrade": ["docker", "compose", "exec", "-T", "api", "alembic", "downgrade", "-1"],
    "reversible": true
  },
  "rollback": ["/usr/local/libexec/trilha/rollback-compose"],
  "health": [{ "name": "api", "url": "https://app.exemplo.com/health/ready", "timeout_seconds": 180 }]
}
```

- `steps[]` rodam em ordem. `on_fail: continue` é a única forma de passar por um passo que falha.
- `migrate` roda imediatamente antes do passo marcado `switch`: o schema anda antes do tráfego,
  e uma migração que falha aborta a entrega com a revisão anterior ainda servindo.
- `health[]` é um check por serviço, cada um consultado até o próprio timeout. Um serviço que
  nunca fica saudável dispara o rollback.
- O rollback devolve a imagem anterior e reverte o schema **só** quando a migração se declarou
  `reversible`; caso contrário ele é reportado como só-imagem e o log diz que o schema mantém a
  forma nova.
- O código de saída e a duração de cada passo entram no log. A argv `deploy` plana continua
  válida: é um perfil com um passo só.

## Pacotes

| Pacote | O que é |
|---|---|
| `runner` | o pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `claude-code`, `ai`, `echo`; `driver.Register` para o seu; `Access` para credenciais por projeto e residência |
| `worktree` | worktrees git por task, commit, diff stat |
| `queue` | `Local` (o grafo) e `Remote` (cliente do trilha-cloud); `Checkouts` resolve um alias de repositório |
| `sandbox` | `None` roda no worktree; `Docker` roda o agente e os checks num container com serviços |
| `cmd/trilha-runner` | a CLI |

Depende do `trilha-spec` (o protocolo) e dos pacotes `ai` e `ai/mcp` do framework `trilha` —
nada fora da biblioteca padrão do Go além desses dois módulos.

## Licença

MIT.
