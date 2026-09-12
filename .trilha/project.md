---
name: trilha-runner
description: "Executes tasks of the Trilha protocol: worktree, agent, verification, evidence"
default_agent: coder
verify:
  - go vet ./...
  - go test ./...
---

# trilha-runner

The local runner of the Trilha strategy: takes a `ready` task from `.trilha/`, checks out a
branch for it, starts the agent its manifest names, commits, runs the checks in the worktree
and records evidence. Go; depends on `trilha-spec` and on `trilha/ai`.

## Commands

- build: `go build ./cmd/trilha-runner`
- test: `make test`

## Where things are

- `runner/` pipeline · `driver/` exec, ai, echo · `worktree/` git · `queue/` local + remote · `sandbox/` seam
