# User journeys and test cases

> 🇺🇸 English · [🇧🇷 Português](journeys.pt-BR.md)

What people do with `trilha-runner`, what breaks for them if it fails, and the test that
proves each case. Ordered by risk. How the levels work is in [testing.md](testing.md).

Levels: **U** unit · **I** integration · **E** end to end (CLI binary).
Every case says which **resources** it needs (`git`, `sh`, `httptest`, `Docker`, the compiled
binary, or none), its **preconditions**, **data**, **steps** and the **expected** result.
`internal/catalog` fails the suite when a case is incomplete, names a test that does not
exist, or when a test belongs to no case — and when the two locales disagree.

---

## J1 — Run a ready task locally · critical

*A maintainer runs `trilha-runner next` (or `run TASK-NNN`) and gets a branch with the agent's
work, checked, with evidence, and the task in `review`.*
Worst impact: work lands in the maintainer's checkout, or a task reaches `review` without its
checks having run.

**J1.1 Happy path to review** — E `TestRunEcho`, I `TestRunPipeline`, U `TestLocal`
- Resources: git, sh, compiled binary.
- Preconditions: git repository with one commit; `.trilha/` with one `ready` task whose check
  is `test -f TRILHA_RUN.md`.
- Data: task `TASK-001`, driver `echo`.
- Steps: `trilha-runner next --driver echo`.
- Expected: output `TASK-001 → review` and `branch trilha/task-001`; the file exists only in
  `.trilha/runs/TASK-001/wt`; three evidence records (two checks, one run with commit and
  driver); `next` again answers there is nothing to run; `run` again refuses a task that is
  not `ready`; with no `ready` task the queue is empty.

**J1.2 The maintainer's checkout never changes** — I `TestCreateCommitLeavesTheCheckoutAlone`,
`TestRunPipeline`, U `TestBranchAndPath`
- Resources: git.
- Preconditions: repository on `main` at commit *H*.
- Data: task `TASK-001`; a file `work.txt`.
- Steps: create the task's worktree, write the file, commit.
- Expected: the worktree is `.trilha/runs/TASK-001/wt` on `trilha/task-001`; the commit is
  authored by `trilha-runner`; `main` is still at *H*; the file does not exist in the
  checkout; with nothing changed, no commit is made.

**J1.3 A rerun reuses the branch** — I `TestCreateReusesWorktreeAndBranch`, `TestRunFailsChecks`
- Resources: git, sh.
- Preconditions: a task that ran once and has a commit on its branch.
- Data: the same `TASK-001`.
- Steps: run again; or `worktree clean`, then run again.
- Expected: the same worktree path; after a clean the branch keeps its commit and the new
  checkout starts from it.

**J1.4 Housekeeping** — E `TestRunEcho`, I `TestListOnlyCheckouts`, `TestRemoveDirectoryGitDoesNotKnow`
- Resources: git, compiled binary.
- Preconditions: a task that ran; a run directory with evidence only; a stray directory git
  does not know.
- Data: `TASK-001`, `TASK-002`, `TASK-003`.
- Steps: `trilha-runner worktree list`, then `worktree clean TASK-001`.
- Expected: list shows `trilha/task-001` (and only directories that are checkouts); clean
  answers `removed`, keeps the branch, and also clears a run directory git does not know.

## J2 — A run that fails still leaves evidence · critical

*The agent crashes, the checks fail, or the provider is forbidden. The maintainer sees why.*
Worst impact: a failed run with no trace, or a failure reported as success.

**J2.1 The agent exits non-zero** — E `TestRunFailingAgentLeavesEvidence`, I `TestExecDriverFailureIsRecorded`
- Resources: git, sh, compiled binary.
- Preconditions: a `ready` task.
- Data: `--driver exec --cmd 'sh -c "echo boom; exit 9"' --attempts 1 --json`.
- Steps: `trilha-runner run TASK-001` with the data above.
- Expected: the CLI exits non-zero; JSON says `status: failed`, `failure_class: execution`;
  the task file says `failed`; one `run` record with stage `execution`; `agent.log` has
  `boom`; nothing outside `.trilha/` changed in the checkout.

**J2.2 The checks fail** — I `TestRunFailsChecks`
- Resources: git, sh.
- Preconditions: a `ready` task; driver `echo`.
- Data: check `sh -c "exit 1"`.
- Steps: run the task.
- Expected: after the allowed attempts, `failed`, `passed=false`.

