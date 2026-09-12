# trilha-runner Constitution

`trilha-runner` executa tasks do protocolo Trilha numa máquina do usuário: worktree por task,
agente por manifesto, verificação por checks, evidência por execução, e o worker que liga isso
ao trilha-cloud.

## Core Principles

### I. O protocolo manda (NON-NEGOTIABLE)
O runner lê e escreve `.trilha/` só pelo módulo `trilha-spec`. Status, transições, formato de
evidência e pacote de contexto não são redefinidos aqui; o que o runner acrescenta é o registro
`run` com `meta` livre, como o protocolo permite.

### II. A cópia de trabalho é sagrada
Todo agente roda num worktree em `.trilha/runs/TASK-NNN/wt`, no branch `trilha/task-nnn`. Nada
escreve na cópia do mantenedor. O que o agente fez é um commit num branch: revisável,
descartável, publicável.

### III. Evidência sempre, inclusive na falha
Uma execução que não chega a rodar (sem comando, sem git, driver desconhecido) deixa um
registro `run` com o estágio que falhou e move a task para `failed`. Não existe execução sem
rastro.

### IV. Driver é ponto de extensão, cerca não é
`exec`, `ai` e `echo` são drivers; `driver.Register` aceita outros. A cerca — worktree, lista
de ferramentas do manifesto, checks — é a mesma para todos e não é configurável por driver.

### V. Reaproveitar o framework, não copiá-lo
Cliente de modelo, loop de agente e cliente MCP vêm de `github.com/emersonjoe/trilha/ai`. Fora
`trilha-spec` e `trilha`, nenhuma dependência.

### VI. Testável sem rede
O driver `echo` e o `Local` queue permitem testar o pipeline inteiro com `go test`, sem chave
nem rede. Todo caminho do `Runner.Run` tem teste que o percorre.

## Idioma

Código e documentação pública em inglês, `README.pt-BR.md` no mesmo commit. Specs e esta
constituição em português do Brasil.

## Fluxo de trabalho

Spec em `specs/NNN-nome/` (spec-kit) e task em `.trilha/tasks/` do próprio repositório; o
runner roda as próprias tasks. Spec curta para mudança pequena.

**Version**: 1.0.0 | **Ratified**: 2026-09-12 | **Last Amended**: 2026-09-12
