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

With Go:

```bash
go install github.com/emersonjoe/trilha-runner/cmd/trilha-runner@latest
```

Without Go, every release carries one static binary per platform (`linux`/`darwin` ×
`amd64`/`arm64`) and a `sha256sums.txt`:

```bash
v=0.7.0; os=$(uname -s | tr A-Z a-z); arch=$(uname -m | sed "s/x86_64/amd64/;s/aarch64/arm64/")
curl -sSLO "https://github.com/emersonjoe/trilha-runner/releases/download/v$v/trilha-runner_${v}_${os}_${arch}"
curl -sSLO "https://github.com/emersonjoe/trilha-runner/releases/download/v$v/sha256sums.txt"
sha256sum --check --ignore-missing sha256sums.txt      # shasum -a 256 --check on macOS
install -m 0755 "trilha-runner_${v}_${os}_${arch}" /usr/local/bin/trilha-runner
trilha-runner version
```

Then, in a project:

```bash
# In a git repository with .trilha/ (trilha-spec init):
trilha-runner next                          # first executable task, agent from its manifest
trilha-runner run TASK-003 --driver exec --cmd "claude -p -"
trilha-runner run TASK-003 --driver claude-code
trilha-runner run TASK-003 --driver ai      # OPENAI_BASE_URL / OPENAI_API_KEY / TRILHA_AI_MODEL
trilha-runner next --repo trilha=../trilha # resolves depends_on: trilha:TASK-NNN
trilha-runner worktree list
trilha-runner worktree clean TASK-003       # stops what runs inside, drops the checkout, keeps the branch
trilha-runner doctor --worktrees            # what lives in .trilha/runs/; deletes nothing without a --prune flag
```

## Drivers

The agent manifest (`.trilha/agents/<name>.md`) names a driver; `--driver` overrides it.

