---
id: TASK-008
title: Materializar bundles e operar repositorios remotos
status: done
spec: 002-sincronizar-trabalho-do-cloud-em-repositorios-reais
agent: coder
depends_on: []
acceptance:
  - worker clona ou atualiza o repositorio configurado
  - bundle gera arquivos trilha-spec e commit rastreavel
  - branches de spec e implementacao podem ser publicadas
  - credencial Git permanece somente no host executor
checks:
  - go test ./...
  - make test
created: "2026-09-13T21:22:42Z"
updated: "2026-09-13T21:43:56Z"
---