**J2.3 An eval below its threshold fails the run even when the command exits zero** —
I `TestVerificationRecordsEvalAndFailsThreshold`
- Resources: git, sh.
- Preconditions: a `ready` task whose check prints a metric line and exits zero.
- Data: `{"metric":…,"value":…,"threshold":…,"comparator":">="}` under the threshold.
- Steps: run the task.
- Expected: an `eval` record that did not pass; the task does not reach `review`.

**J2.4 A forbidden provider stops before any call, with no retry** — I `TestPolicyFailureIsRecordedWithoutRetry`,
U `TestAIRejectsForbiddenHostBeforeCallingProvider`
- Resources: git.
- Preconditions: a `ready` task; driver `ai` with a provider policy.
- Data: base URL `https://outside.example/v1`, allowed host `gateway.example.br`, credential
  `secret`.
- Steps: run the task.
- Expected: one attempt, failure class `policy`, no request to the provider, evidence without
  the credential.

**J2.5 No command for `exec`** — I `TestExecWithoutCommandIsRecorded`
- Resources: git.
- Preconditions: a `ready` task, no agent manifest, no `--cmd`.
- Data: none.
- Steps: run the task.
- Expected: error `no command`; one attempt, no retry; task `failed`; one `run` record with
  stage `execution` and driver `exec`.

**J2.6 Not a git repository / unknown driver** — I `TestNotAGitRepositoryIsRecorded`,
`TestUnknownDriverIsRecorded`, `TestIsRepo`, U `TestPrintResultWithoutWorktree`
- Resources: git (for the second case).
- Preconditions: a `ready` task in a `.trilha/` that is not a git repository (or a repository
  without a commit); or a task whose agent manifest names `driver: nope`.
- Data: `TASK-001`.
- Steps: `trilha-runner run TASK-001`.
- Expected: the CLI exits non-zero with the reason; the task goes `ready → running → failed`;
  one `run` record with stage `worktree` (no git) or `driver` (unknown driver) and the
  driver's name, its note naming the stage once; no worktree is created; the output has no
  `branch` line, since there is none.

**J2.7 A blocked task is not a failed run** — I `TestBlockedTaskIsNotFailed`
- Resources: git.
- Preconditions: a `ready` task depending on a task that is not `done`.
- Data: `TASK-002` depends on `TASK-001`.
- Steps: run `TASK-002`.
- Expected: an error naming the dependency; the task stays `ready`; no evidence.

## J3 — Secrets never leak · critical

*A project credential reaches the agent and nowhere else.*
Worst impact: a token visible in `ps`, in evidence, in a log or sent to the cloud.

**J3.1 The claude-code credential is redacted and routed** — U `TestClaudeCodeUsesFixedArgvAndRedactsCredential`,
`TestClaudeCodeOAuthProviderSelectsTokenAndDropsBadModel`
- Resources: sh (a fake `claude` on `PATH`).
- Preconditions: a claim whose provider is `claude-code` or `claude-code-oauth`.
- Data: a made-up credential; a model id that is not well-formed.
- Steps: execute the preset driver; the fake `claude` echoes its argv and environment.
- Expected: argv is the fixed `claude -p - --permission-mode acceptEdits`; an API key goes
  to `ANTHROPIC_API_KEY`, an OAuth token to `CLAUDE_CODE_OAUTH_TOKEN`; the malformed model
  never reaches argv; the credential is `[REDACTED]` in the captured output.

**J3.2 No secret on a host command line in the sandbox** — I `TestSandboxedCommandCarriesNoCredential`,
`TestSecretNeverReachesACommandLine`, `TestEnvironmentValueWithANewlineIsRefused`
- Resources: sh, fake docker binary.
- Preconditions: a sandboxed run; a fake `docker` that logs every invocation.
- Data: a credential in the run environment; a value with a newline.
- Steps: prepare the sandbox and execute the agent through it.
- Expected: no `docker` call carries the credential or any `KEY=VALUE`; the environment
  enters once through a `0600` `--env-file` that is removed at teardown; a value with a
  newline is refused.

**J3.3 Deploy secrets are allow-listed and redacted** — I `TestExecuteUsesAllowListAndRedactsSecrets`
- Resources: sh.
- Preconditions: a profile whose deploy script prints the revision and `DATABASE_URL`.
- Data: secret `DATABASE_URL=private-value`, revision `abc123`.
- Steps: execute the deployment.
- Expected: it passes; the log has `[REDACTED]` and never `private-value`.