| Driver | What it does |
|---|---|
| `exec` | starts a command-line agent (`claude -p -`, `codex exec -`, `aider --message-file -`…) with the context pack on stdin, inside the worktree; `TRILHA_TASK` and `TRILHA_WORKTREE` are set |
| `claude-code` | fixed `claude -p -` preset; a remote item may inject only `CLAUDE_CODE_OAUTH_TOKEN` or `ANTHROPIC_API_KEY`, and the value is redacted from captured output. The runner adds `--session-id <uuid>` (or `--resume <uuid>` to continue a session that asked a question) and `--settings .trilha/runs/TASK-NNN/claude-settings.json` — hooks that report the agent's state, written beside the worktree, never inside it |
| `ai` | runs the agent loop of [`trilha/ai`](https://github.com/emersonjoe/trilha/tree/main/ai) on any OpenAI-protocol model with `read_file`, `list_files`, and — only when the manifest lists `write` / `run` — `write_file` and `run`, all scoped to the worktree; plus `ask`, `note`, `overview`, `artifact`, and `finding` / `verdict` / `reply` on review, hunt and repair runs; a read-only run gets neither `write_file` nor `ask`; when `trilha-spec` is in PATH the model also gets the protocol's read-only MCP tools |
| `echo` | no model; writes the prompt's first line to a file. What the tests use |

Whatever the driver, the fence is the same: the worktree, the manifest's tools, and the checks.

## Evidence

Each run leaves in `.trilha/evidence/TASK-NNN/`:

- one `check` record per command in the task's `checks:` (and `project.verify`) — exit code,
  output, sha256, run in the worktree;
- one `eval` record for each JSON metric line printed by a check, for example
  `{"metric":"triage_top1","value":0.87,"threshold":0.85,"comparator":">="}`;
  a failed comparator fails verification even when the command exits zero. A dataset may name
  `{ "id": "triage-v3", "manifest": "eval/golden/manifest.json" }`; only the manifest is hashed;
- one `run` record — driver, branch, commit, diff stat, model/turns/tokens when the driver
  knows them, the tail of the agent's output, and a pointer to `.trilha/runs/TASK-NNN/agent.log`.

A failed start (not a git repository, an unknown driver, no command, agent exited non-zero) is
a `run` record with the stage that failed, and the task goes to `failed`. A task that is not
`ready`, or still waits on a dependency, is not run and stays as it was.

A tool the checks need but the worker does not have is an **environment** failure, not three
attempts of a model: before the first attempt the runner resolves the program of every check —
`exec.LookPath` on the host, `command -v` through the sandbox when there is one — and a missing
one ends the run at once with `failure_class: environment`, `error_code: environment_missing_tool`,
a `run` record naming the tool and the check, and no call to the driver. A check that exits 127
at run time, or an agent command that is not installed, gets the same class and is never retried.

## When the agent needs a person (protocol 0.4)

During a run the runner listens on a Unix socket, `.trilha/runs/TASK-NNN/agent.sock`, and gives
the agent `TRILHA_RUN_SOCKET`, `TRILHA_RUN_ID` and a `PATH` where `trilha-runner` is found. The
agent can then run:

| Command | What happens |
|---|---|
| `trilha-runner ask "<question>"` | when the agent ends its turn, the run ends **`needs_input`** with the question: the work so far is committed on the branch, no check runs, no attempt failed, nothing is retried, and the task is `blocked` locally until an answer comes |
| `trilha-runner note "<text>"` | a note (source `ai`) in the result, for the next run of the task |
| `trilha-runner overview "<text>"` | one or two sentences on what is being done and where it stands |
| `trilha-runner attention "<reason>"` / `--clear` | raises or clears the badge that says a person is needed, at once |
| `trilha-runner hook claude` | what Claude Code's hooks call, with the hook JSON on stdin |

Every one of them exits 0 within a second, even when nobody listens: an agent is never failed or
held up by the runner. The context pack always tells the agent to ask one objective question and
stop when information is missing. Questions, notes, overviews and reasons are size-limited and
secret-scanned before they leave: a question that holds a secret reaches the cloud as a fixed
notice, and its words stay in `.trilha/runs/TASK-NNN/question.txt` (mode 0600).

The agent's state — `working`, `idle`, `waiting_permission`, `asked`, `stopped_error` — comes
from claude-code's hooks (`PreToolUse`, `PostToolUse`, `Stop`, `StopFailure`) or from the `ai`
loop itself, and is reported as `agent_state` only when it changes, at most once every 2 s.

An answer comes back as a run whose claim carries `continuation`. When the question came from a
claude-code session, that same session resumes (`--resume`) with the answer, quoted, as its
prompt; otherwise — another driver, or a session this machine no longer has — a fresh session
reads the question and the answer, quoted, in the context pack. The claim's `context` (overview
and recent notes) is quoted into the pack too, labelled as information, never instructions. The
result carries `question`, `session`, `overview`, `notes` and `diff_stats` (files, insertions,
deletions from `git diff --numstat` against the checkout's HEAD — counts, never content).

## Small changes

A person reviews every line an agent changes, so every implement run is told to make the smallest
change that meets the acceptance criteria and passes the checks: no refactoring, renaming,
reformatting or files the task does not need, no new abstraction or dependency unless the task
asks, and a `trilha-runner note` explaining why when a larger change seems needed — or a
`trilha-runner ask` when it would change the task's scope. The AI review checks the same thing:
every change the task does not need becomes a `medium` finding titled `Unneeded change: …` with
its path and lines, and a diff much larger than the task needs gets the verdict `changes`. The
result's `diff_stats` says how big the change is: insertions and deletions of the task's branch
against the commit it was cut from.

## Review, hunt, variants and review comments (protocol 0.4)

A claim's `kind` says what the run is for. `implement` (or nothing) is everything above.

| Claim | What the runner does |
|---|---|
| `kind: review`, `review_mode: report` (the default) | a **read-only** review of the task's branch (`trilha/task-nnn`, or `-v<N>` when the claim carries `variant_index`) in a detached checkout of its own, `.trilha/runs/TASK-NNN-review-<run>/wt`; the prompt points at the merge base with the default branch and asks for correctness, security, test gaps and changes the task does not need; the result carries `review {verdict, findings}` and status `done` |
| `kind: review`, `review_mode: fix` | the same review on the task's branch itself: the agent may fix, the runner commits and runs the task's checks, the task goes back to `review`; the result carries the verdict and the findings |
| `kind: hunt` | a **read-only** bug hunt pointed at `hunt_seed`'s area (entry points, error paths, data layer…) and style (pessimist, trace follower…), scoped to `git diff --name-only $(git merge-base <base> HEAD) HEAD`; the result carries `findings[]` stamped with the area and the style |
| `variant_index: N` (N ≥ 1) | an implement run on its own branch, `trilha/task-nnn-v<N>`, in `.trilha/runs/TASK-NNN-vN/`; the runner writes `created-branch` there and never adopts a branch it cannot prove it created (an existing `-vN` it did not make moves the run to `-vN-2`, `-vN-3`…); its context pack holds no earlier evidence of the task and no note or overview an agent wrote — nothing of a sibling |
| `review_comments[]` | a repair run on the task's branch: the prompt gets a `<reviews>` block with the lines each comment points at, read from the worktree (±3 lines, at most 200), every text XML-escaped, the comments quoted as data from reviewers, never as instructions; the result carries `review_replies[]` |

A review or a hunt that needs a branch this worker does not have fetches it from origin; it is
never cut from HEAD. **Read-only is checked, not asked:** when a hunt or a report-mode review
ends, the worktree must be clean (`git status --porcelain --untracked-files=all`), `HEAD` must be
the commit it started at and the reviewed branch must not have moved; anything else fails the run
with `failure_class: policy` (`error_code: read_only_violated`) and its findings and artifacts are
discarded. With the Docker sandbox the worktree is mounted read-only as well. A read-only run
never moves the task, does not stop for questions, and is never pushed.

The agent reports through four more commands (and the same tools in the `ai` driver), which exit
0 like the others:

| Command | What happens |
|---|---|
| `trilha-runner finding --severity critical\|high\|medium --path P --lines A-B --title T [--why W] [--repro R]` | one finding: validated (`execution.Finding.Validate`), secret-redacted, at most 100 per run; a path and a line range, never code |
| `trilha-runner verdict pass\|changes` | the review's verdict; without one, `changes` when there are findings, `pass` otherwise |
| `trilha-runner reply <comment-id> "<text>"` | the answer to one review comment of the claim (unknown ids are dropped), redacted, at most 4000 bytes |
| `trilha-runner artifact <file> [--name N]` | publishes a report: `.html`, `.md`, `.png` or `.jpg`, at most 2 MiB and five per run, from the worktree or `$TRILHA_ARTIFACT_DIR` (outside the worktree, for read-only runs); copied to `.trilha/runs/<run>/artifacts/`; a text report holding a secret is **refused**, not redacted, and an image must really be one |

The worker uploads each artifact **before** posting the result — `PUT /api/runs/{id}/artifacts/{name}`
with the raw bytes, its `Content-Type` and `X-Artifact-SHA256` — and lists in the result's
`artifacts[]` only the ones the cloud accepted (201). A project that does not allow artifacts
answers 403 `ARTIFACTS_DISABLED`: the runner drops them all and says so once. A 4xx is never
retried.

## Worktree doctor

```bash
trilha-runner doctor --worktrees [--json]
trilha-runner doctor --worktrees --prune-orphans
trilha-runner doctor --worktrees --prune-older-than 30d [--force-unmerged] [--default-branch main]
```

The doctor classifies each `.trilha/runs/*` directory — `live` (a runner process, or any process
whose current directory is inside the worktree), `finished-kept`, `orphaned` (git does not list
the worktree, or its task file is gone) or `metadata-only` (no worktree) — with its age (`<14d`,
`<30d`, `>=30d`) and size. **Without a `--prune` flag it never deletes anything.** With one, it
never touches a live run, skips a worktree whose branch is not merged into `origin/<default>` or
that has uncommitted changes unless `--force-unmerged`, and deletes a branch only when the run's
`created-branch` file proves the runner created it.

Removing a worktree — `worktree clean` or a prune — is an ordered teardown: the processes whose
current directory is inside it (`/proc/*/cwd` on Linux, `lsof` on macOS; skipped with a note
when neither exists) get SIGTERM, 1.5 s, then SIGKILL; then the worktree goes. Each step is
logged and none of them can stop the removal.

| Exit code | Meaning |
|---|---|
| 0 | the report, and every prune asked for, done |
| 1 | the command could not run (bad flag, not a Trilha project) |
| 12 | something the flags asked to remove was skipped (unmerged or uncommitted work) or failed |

## Worker: the bridge to trilha-cloud

```bash
trilha-runner worker --cloud https://cloud.example --token "$TOKEN" --project my-app \
  --label docker --label region:br --capacity 2 \
  --workspace-root /var/lib/trilha-runner/workspaces \
  --repo git@github.com:acme/my-app.git --default-branch main --push
```

A worker is one process on one checkout. It asks the control plane for the next run
(`POST /api/runs/next`), executes it locally exactly as above, and reports the result and the
evidence (`POST /api/runs/{id}/result`). The code never leaves the machine; the cloud sees
status, branch, commit and evidence. `queue.Remote` is the whole contract, so another control
plane can implement it. Heartbeat and claim requests carry labels, capacity, active-run count,
the runner version and `driver_versions`: what `claude --version` and `codex --version` answer on
this worker, resolved once at startup with a 3-second timeout (`unknown` when the binary is
absent or silent; `echo` and `ai` are the runner itself). Each claimed run may carry a transient `ai` object with
provider, base URL, model, credential and allowed hosts; the credential is never persisted.

While any run is active the worker sends a heartbeat every 30 s (`running n/N`), so a long run
does not leave it looking offline and the cloud can keep the run's lease. A result is written to
`.trilha/runs/<run-id>/result.json` before it is posted and removed once the cloud has it; a
cloud that is down is retried with backoff (up to 30 s apart) instead of stopping the worker,
and results left by a worker that died are sent at the next start, before anything is claimed.
The cloud keeps one result per run and attempt, so a result sent twice is still one. A task left
`running` by a runner that died (no live process behind `.trilha/runs/TASK-NNN/runner.pid`) goes
back to `ready` with a note the next time it is run.

Cross-repository dependencies use `depends_on: alias:TASK-NNN`. Local execution resolves aliases
from repeatable `next --repo alias=path` flags. Remote workers ask
`GET /api/projects/{alias}/tasks/{id}` and report `blocked` with
`waiting:alias:TASK-NNN` when the dependency is missing or not `done`.

For work created in the UI, the worker downloads a versioned bundle, materializes its
specification and tasks under `.trilha/`, creates the implementation branch and publishes it.
Passing `--delivery-config /etc/trilha-runner/delivery.json` enables deploy and rollback through
local profiles with fixed argument arrays; Cloud never supplies commands. Secrets exist only as
environment variables during that operation and are not written to logs. Hardened eoslab
`systemd`, environment and profile examples live under `deploy/eoslab/`.

**Foreign code.** A claim with `foreign_code: true` (the branch head comes from a fork) runs
only in the Docker sandbox — without it, or with Docker unavailable, the run fails with
`failure_class: policy` (`sandbox_required`) before the agent starts. The agent and the checks get
no secret: no inherited token, no deployment secret, and the AI credential only as the variable a
command-line driver needs to call the model (the `ai` driver keeps it in process, and its checks
run inside the sandbox). `project.verify` is read from the default branch, never from the fork's
branch, and the branch is never pushed.

Profiles may use ordered `steps`, a `migrate` gate before the `switch` step, and multiple
`health` endpoints. A failed migration aborts before switching; a failed health check triggers
the allow-listed rollback. A non-reversible migration is reported as an image-only rollback.

## Docker sandbox

An agent manifest may declare a Docker sandbox using the protocol's one-level map (the services
value is JSON):

