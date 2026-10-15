# Testes

> [🇺🇸 English](testing.md) · 🇧🇷 Português

Como o `trilha-runner` é testado e o que uma mudança precisa trazer junto. As jornadas de
usuário e seus casos de teste estão em [journeys.pt-BR.md](journeys.pt-BR.md).

## Comece pelo usuário e pelo risco

Antes de codar, nomeie a jornada que a mudança toca (veja o catálogo) e o pior que acontece
se ela quebrar. A ordem de risco neste projeto:

1. **A cópia de trabalho do mantenedor é escrita.** Trabalho perdido ou misturado no
   repositório de alguém.
2. **Um segredo vaza** — para uma linha de comando, a evidência, um log ou o cloud.
3. **Uma execução termina sem evidência**, ou com evidência dizendo que passou quando não
   passou.
4. **Uma task vai para o status errado**, e um revisor aceita trabalho que nunca rodou os
   checks.
5. **O worker para de reportar** — o cloud mostra uma execução travada ou perdida.

Mudança em um desses ganha o caso no catálogo antes do código.

## A pirâmide

| Nível | O que cobre aqui | Onde | Como roda |
|---|---|---|---|
| Unidade | regras, bordas, exceções: classificação de retry, parse de dependência, flags de repositório, comparadores de eval, redação | `*_test.go` ao lado do código | `go test`, sem rede, sem chave |
| Integração | as fronteiras: repositórios `git` reais (`worktree`, `runner`), o contrato HTTP do cloud com `httptest` (`queue`, worker), o protocolo pelo `trilha-spec`, Docker com containers reais (`sandbox`) | os mesmos pacotes | `go test`; o caso Docker precisa de Docker e é pulado sem ele |
| Ponta a ponta | o binário da CLI como o usuário roda, num repositório git novo: `cmd/trilha-runner/e2e_test.go` | `cmd/trilha-runner` | `TestMain` compila o binário; `go test` o executa |

Muitos testes de unidade, integração em toda fronteira, poucos casos ponta a ponta — um por
jornada crítica. Caso extremo e exceção vão para unidade ou integração, nunca para o E2E.

Este projeto não tem interface web, então não há suíte de navegador. Se um dia ganhar, os
casos E2E dela rodam em Chromium, Firefox e WebKit no CI.

## Ambiente parecido com produção

- **Git de verdade, não um fake.** Os testes criam repositórios em `t.TempDir()` com
  `git init` e um commit, como o projeto de um mantenedor. O `t.TempDir()` os remove.
- **O driver `echo`** faz o papel do modelo: o pipeline inteiro roda sem rede nem chave. O
  driver `exec` com `sh -c` faz o papel de um agente de linha de comando.
- **O cloud é um `httptest.Server`** falando o mesmo contrato do trilha-cloud; a fixture
  `execution-v1` é comparada byte a byte com a do `trilha-spec` (`make conformance`).
- **O Docker roda containers reais** (`pgvector/pgvector:pg16`, `alpine`) numa rede privada,
  removidos em todo caminho.

## Dados de teste

Só sintéticos. Títulos de task, nomes de projeto, tokens e credenciais nas fixtures são
inventados (`secret`, `t@t`, `gateway.example.br`). **Nunca** dado pessoal real, token real
ou conteúdo de repositório real em fixture, no repositório ou em log. Teste que cria algo
fora do `t.TempDir()` — container, rede, arquivo — remove com `t.Cleanup` ou `defer`.

## Comandos

Um comando roda tudo o que o CI roda:

```bash
make ci
```

| Comando | O que faz |
|---|---|
| `make test` | gofmt, `go vet`, `go test ./...` — antes de todo commit |
| `make race` | `go test -race ./...` |
| `make flaky` | a suíte três vezes, embaralhada, com `-race` — antes de dar um teste instável por corrigido |
| `make e2e` | só os casos ponta a ponta da CLI |
| `make cover` | cobertura por pacote em `coverage.out` |
| `make ci` | `test` + `race`, o que o CI roda |

O CI (`.github/workflows/ci.yml`) roda `make test` em Go 1.22 e 1.25 e `make race` em todo
push e pull request.

## Regras para uma mudança

- **Correção de bug começa com um teste que falha.** Escreva o teste de regressão, veja-o
  falhar no código atual, depois corrija. O commit leva os dois.
- **Comportamento novo chega com seus testes** no nível mais baixo que o pega, e com seu caso
  em [journeys.pt-BR.md](journeys.pt-BR.md) quando toca uma jornada.
- **Rode a suíte inteira antes de dizer que terminou** e reporte os números: passaram,
  falharam, pulados (as linhas `ok`/`FAIL` do `make ci`).
- **Teste instável nunca é silenciado.** Nada de `t.Skip` para deixar o CI verde, nada de
  laço de retry em volta de uma asserção. Corrija a causa, ou abra uma issue com o rótulo
  `bug` e um dono e aponte-a no teste. Skip só para capacidade de plataforma ausente (Windows
  sem `sh`, máquina sem Docker), dizendo qual.
- **Diga o que ficou sem teste e por quê** no pull request, em palavras que o negócio lê.
