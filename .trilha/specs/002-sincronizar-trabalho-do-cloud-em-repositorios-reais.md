---
id: 002-sincronizar-trabalho-do-cloud-em-repositorios-reais
title: Sincronizar trabalho do Cloud em repositorios reais
status: approved
---

# Sincronizar trabalho do Cloud em repositorios reais

## Why

O worker pressupõe hoje que o checkout e o protocolo `.trilha` já existem na
máquina. Isso impede que uma spec criada na UI do Cloud se torne uma execução
real sem preparação manual do repositório.

## What changes

- O contrato remoto passa a oferecer um bundle versionado da spec, rodada e
  task já reclamada.
- O worker aceita uma URL Git e uma raiz de workspaces, clona ou atualiza o
  repositório e materializa o bundle usando as bibliotecas do `trilha-spec`.
- A sincronização usa uma branch `trilha/spec-*`, commit dedicado e push
  opcional. A implementação continua em `trilha/task-*` e também pode ser
  publicada após os checks.
- Deploy keys e configuração SSH permanecem no host do worker por meio de
  `GIT_SSH_COMMAND`; nenhum segredo Git é transmitido ao Cloud.

## Out of scope

- Armazenar credenciais Git no Cloud.
- Fazer merge automático em branch protegida.
- Executar shell recebido da API.

## Acceptance

- **SC-001** O worker clona um repositório ausente e atualiza com fast-forward
  um checkout existente.
- **SC-002** O bundle cria uma spec e tasks válidas no protocolo aberto sem
  sobrescrever silenciosamente conteúdo divergente.
- **SC-003** A sincronização produz commit rastreável e, quando habilitada,
  publica a branch da spec e a branch da task.
- **SC-004** O fluxo legado em um checkout local continua funcionando.

## Security impact

- URL, branch e caminhos são validados; processos são executados por programa e
  argumentos, nunca por shell.
- A chave privada Git é configurada somente no serviço do host e deve usar
  permissão `0600`, host key checking e acesso ao repositório necessário.