```yaml
sandbox:
  image: ghcr.io/acme/app-ci:latest
  services: '[{"name":"db","image":"pgvector/pgvector:pg16","env":{"POSTGRES_PASSWORD":"test"},"ready":["pg_isready","-h","localhost","-U","postgres"]}]'
```

`ready` runs inside the service container until it exits zero. Probe the service the way the
agent will reach it — over TCP, `pg_isready -h localhost` — because a probe on the Unix socket
answers while Postgres is still running `initdb`'s temporary server, and the agent then finds
`db:5432 - no response`.

The runner creates a private network, starts declared services, mounts only the worktree at
`/workspace`, and executes external drivers and checks through `docker exec`. CPU, memory, PID,
read-only-root, `cap-drop ALL` and `no-new-privileges` limits are fixed by the runner. The Docker
socket is never mounted into the agent container, and teardown removes containers and the network
on every path. A container that does not stay up is reported with its own output rather than
surfacing later as an unexplained `docker exec` failure.

Two properties are worth stating plainly, because they are what make the sandbox usable and
safe at the same time:

- **The agent is not root.** It runs as the user that owns the worktree. With every capability
  dropped there is no `CAP_DAC_OVERRIDE` to fall back on, so this is precisely what keeps the
  one writable path writable — and it makes the agent something less than root while it is
  there.