**J3.4 Only known credential variables are forwarded** — U `TestCredentialEnv`
- Resources: none.
- Preconditions: none.
- Data: no config; config without credential; an API key; an OAuth token.
- Steps: ask for the credential environment.
- Expected: nothing, nothing, `ANTHROPIC_API_KEY=…`, `CLAUDE_CODE_OAUTH_TOKEN=…`.

## J4 — Cross-repository dependencies · high

*A task depends on `trilha:TASK-001` in a sibling repository.*
Worst impact: a task runs before what it depends on is done, or waits forever.

**J4.1 Unresolved, then resolved** — E `TestNextResolvesSiblingRepository`, I `TestLocalResolvesCrossRepositoryDependency`
- Resources: git, sh, compiled binary.
- Preconditions: a sibling `.trilha/` where `TASK-001` is `done`; the primary task depends on
  `trilha:TASK-001`.
- Data: `--repo trilha=<path>`.
- Steps: `next --driver echo` without `--repo`; then with it.
- Expected: first a non-zero exit naming `waiting:trilha:TASK-001`; then `TASK-001 → review`.

**J4.2 Rules** — U `TestParseRepository`, `TestBlocker`, `TestParseKeepsCrossRepositoryDependencies`,
`TestParseLocalOnlyIsTheProtocolParse`, `TestGetAndList`, `TestMoveWithCrossRepositoryDependencyKeepsIt`,
`TestRemoteDependencyStatus`
- Resources: none (task files in a temporary directory).
- Preconditions: tasks with local and `alias:` dependencies.
- Data: `--repo` values; task files whose name and id disagree.
- Steps: parse flags and tasks, ask for blockers, move a task.
- Expected: `--repo` must be `alias=path` with no `:` in the alias; a local dependency not
  `done` blocks; an alias pointing nowhere waits; moving the task keeps the remote dependency
  in the file; a file whose name disagrees with its id is refused.

## J5 — Worker and trilha-cloud · high

*A worker claims runs, executes them, and reports progress, result and evidence.*
Worst impact: runs stuck in the cloud, a claim over capacity, or a result the cloud cannot read.

**J5.1 The HTTP contract** — I `TestRemote`, U `TestExecutionV1FixtureMatchesRemoteResult`
- Resources: httptest.
- Preconditions: a fake control plane.
- Data: a claim, a result, a heartbeat; the `execution-v1` fixture.
- Steps: claim, report, heartbeat.
- Expected: `Authorization: Bearer`; 204 is an empty queue; the result matches the
  `execution-v1` fixture of `trilha-spec`.

**J5.2 Capacity** — I `TestWorkerCapacityClaimsThirdOnlyAfterOneFinishes`
- Resources: httptest.
- Preconditions: a worker with `--capacity 2`; a control plane with three runs.
- Data: runs whose dependencies are waiting (they finish immediately).
- Steps: run the worker loop.
- Expected: claims report `running` 0, then 1; the third claim happens only after a result
  was posted; three results in all.

**J5.3 Progress is best effort** — I `TestWorkerReportsProgressBestEffort`
- Resources: git, sh, httptest.
- Preconditions: a project with a `ready` task; a control plane whose progress route answers 500.
- Data: one claimed run without a bundle.
- Steps: run the worker.
- Expected: `POST …/progress` with `consulting_ai` and attempt 1 is sent; the run completes
  and `POST …/result` is still sent.

**J5.4 A worker that cannot start says so in the heartbeat** — I `TestWorkerReportsStartupFailureAsHeartbeat`
- Resources: httptest.
- Preconditions: a directory with no `.trilha/`.
- Data: `trilha-runner worker` against a fake control plane.
- Steps: start the worker.
- Expected: a heartbeat with status `failed` and an error naming `no .trilha/`; the command
  returns an error.

**J5.5 A bundle the worker cannot trust is refused** — I `TestRemoteBundle`
- Resources: httptest.
- Preconditions: a claimed run.
- Data: bundles for another task, another project, an unknown version, or not JSON; a 404.
- Steps: ask for the run's bundle.
- Expected: `invalid work bundle` for the first three; a 404 is `ErrNoBundle` (a run from
  before Cloud-managed specs, executed as is); a 5xx names the status.

**J5.6 Progress reaches its route** — I `TestRemoteProgress`
- Resources: httptest.
- Preconditions: a claimed run.
- Data: an event with phase, attempt, branch and model; a 502.
- Steps: report progress.
- Expected: `POST /api/runs/{id}/progress` with the event as sent; a 502 is an error the
  worker ignores (J5.3).

