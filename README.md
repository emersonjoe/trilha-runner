# trilha-runner

> 🇺🇸 English · [🇧🇷 Português](README.pt-BR.md)
>
> **Hands-on chapter on the Trilha site:** <https://emersonjoe.github.io/trilha/learn/agentic-runner>

**Executes tasks of the [Trilha protocol](https://github.com/emersonjoe/trilha-spec): one task,
one agent, one worktree, verified, with evidence.**

```
Task → Agent → Worktree → Execution → Verification → Evidence
```

`trilha-spec` says *what* to do; `trilha-runner` is *how* it runs on a machine you control.
It reads `.trilha/`, picks the task that is ready with every dependency done, checks out a
branch for it (`trilha/task-nnn` in `.trilha/runs/TASK-NNN/wt`), hands the agent named in the
task's manifest the context pack, commits what the agent changed, runs the task's checks in
the worktree, records every result as evidence and moves the task to `review` — or `failed`.
Your working copy is never touched.

## Install and run

```bash
go install github.com/emersonjoe/trilha-runner/cmd/trilha-runner@latest

# In a git repository with .trilha/ (trilha-spec init):
trilha-runner next                          # first executable task, agent from its manifest
trilha-runner run TASK-003 --driver exec --cmd "claude -p -"
trilha-runner run TASK-003 --driver claude-code             # the preset: fixed argv, credential from the run
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner run TASK-003 --sandbox docker                 # agent and checks in a container with services
trilha-runner next --repo trilha=../trilha                  # respects dependencies in a sibling checkout
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # drops the checkout, keeps the branch
```

## Drivers

The agent manifest (`.trilha/agents/<name>.md`) names a driver; `--driver` overrides it.

| Driver | What it does |
|---|---|
| `claude-code` | `exec` with the argv fixed by the runner (`claude -p - --permission-mode acceptEdits`) and the credential from the run, under `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY` |
| `exec` | starts a command-line agent (`claude -p -`, `codex exec -`, `aider --message-file -`…) with the context pack on stdin, inside the worktree; `TRILHA_TASK` and `TRILHA_WORKTREE` are set |
| `ai` | runs the agent loop of [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) on any OpenAI-protocol model with `read_file`, `list_files`, and — only when the manifest lists `write` / `run` — `write_file` and `run`, all scoped to the worktree; when `trilha-spec` is in PATH the model also gets the protocol's read-only MCP tools |
| `echo` | no model; writes the prompt's first line to a file. What the tests use |

Whatever the driver, the fence is the same: the worktree, the manifest's tools, and the checks.

### Model access per project

A claim from the control plane may carry the project's own model access —
provider, base URL, model, credential and the hosts that credential may be spent on. The
worker never writes it down: it reaches the agent as process environment and nowhere else, and
it is redacted from the output the driver keeps and reports. When `allowed_hosts` is present,
a base URL outside it is refused **before the first request**: the refusal is a `run` record
with `stage: policy` and the task goes to `failed`, not `review`. A project whose data must
stay in the country says so once, and no worker's own configuration can override it.

## Sandbox: the agent and the checks in a container

The worktree is a sandbox for what an agent may *touch*; it is not one for what the checks
*need*. A product whose API suite needs Postgres declares it in the agent manifest:

```markdown
---
name: coder
role: Implements a task inside its own worktree and produces evidence.
driver: exec
command: claude -p -
tools: [read, write, run]
sandbox: {"image":"golang:1.22","services":[{"name":"postgres","image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"trilha","POSTGRES_DB":"acervo"},"ready":["pg_isready","-U","postgres"]}]}
---
```

`sandbox:` may also name a JSON file relative to the repository root
(`sandbox: .trilha/sandboxes/api.json`). With `--sandbox docker`, the runner starts the
services on a network of their own, waits for each one's `ready` argv, then runs the agent and
the checks in a container on that network, where a service answers by its name.

- The worktree is the only writable path that survives: the root filesystem is read-only and
  `/tmp` is a tmpfs that dies with the container.
- The limits are the runner's, not the manifest's — one CPU, 1 GiB of memory, 256 PIDs,
  `no-new-privileges`, `cap-drop ALL` — because a declaration that can raise its own ceiling
  bounds nothing.
- The agent is not root: it runs as the user that owns the worktree. With every capability
  dropped there is no `CAP_DAC_OVERRIDE` to fall back on, so this is what makes the worktree
  writable and everything else not.
- Nothing mounts the Docker socket. A sandbox that can talk to the daemon is not a sandbox, so
  this exists only where the worker runs, on a host the operator labelled `docker`, never in a
  control plane.
- Whatever `Prepare` created is removed afterwards, including when it failed partway through.
- The evidence records the wrapped command, so what ran is what the record says.

## Cross-repository dependencies

A task in a product repository may wait for a task in another one. The protocol spells it with
the alias in front, keeps it out of this repository's order, and refuses to start a task whose
alias nobody answered for:

```markdown
---
id: TASK-005
title: Import from the framework
status: ready
depends_on:
  - TASK-004          # this repository
  - trilha:TASK-004   # another one
---
```

What the protocol does not decide is what an alias *means* — that is a machine's business, not
a document's. `--repo` says:

```bash
trilha-runner next --repo trilha=../trilha --repo cloud=../trilha-cloud
# · TASK-005: trilha:TASK-004
```

The task is not offered while the dependency is open, and the reason names it. A dependency
nobody could answer for reads differently — `waiting:trilha:TASK-004` — because a dependency
that cannot be seen is not one that has been met; the runner never reads it as done. The
resolver goes on the store, so `next` and the run that follows it cannot disagree about whether
a task may start.

A Cloud-connected worker resolves the same dependency against the control plane
(`GET /api/projects/{alias}/tasks/{id}`) instead of a path, so it needs no checkout of the
other project.

## Evidence

Each run leaves in `.trilha/evidence/TASK-NNN/`:

- one `check` record per command in the task's `checks:` (and `project.verify`) — exit code,
  output, sha256, run in the worktree;
- one `eval` record per metric line a check printed (below);
- one `run` record — driver, branch, commit, diff stat, model/turns/tokens when the driver
  knows them, the metrics, the tail of the agent's output, and a pointer to
  `.trilha/runs/TASK-NNN/agent.log`.

A failed start (no command, not a git repository, agent exited non-zero) is a `run` record
with the stage that failed, and the task goes to `failed`.

### Metrics: a check that measures rather than asserts

A quality harness — triage accuracy on a labelled set, a translation score, a p95 latency,
accessibility violations — proves its result with a number against a threshold, and a number
buried in a command's output is invisible to a reviewer. So the harness prints one line of
JSON per number and the runner turns each into an `eval` record beside the `check` record that
carried it:

```json
{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}
{"metric":"p95_latency_ms","value":410,"threshold":300,"comparator":"<=","unit":"ms"}
```

The comparators are `>=`, `<=` and `==` — a gate is "at least", "at most" or "exactly", and
anything subtler is a statistical test rather than a gate. **A comparator that is not satisfied
fails the verification even when every command exited 0**, so the task goes to `failed` and the
`run` record's summary lists the metrics. A metric with no threshold is a measurement, not a
gate: it is recorded to be trended and it fails nothing.

An optional `dataset` says what was measured against. It carries the hash of the set's
*manifest*, never its content — a golden set holds real cases, and real cases hold personal
data:

```json
{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">=",
 "dataset":{"id":"triage-v3","sha256":"9f86d081…"}}
```

Any other line a check prints is ordinary output and is ignored. The scanning, the records and
the gating are the protocol's (`task.RunChecks`); what the runner adds is running them where
the sandbox says and putting the numbers in the `run` record's summary.

A task can also declare the gate as an acceptance criterion, which `trilha spec doctor` then
checks was actually evidenced:

```markdown
acceptance:
  - "metric: triage_top1 >= 0.85"
```

## Worker: the bridge to trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.example --token "$TOKEN" --project my-app \
  --workspace-root /var/lib/trilha-runner/workspaces \
  --repo git@github.com:acme/my-app.git --default-branch main --push
```

A worker is one process on one checkout. It asks the control plane for the next run
(`POST /api/runs/next`), executes it locally exactly as above, and reports the result and the
evidence (`POST /api/runs/{id}/result`). The code never leaves the machine; the cloud sees
status, branch, commit and evidence. `queue.Remote` is the whole contract, so another control
plane can implement it.

### What this host can do, and how much of it

```bash
trilha-runner worker --cloud https://cloud.example --token "$TOKEN" --project my-app \
  --label docker --label gpu --label region:br --capacity 2
```

A fleet has more than one kind of host: one without Docker, one with a GPU, one in a region a
project's data may not leave. `--label` (repeatable, `TRILHA_WORKER_LABELS`) and `--capacity`
(`TRILHA_WORKER_CAPACITY`) ride both the heartbeat and the claim, together with the runs in
flight, the runner version and the drivers this host has, so the control plane can route a run
that needs Postgres for its checks to the host that has Docker. A run whose requirements this
worker does not meet is refused and reported back, never executed silently.

With `--capacity` above 1 the worker keeps that many runs in flight, each in its own worktree
and reported independently, and it claims a slot before it claims a run — a host that has no
slot leaves the work for another.

For work created in the UI, the worker downloads a versioned bundle, materializes its
specification and tasks under `.trilha/`, creates the implementation branch and publishes it.
### Delivery profiles

Passing `--delivery-config /etc/trilha-runner/delivery.json` enables deploy and rollback through
local profiles with fixed argument arrays; Cloud never supplies commands. Secrets exist only as
environment variables during that operation and are not written to logs. Hardened eoslab
`systemd`, environment and profile examples live under `deploy/eoslab/`.

A profile that is a compose stack with a database needs an ordered sequence with a gate in the
middle, so it declares one instead of hiding it in a wrapper script:

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
  "health": [{ "name": "api", "url": "https://app.example.com/health/ready", "timeout_seconds": 180 }]
}
```

- `steps[]` run in order. `on_fail: continue` is the only way past a failing step.
- `migrate` runs immediately before the step marked `switch`: the schema moves before traffic
  does, and a migration that fails aborts the delivery with the previous revision still serving.
- `health[]` is one check per service, each polled until its own timeout. A service that never
  becomes healthy triggers the rollback.
- The rollback puts the previous image back and reverses the schema **only** when the migration
  declared itself `reversible`; otherwise it is reported as image-only and the log says the
  schema keeps the new shape.
- Each step's exit code and duration are in the log. The flat `deploy` argv stays valid: it is
  a profile with a single step.

## Packages

| Package | What it is |
|---|---|
| `runner` | the pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `claude-code`, `ai`, `echo`; `driver.Register` for your own; `Access` for per-project credentials and residency |
| `worktree` | git worktrees per task, commit, diff stat |
| `queue` | `Local` (the graph) and `Remote` (trilha-cloud client); `Checkouts` resolves a repository alias |
| `sandbox` | `None` runs in the worktree; `Docker` runs the agent and the checks in a container with services |
| `cmd/trilha-runner` | the CLI |

Depends on `trilha-spec` (the protocol) and on the `ai` and `ai/mcp` packages of the `trilha`
framework — nothing outside the Go standard library beyond those two modules.

## License

MIT.