- **No secret is ever on a command line.** The run's environment, the project's credential
  included, enters the container once through a `0600` file at `docker run`. `docker exec`
  carries no `KEY=VALUE`, because an argument of `docker exec` is an argument of a process on
  the *host*, and `ps` shows it to every user on the machine.

## Packages

| Package | What it is |
|---|---|
| `runner` | the pipeline: `Runner.Run`, `Runner.Next` |
| `driver` | `exec`, `ai`, `claude-code`, `echo`; `driver.Register` for your own |
| `worktree` | git worktrees per task and run (own, variant, existing, detached), commit, diff stat, doctor, teardown |
| `queue` | `Local` (the graph) and `Remote` (trilha-cloud client) |
| `sandbox` | `None` and the local `Docker` sandbox with private services |
| `cmd/trilha-runner` | the CLI |

Depends on `trilha-spec` (the protocol) and on the `ai` and `ai/mcp` packages of the `trilha`
framework — nothing outside the Go standard library beyond those two modules.

## Testing

`make ci` runs what CI runs: gofmt, `go vet`, the whole suite and the race detector — with no
network and no key, using the `echo` driver and real git repositories in temporary
directories. How the suite is organized, and the user journeys each test proves, are in
[docs/testing.md](docs/testing.md) and [docs/journeys.md](docs/journeys.md).