**J5.7 Cross-project dependencies of a bundle** — I `TestRemoteDependencyBlocker`
- Resources: httptest.
- Preconditions: a control plane knowing `trilha:TASK-001` (`done`) and `trilha:TASK-002` (`running`).
- Data: dependency lists mixing local and remote ids; the control plane going down.
- Steps: ask whether the bundle's task may run.
- Expected: only `alias:TASK-NNN` dependencies are asked about, with `requesting_project`;
  the first one not `done` — or unknown, or the cloud being down — answers
  `waiting:alias:TASK-NNN` and stops asking.

**J5.8 Deployments: claim and report** — I `TestRemoteNextDeployment`, `TestRemoteDoneDeploymentSendsNoSecret`
- Resources: httptest.
- Preconditions: a control plane with deployment work.
- Data: work for this project, for another, without id or profile; a secret in the claim.
- Steps: claim, then report a result.
- Expected: the claim carries worker and project; 204 is an empty queue; work for another
  project, or without id or profile, is refused; the report always carries this worker and
  project (a value set by the caller is overwritten) and never the claim's secrets.

## J6 — Work created in the UI (bundles) · high

*The worker downloads a bundle, writes its spec and tasks under `.trilha/` and publishes the branch.*
Worst impact: the maintainer's own spec or task overwritten, or a round that stops
publishing after its first task.

**J6.1 Clone and materialize** — I `TestEnsureRepositoryAndMaterializeBundle`
- Resources: git (a bare repository as the remote).
- Preconditions: a remote with one commit on `main`; an empty workspace root.
- Data: a bundle for spec `001-user-access`, round 1, `TASK-001`.
- Steps: ensure the checkout, materialize with push.
- Expected: branch `trilha/spec-spec-1-round-001` with `project.md`, the spec and the task;
  the remote has the same commit on that branch.

**J6.2 The checkout is the runner's, and only for its remote** — I `TestEnsureRepositoryReusesAndGuardsTheCheckout`
- Resources: git.
- Preconditions: a checkout from an earlier claim, left dirty on another branch.
- Data: the same project name with another remote; names with `../`, capitals, newlines,
  `~1`, spaces.
- Steps: ensure the checkout again, then with each bad input.
- Expected: leftovers are reset to the remote default branch; the same project name pointing
  at another remote is refused; project names that escape the workspace, and remotes or
  branches with newlines or revision syntax, are refused.

**J6.3 Divergent files are refused, nothing is pushed** — I `TestMaterializeRefusesDivergentFiles`
- Resources: git.
- Preconditions: on the remote's main, a spec with another `cloud_id`; a hand-written
  `TASK-001`; or a `TASK-001` of another Cloud specification.
- Data: the sample bundle.
- Steps: materialize with push.
- Expected: the bundle is refused with the reason; the remote has no new branch.

**J6.4 The same bundle again is accepted** — I `TestMaterializeSameBundleTwice`
- Resources: git.
- Preconditions: the bundle was materialized and its files merged into the remote's main.
- Data: the same bundle.
- Steps: materialize again.
- Expected: no error — the files carry the same `cloud_id`.

**J6.5 Every task of a round publishes** — I `TestMaterializeSecondTaskOfTheRoundPushes`,
`TestPushBranch`, `TestCheckoutFromBeforeTrackingRecovers`
- Resources: git.
- Preconditions: a worker with `--push`; a round with two tasks.
- Data: `TASK-001` and `TASK-002`; a second clone standing for someone else.
- Steps: materialize and push for TASK-001; reset the checkout; materialize and push for
  TASK-002. Separately: push the task branch, rewrite it, push again; then someone else
  pushes to it, and the runner pushes once more.
- Expected: the second push of the same branch succeeds; a branch someone else moved is
  not overwritten; a checkout cloned by an earlier version adopts the branches already
  published the first time it is reused, and the refspec is added once.

Open question: a spec with the same id and **no** `cloud_id` (written by hand) is replaced on
the spec branch, while a hand-written task is refused. The asymmetry is not tested either
way until it is decided.

## J7 — Docker sandbox · high

*The agent and checks run in a container with the project's services.*
Worst impact: the agent escapes the worktree, runs as root, or containers are left behind.

