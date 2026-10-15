# Testes

> [🇺🇸 English](testing.md) · 🇧🇷 Português

Como o `trilha-runner` é testado e o que uma mudança precisa trazer junto. As jornadas de
usuário e seus casos de teste estão em [journeys.pt-BR.md](journeys.pt-BR.md).

## As sete práticas, aqui

O padrão segue as
[sete melhores práticas de testes de ponta a ponta da IBM](https://www.ibm.com/br-pt/think/insights/end-to-end-testing-best-practices).
Onde cada uma vive neste repositório:

| Prática | Aqui |
|---|---|
| 1. Comece pelos usuários e processos centrais, pelo risco | a ordem de risco abaixo; jornadas em [journeys.pt-BR.md](journeys.pt-BR.md) nessa ordem |
| 2. Crie, documente e gerencie os casos de teste | todo caso tem recursos, pré-condições, dados, passos e esperado; o `internal/catalog` reprova a suíte quando um caso está incompleto, cita teste inexistente, um teste não tem caso, ou as línguas diferem |
| 3. Ambiente parecido com produção, dados realistas, teardown | git real, o contrato real do cloud via `httptest`, Docker real (obrigatório no CI), Linux **e** macOS no CI; dados sintéticos garantidos por `TestFixturesAreSynthetic`; `t.TempDir` e o teardown do sandbox |
| 4. Automatize, no pipeline | `make ci` localmente e em `.github/workflows/ci.yml` em todo push e pull request |
| 5. Testes contínuos, com métricas | `make report` em toda rodada do CI (passaram/falharam/pulados, pulos com motivo, testes mais lentos, cobertura por pacote) contra um piso de cobertura que só sobe; `nightly.yml` roda `make flaky` toda noite |
| 6. Ponta a ponta junto de unidade e integração | a pirâmide abaixo |
| 7. Comunique-se com o time | o `.github/pull_request_template.md` pede a jornada, os casos, os testes por nível, os números, o que ficou sem teste e o aceite em palavras do negócio |

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
| Integração | as fronteiras: repositórios `git` reais (`worktree`, `runner`, `syncer`), o contrato HTTP do cloud com `httptest` (`queue`, worker), o protocolo pelo `trilha-spec`, Docker com containers reais (`sandbox`) | os mesmos pacotes | `go test`; o caso Docker precisa de Docker — obrigatório no CI, pulado fora dele sem Docker, dizendo por quê |
| Ponta a ponta | o binário da CLI como o usuário roda, num repositório git novo: `cmd/trilha-runner/e2e_test.go` | `cmd/trilha-runner` | `TestMain` compila o binário; `go test` o executa |

Muitos testes de unidade, integração em toda fronteira, poucos casos ponta a ponta — um por
jornada crítica. Caso extremo e exceção vão para unidade ou integração, nunca para o E2E.

A jornada do usuário aqui é a linha de comando, então "o navegador do usuário" é o sistema
operacional dele: os casos ponta a ponta e a integração com git rodam em Linux e macOS no CI.
Não há interface web, então não há suíte de navegador; se um dia houver, os casos E2E dela
rodam em Chromium, Firefox e WebKit no CI.

## Ambiente parecido com produção

- **Git de verdade, não um fake.** Os testes criam repositórios em `t.TempDir()` com
  `git init` e um commit, como o projeto de um mantenedor. O `t.TempDir()` os remove.
- **O driver `echo`** faz o papel do modelo: o pipeline inteiro roda sem rede nem chave. O
  driver `exec` com `sh -c` faz o papel de um agente de linha de comando.
- **O cloud é um `httptest.Server`** falando o mesmo contrato do trilha-cloud; a fixture
  `execution-v1` é comparada byte a byte com a do `trilha-spec` (`make conformance`).
- **O Docker roda containers reais** (`postgres:16-alpine`) numa rede privada, removidos em
  todo caminho. O CI define `TRILHA_REQUIRE_DOCKER=1` nos runners hospedados pelo GitHub, e
  esse caso falha em vez de ser pulado quando falta Docker; o runner self-hosted o reporta
  como pulado.
- **Linux e macOS.** Quem mantém roda o runner no macOS e servidores rodam em Linux; o job
  `host` roda `make host` (o binário ponta a ponta e os pacotes de git) no macOS.

## Dados de teste

Só sintéticos. Títulos de task, nomes de projeto, tokens e credenciais nas fixtures são
inventados (`secret`, `t@t`, `gateway.example.br`). **Nunca** dado pessoal real, token real
ou conteúdo de repositório real em fixture, no repositório ou em log. O
`TestFixturesAreSynthetic` garante: e-mails precisam ser `t@t`, `…@example…` ou
`…@trilha.local`; texto com formato de credencial (`sk-`, `ghp_`, `github_pat_`, `xox?-`,
`AKIA`) precisa se declarar falso com `FAKE`, `EXAMPLE` ou `SECRET`; um teste só cita hosts
sob `example`, loopback ou `.local`. Teste que cria algo fora do `t.TempDir()` — container,
rede, arquivo — remove com `t.Cleanup` ou `defer`.

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
| `make host` | o binário ponta a ponta e os pacotes de git — o que o job macOS roda |
| `make report` | o relatório em markdown: passaram/falharam/pulados, o motivo de cada pulo, testes mais lentos, cobertura por pacote; reprova com falha ou cobertura abaixo do piso |
| `make cover` | cobertura por pacote em `coverage.out` |
| `make ci` | `test` + `race`, o que o CI roda |

O CI (`.github/workflows/ci.yml`) roda `make test` em Go 1.22 e 1.25, `make report` no 1.25
(no resumo do job), `make race`, e `make host` no macOS, em todo push e pull request. O
`nightly.yml` roda `make flaky` e o relatório toda noite.

## Métricas e o piso de cobertura

O `testdata/coverage-floor.txt` guarda a cobertura total mínima que o CI aceita. Ele só sobe:
quando o relatório diz que a cobertura está um ponto ou mais acima do piso, a mudança o sobe.
Mudança que fica abaixo acrescenta os testes que faltam; o piso não é rebaixado para caber.
Leia as listas de **pulados** e **mais lentos** do relatório a cada release: pulo que
ninguém explica e teste que só fica mais lento são trabalho a agendar.

## Regras para uma mudança

- **Correção de bug começa com um teste que falha.** Escreva o teste de regressão, veja-o
  falhar no código atual, depois corrija. O commit leva os dois.
- **Comportamento novo chega com seus testes** no nível mais baixo que o pega, e todo teste
  novo entra num caso de [journeys.pt-BR.md](journeys.pt-BR.md) e da versão em inglês — a
  guarda do catálogo reprova sem isso.
- **Rode a suíte inteira antes de dizer que terminou** e reporte os números: passaram,
  falharam, pulados (as linhas `ok`/`FAIL` do `make ci`).
- **Teste instável nunca é silenciado.** Nada de `t.Skip` para deixar o CI verde, nada de
  laço de retry em volta de uma asserção. Corrija a causa, ou abra uma issue com o rótulo
  `bug` e um dono e aponte-a no teste. Skip só para capacidade de plataforma ausente (Windows
  sem `sh`, máquina sem Docker), dizendo qual.
- **Diga o que ficou sem teste e por quê** no pull request, em palavras que o negócio lê —
  o template de pull request pede isso.