Live cases run a real model and are opt-in, never in CI: `TRILHA_E2E_OLLAMA=1` drives the
`ai` driver against Ollama on `localhost:11434` (`TRILHA_E2E_OLLAMA_MODEL`, default
`qwen2.5:7b`), and `TRILHA_E2E_CLAUDE=1` drives `claude-code` with the local Claude Code login
(`TRILHA_E2E_CLAUDE_MODEL`, default `haiku`; it costs money). Both ask a question, answer it in a
continuation and check the result; the Ollama set also runs a read-only hunt on a planted
off-by-one and expects a valid finding. Each tries up to three times (`TRILHA_E2E_TRIES` lowers it):
`make e2e-ollama`.

For `claude-code` with an API key instead of a login, keep the key in the system keychain, not
in a shell profile or a `.env` file:

```bash
make claude-key          # asks for the key (hidden), checks it with the API, stores it
make e2e-claude          # runs the live Claude case with the key for that process alone
make claude-key-status   # where it is, masked, and whether the API still accepts it
make claude-key-forget   # removes it (revoke it in the Console too when you are done)
```

The key lives in the macOS Keychain, the Linux Secret Service (`secret-tool`), or else a 0600
file under `~/.config/trilha`. `make claude-key` also reads it from stdin, so a password manager
can feed it (`op read 'op://…' | make claude-key`). It never goes on a command line, is printed
only masked, reaches the agent the way a control plane's credential does, and the live case
fails if it shows up in any file the run leaves or anything sent to the control plane. Use a
key from a separate Console workspace with a low spend limit.

## License

MIT.