**J7.1 Services, readiness and teardown** — I `TestDockerPostgresAndTeardown`
- Resources: Docker (required in CI through `TRILHA_REQUIRE_DOCKER=1`; skipped elsewhere
  without it, saying so).
- Preconditions: a reachable Docker daemon.
- Data: image `postgres:16-alpine` with a `db` service and `pg_isready -h localhost` readiness
  (over TCP, the way the agent reaches it).
- Steps: prepare the sandbox, run commands through its prefix, release it.
- Expected: `pg_isready` answers; the agent runs as the host user; only the worktree is
  writable; no container or network remains after release.

**J7.2 Least privilege** — I `TestAgentDropsCapabilitiesAndRunsAsTheWorktreeOwner`
- Resources: sh, fake docker binary.
- Preconditions: a fake `docker` that logs the `docker run` line.
- Data: a worktree owned by the current user.
- Steps: prepare the sandbox.
- Expected: `--cap-drop ALL`, `--user` the worktree's owner, read-only root and
  `no-new-privileges`; the Docker socket is never mounted.

**J7.3 A container that dies is reported with its own output** — I `TestContainerThatDidNotStayUpIsReported`,
`TestServiceThatExitsIsCaughtBeforeReadiness`
- Resources: sh, fake docker binary.
- Preconditions: a fake `docker` whose agent container or service exits at once.
- Data: the container's own output.
- Steps: prepare the sandbox.
- Expected: an error carrying that output; readiness is never awaited on a dead service; the
  container is still removed.

**J7.4 Manifest parsing and no sandbox** — U `TestFromAgent`, `TestNoneRunsInPlace`
- Resources: none.
- Preconditions: an agent manifest with `sandbox:`; or no sandbox.
- Data: image and services JSON.
- Steps: build the sandbox from the manifest; prepare `None`.
- Expected: the image and the `db` service are read as written; `None` runs in place and
  hands the environment back unchanged.

## J8 — Deploy and rollback · high

*A delivery profile deploys, migrates, health-checks, and rolls back.*
Worst impact: a switch after a failed migration, or no rollback after a failed health check.

**J8.1 Migration fails → abort before switch** — I `TestMigrationFailureAbortsBeforeSwitch`
- Resources: sh.
- Preconditions: a profile with steps `pull`, `switch` and a `migrate` gate.
- Data: a migration that exits 7.
- Steps: execute a deploy.
- Expected: order `pull`, `migrate` — no `switch`; error `switch aborted`.

**J8.2 Health fails → image-only rollback** — I `TestHealthFailureRollsBackImageOnly`
- Resources: sh, httptest.
- Preconditions: a profile with a non-reversible migration, a rollback and a health check.
- Data: a health endpoint answering 503.
- Steps: execute a deploy.
- Expected: order `pull`, `migrate`, `switch`, `rollback`; no downgrade; the log says
  `image-only rollback`.

**J8.3 Unknown profile refused** — I `TestExecuteRejectsUnknownProfile`
- Resources: none.
- Preconditions: a config without the profile the claim names.
- Data: profile `missing`.
- Steps: execute.
- Expected: not passed, with an error; nothing runs.

**J8.4 The profile file says exactly what the operator meant** — U `TestLoadShippedExample`, `TestLoadRefuses`
- Resources: none.
- Preconditions: the shipped `deploy/eoslab/delivery.json.example`; files in a temporary directory.
- Data: a misspelled field (`rolback`), a non-JSON file, no profiles, a missing file.
- Steps: load each.
- Expected: the example loads as shipped; each of the others is refused at startup.

## J9 — Transient provider failures · medium

*The model provider answers 429/503 or the connection resets; the run retries and continues.*
Worst impact: a run abandoned for a hiccup, or hammering a provider that refused the credential.

**J9.1 Classification and backoff** — U `TestClassifyRetry`, `TestRetryDelay`, `TestWaitRetryStopsOnCancel`
- Resources: none.
- Preconditions: none.
- Data: timeouts, resets, 429, 5xx, 401, 403, quota, permission; `Retry-After` values.
- Steps: classify each error; compute delays.
- Expected: timeouts, resets, 429 and 5xx retry; 401/403/quota/permission do not;
  `Retry-After` up to 300 s is honoured; backoff doubles up to 16× the base; a cancelled
  wait stops at once.

**J9.2 The checkpoint carries what the next attempt needs** — U `TestCheckpointPreservesRecoveryStateAndDiff`
- Resources: none.
- Preconditions: a failed attempt with recovery state.
- Data: turns, files analysed, tool calls, the diff stat.
- Steps: save the checkpoint and read it back.
- Expected: attempt number, diff and recovery state are kept.

