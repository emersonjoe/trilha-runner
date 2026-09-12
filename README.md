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
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # drops the checkout, keeps the branch
```

## Drivers

The agent manifest (`.trilha/agents/<name>.md`) names a driver; `--driver` overrides it.

| Driver | What it does |
|---|---|
| `exec` | starts a command-line agent (`claude -p -`, `codex exec -`, `aider --message-file -`…) with the context pack on stdin, inside the worktree; `TRILHA_TASK` and `TRILHA_WORKTREE` are set |
| `ai` | runs the agent loop of [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) on any OpenAI-protocol model with `read_file`, `list_files`, and — only when the manifest lists `write` / `run` — `write_file` and `run`, all scoped to the worktree; when `trilha-spec` is in PATH the model also gets the protocol's read-only MCP tools |
| `echo` | no model; writes the prompt's first line to a file. What the tests use |

Whatever the driver, the fence is the same: the worktree, the manifest's tools, and the checks.

## Evidence

Each run leaves in `.trilha/evidence/TASK-NNN/`:

- one `check` record per command in the task's `checks:` (and `project.verify`) — exit code,
  output, sha256, run in the worktree;
- one `run` record — driver, branch, commit, diff stat, model/turns/tokens when the driver
  knows them, the tail of the agent's output, and a pointer to `.trilha/runs/TASK-NNN/agent.log`.

A failed start (no command, not a git repository, agent exited non-zero) is a `run` record
with the stage that failed, and the task goes to `failed`.

## Worker: the bridge to trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.example --token $TOKEN --project my-app
```

A worker is one process on one checkout. It asks the control plane for the next run
(`POST /api/runs/next`), executes it locally exactly as above, and reports the result and the
evidence (`POST /api/runs/{id}/result`). The code never leaves the machine; the cloud sees
status, branch, commit and evidence. `queue.Remote` is the whole contract, so another control
plane can implement it.

## Packages

| Package | What it is |
|---|---|
| `runner` | the pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `ai`, `echo`; `driver.Register` for your own |
| `worktree` | git worktrees per task, commit, diff stat |
| `queue` | `Local` (the graph) and `Remote` (trilha-cloud client) |
| `sandbox` | the seam for containers and VMs; `None` runs in the worktree |
| `cmd/trilha-runner` | the CLI |

Depends on `trilha-spec` (the protocol) and on the `ai` and `ai/mcp` packages of the `trilha`
framework — nothing outside the Go standard library beyond those two modules.

## License

MIT.