**J9.3 The run retries and continues** — I `TestTransientProviderFailureIsRetried`,
`TestPermanentProviderFailureIsNotRetried`, `TestTransientFailuresExhaustAttempts`,
`TestFailedChecksGetACorrectiveAttempt`
- Resources: git.
- Preconditions: a `ready` task whose check needs `done.txt`; a 1 ms retry base.
- Data: a scripted driver answering `HTTP 503` once, then working; `HTTP 401`; `connection
  reset` on every attempt; checks that fail on the first attempt only.
- Steps: run the task.
- Expected: 503 → a `retry_wait` event, a second attempt that receives attempt 2 and a
  checkpoint naming the 503, review with attempts `recovered`/`passed` and evidence for
  both; 401 → one attempt, `provider_permanent`; resets → as many attempts as allowed, then
  `failed`; failed checks → a corrective attempt that reaches review.

## J10 — Drivers · medium

*The manifest names a driver; the runner starts the agent through it.*
Worst impact: an agent that writes or runs outside its worktree.

**J10.1 `echo`, `exec`, tools fenced to the worktree** — U `TestEcho`, I `TestExec`, `TestToolsStayInside`
- Resources: sh.
- Preconditions: a worktree directory.
- Data: a prompt; a path `../escape`; a command outside the task's checks.
- Steps: run `echo`; run `exec` with the prompt on stdin; call the `ai` tools.
- Expected: `echo` writes the prompt's first line and an unknown driver is refused; `exec`
  receives the prompt and `TRILHA_TASK`; file tools refuse paths outside the worktree;
  `run` refuses commands that are not allow-listed.

**J10.2 `drivers` lists them** — E `TestRunEcho`
- Resources: compiled binary.
- Preconditions: none.
- Data: none.
- Steps: `trilha-runner drivers`.
- Expected: `ai`, `claude-code`, `echo`, `exec`, one per line.

## J11 — Release · low

**J11.1 The binary reports the version that shipped** — U `TestVersionMatchesTheNewestChangelogEntry`
- Resources: none.
- Preconditions: `CHANGELOG.md` with a released section.
- Data: the `version` constant.
- Steps: compare them.
- Expected: equal; an `Unreleased` section does not count.

## J12 — The suite keeps itself honest · medium

*Whoever changes the runner trusts that the catalog, the fixtures and the numbers are true.*
Worst impact: a catalog that promises tests that are gone, real data in a fixture, or
coverage that erodes unnoticed.

**J12.1 The catalog and the tests agree** — U `TestCatalogReferencesExistingTests`,
`TestEveryTestBelongsToACase`, `TestCatalogLocalesAgree`, `TestEveryCaseIsComplete`,
`TestParseReadsCasesTestsAndFields`
- Resources: none (reads the repository).
- Preconditions: both catalogs and every `*_test.go`.
- Data: the case ids, the test names they cite, their field lines.
- Steps: run `go test ./internal/catalog`.
- Expected: it fails when a cited test does not exist, a test belongs to no case, the
  locales differ in cases or tests, or a case lacks resources, preconditions or expected.

**J12.2 Fixtures are synthetic** — U `TestFixturesAreSynthetic`
- Resources: none (reads the repository).
- Preconditions: tests, `testdata/` and `deploy/*.example`.
- Data: e-mail addresses, credential-shaped strings, URL hosts in tests.
- Steps: run `go test ./internal/catalog`.
- Expected: it fails on an address outside `t@t`/`example`/`trilha.local`, on anything shaped
  like a real token (`sk-`, `ghp_`, `github_pat_`, `xox?-`, `AKIA`), and on a test calling a
  host that is not an example, loopback or `.local` name.

**J12.3 The numbers are reported, and coverage does not fall** — U `TestReportSummarizesARun`,
`TestReportFailsBelowTheFloor`, `TestCoverageByPackage`
- Resources: none.
- Preconditions: a `go test -json` stream and a coverage profile.
- Data: passes, a failure, a skip with its reason, slow tests; a floor above and below.
- Steps: run `internal/testreport` (`make report`).
- Expected: markdown with passed/failed/skipped, the reasons for skips, the slowest tests
  and coverage per package; a non-zero exit when a test failed or the total is below
  `testdata/coverage-floor.txt`; a hint to raise the floor when it is a point or more above.
