# User journeys and test cases

> 🇺🇸 English · [🇧🇷 Português](journeys.pt-BR.md)

What people do with `trilha-runner`, what breaks for them if it fails, and the test that
proves each case. Ordered by risk. How the levels work is in [testing.md](testing.md).

Levels: **U** unit · **I** integration · **E** end to end (CLI binary).
A case marked **gap** has no automated test yet; the reason is beside it.

---

## J1 — Run a ready task locally · critical

*A maintainer runs `trilha-runner next` (or `run TASK-NNN`) and gets a branch with the agent's
work, checked, with evidence, and the task in `review`.*
Worst impact: work lands in the maintainer's checkout, or a task reaches `review` without its
checks having run.

**J1.1 Happy path to review** — E `TestRunEcho`, I `TestRunPipeline`
- Preconditions: git repository with one commit; `.trilha/` with one `ready` task whose check
  is `test -f TRILHA_RUN.md`.
- Data: task `TASK-001`, driver `echo`.
- Steps: `trilha-runner next --driver echo`.
- Expected: output `TASK-001 → review` and `branch trilha/task-001`; the file exists only in
  `.trilha/runs/TASK-001/wt`; three evidence records (two checks, one run with commit and
  driver); `next` again answers there is nothing to run; `run` again refuses a task that is
  not `ready`.

**J1.2 The maintainer's checkout never changes** — I `TestCreateCommitLeavesTheCheckoutAlone`,
`TestRunPipeline`
- Preconditions: repository on `main` at commit *H*.
- Steps: create the task's worktree, write a file, commit.
- Expected: the commit is on `trilha/task-001`, authored by `trilha-runner`; `main` is still
  at *H*; the file does not exist in the checkout.

**J1.3 A rerun reuses the branch** — I `TestCreateReusesWorktreeAndBranch`, `TestRunFailsChecks`
- Preconditions: a task that ran once and has a commit on its branch.
- Steps: run again; or `worktree clean`, then run again.
- Expected: the same worktree path; after a clean the branch keeps its commit and the new
  checkout starts from it.

**J1.4 Housekeeping** — E `TestRunEcho`, I `TestListOnlyCheckouts`, `TestRemoveDirectoryGitDoesNotKnow`
- Steps: `trilha-runner worktree list`, then `worktree clean TASK-001`.
- Expected: list shows `trilha/task-001` (and only directories that are checkouts); clean
  answers `removed`, keeps the branch, and also clears a run directory git does not know.

## J2 — A run that fails still leaves evidence · critical

*The agent crashes, the checks fail, or the provider is forbidden. The maintainer sees why.*
Worst impact: a failed run with no trace, or a failure reported as success.

**J2.1 The agent exits non-zero** — E `TestRunFailingAgentLeavesEvidence`, I `TestExecDriverFailureIsRecorded`
- Preconditions: a `ready` task.
- Data: `--driver exec --cmd 'sh -c "echo boom; exit 9"' --attempts 1 --json`.
- Expected: the CLI exits non-zero; JSON says `status: failed`, `failure_class: execution`;
  the task file says `failed`; one `run` record with stage `execution`; `agent.log` has
  `boom`; nothing outside `.trilha/` changed in the checkout.

**J2.2 The checks fail** — I `TestRunFailsChecks`
- Data: check `sh -c "exit 1"`.
- Expected: `failed`, `passed=false`.

**J2.3 An eval below its threshold fails the run even when the command exits zero** —
I `TestVerificationRecordsEvalAndFailsThreshold`
- Data: a check printing `{"metric":…,"value":…,"threshold":…,"comparator":">="}` under the
  threshold.
- Expected: an `eval` record that did not pass; the task does not reach `review`.

**J2.4 A forbidden provider stops before any call, with no retry** — I `TestPolicyFailureIsRecordedWithoutRetry`,
`TestAIRejectsForbiddenHostBeforeCallingProvider`
- Data: base URL outside the allowed hosts.
- Expected: one attempt, failure class `policy`, evidence without the credential.

**J2.5 No command for `exec`** — I `TestExecWithoutCommandIsRecorded`
- Preconditions: a `ready` task, no agent manifest, no `--cmd`.
- Expected: error `no command`; one attempt, no retry; task `failed`; one `run` record with
  stage `execution` and driver `exec`.

**J2.6 Not a git repository / unknown driver** — I `TestNotAGitRepositoryIsRecorded`,
`TestUnknownDriverIsRecorded`, U `TestPrintResultWithoutWorktree`
- Preconditions: a `ready` task in a `.trilha/` that is not a git repository; or a task whose
  agent manifest names `driver: nope`.
- Steps: `trilha-runner run TASK-001`.
- Expected: the CLI exits non-zero with the reason; the task goes `ready → running → failed`;
  one `run` record with stage `worktree` (no git) or `driver` (unknown driver) and the
  driver's name, its note naming the stage once; no worktree is created; the output has no
  `branch` line, since there is none.

**J2.7 A blocked task is not a failed run** — I `TestBlockedTaskIsNotFailed`
- Preconditions: a `ready` task depending on a task that is not `done`.
- Expected: an error naming the dependency; the task stays `ready`; no evidence.

## J3 — Secrets never leak · critical

*A project credential reaches the agent and nowhere else.*
Worst impact: a token visible in `ps`, in evidence, in a log or sent to the cloud.

**J3.1 The claude-code credential is redacted from output** — U `TestClaudeCodeUsesFixedArgvAndRedactsCredential`,
`TestClaudeCodeOAuthProviderSelectsTokenAndDropsBadModel`
**J3.2 No secret on a host command line in the sandbox** — I `TestSandboxedCommandCarriesNoCredential`,
`TestSecretNeverReachesACommandLine`, `TestEnvironmentValueWithANewlineIsRefused`
**J3.3 Deploy secrets are allow-listed and redacted** — I `TestExecuteUsesAllowListAndRedactsSecrets`
**J3.4 Only known credential variables are forwarded** — U `TestCredentialEnv`

## J4 — Cross-repository dependencies · high

*A task depends on `trilha:TASK-001` in a sibling repository.*
Worst impact: a task runs before what it depends on is done, or waits forever.

**J4.1 Unresolved, then resolved** — E `TestNextResolvesSiblingRepository`, I `TestLocalResolvesCrossRepositoryDependency`
- Steps: `next --driver echo` without `--repo`; then with `--repo trilha=<path>` where the
  dependency is `done`.
- Expected: first a non-zero exit naming `waiting:trilha:TASK-001`; then `TASK-001 → review`.

**J4.2 Rules** — U `TestParseRepository`, `TestBlocker`, `TestParseKeepsCrossRepositoryDependencies`,
`TestMoveWithCrossRepositoryDependencyKeepsIt`, `TestRemoteDependencyStatus`
- Expected: `--repo` must be `alias=path` with no `:` in the alias; a local dependency not
  `done` blocks; an alias pointing nowhere waits; moving the task keeps the remote dependency
  in the file.

## J5 — Worker and trilha-cloud · high

*A worker claims runs, executes them, and reports progress, result and evidence.*
Worst impact: runs stuck in the cloud, a claim over capacity, or a result the cloud cannot read.

**J5.1 The HTTP contract** — I `TestRemote`, U `TestExecutionV1FixtureMatchesRemoteResult`
- Expected: `Authorization: Bearer`; 204 is an empty queue; the result matches the
  `execution-v1` fixture of `trilha-spec`.

**J5.2 Capacity** — I `TestWorkerCapacityClaimsThirdOnlyAfterOneFinishes`
**J5.3 Progress is best effort** — I `TestWorkerReportsProgressBestEffort`
**J5.4 A worker that cannot start says so in the heartbeat** — I `TestWorkerReportsStartupFailureAsHeartbeat`

**J5.5 A bundle the worker cannot trust is refused** — I `TestRemoteBundle`
- Data: bundles for another task, another project, an unknown version, or not JSON; a 404.
- Expected: `invalid work bundle` for the first three; a 404 is `ErrNoBundle` (a run from
  before Cloud-managed specs, executed as is); a 5xx names the status.

**J5.6 Progress reaches its route** — I `TestRemoteProgress`
- Expected: `POST /api/runs/{id}/progress` with the event as sent; a 502 is an error the
  worker ignores (J5.3).

**J5.7 Cross-project dependencies of a bundle** — I `TestRemoteDependencyBlocker`
- Expected: only `alias:TASK-NNN` dependencies are asked about, with `requesting_project`;
  the first one not `done` — or unknown, or the cloud being down — answers
  `waiting:alias:TASK-NNN` and stops asking.

**J5.8 Deployments: claim and report** — I `TestRemoteNextDeployment`, `TestRemoteDoneDeploymentSendsNoSecret`
- Expected: the claim carries worker and project; 204 is an empty queue; work for another
  project, or without id or profile, is refused; the report always carries this worker and
  project (a value set by the caller is overwritten) and never the claim's secrets.

## J6 — Work created in the UI (bundles) · high

*The worker downloads a bundle, writes its spec and tasks under `.trilha/` and publishes the branch.*

Worst impact: the maintainer's own spec or task overwritten, or a round that stops
publishing after its first task.

**J6.1 Clone and materialize** — I `TestEnsureRepositoryAndMaterializeBundle`

**J6.2 The checkout is the runner's, and only for its remote** — I `TestEnsureRepositoryReusesAndGuardsTheCheckout`
- Expected: leftovers (a dirty file, another branch) are reset to the remote default branch;
  the same project name pointing at another remote is refused; project names that escape
  the workspace, and remotes or branches with newlines or revision syntax, are refused.

**J6.3 Divergent files are refused, nothing is pushed** — I `TestMaterializeRefusesDivergentFiles`
- Data: on the remote's main, a spec with another `cloud_id`; a hand-written `TASK-001`; a
  `TASK-001` of another Cloud specification.
- Expected: the bundle is refused with the reason; the remote has no new branch.

**J6.4 The same bundle again is accepted** — I `TestMaterializeSameBundleTwice`

**J6.5 Every task of a round publishes** — I `TestMaterializeSecondTaskOfTheRoundPushes`,
`TestPushBranch`, `TestCheckoutFromBeforeTrackingRecovers`
- Preconditions: a worker with `--push`; a round with two tasks.
- Steps: materialize and push for TASK-001; reset the checkout; materialize and push for
  TASK-002. Separately: push the task branch, rewrite it, push again; then someone else
  pushes to it, and the runner pushes once more.
- Expected: the second push of the same branch succeeds; a branch someone else moved is
  not overwritten; a checkout cloned by an earlier version adopts the branches already published
  the first time it is reused, and the refspec is added once.

Open question: a spec with the same id and **no** `cloud_id` (written by hand) is replaced on
the spec branch, while a hand-written task is refused. The asymmetry is not tested either
way until it is decided.

## J7 — Docker sandbox · high

*The agent and checks run in a container with the project's services.*
Worst impact: the agent escapes the worktree, runs as root, or containers are left behind.

**J7.1 Services, readiness and teardown** — I `TestDockerPostgresAndTeardown` (skips without Docker)
**J7.2 Least privilege** — I `TestAgentDropsCapabilitiesAndRunsAsTheWorktreeOwner`
**J7.3 A container that dies is reported with its own output** — I `TestContainerThatDidNotStayUpIsReported`,
`TestServiceThatExitsIsCaughtBeforeReadiness`
**J7.4 Manifest parsing and no sandbox** — U `TestFromAgent`, `TestNoneRunsInPlace`

## J8 — Deploy and rollback · high

*A delivery profile deploys, migrates, health-checks, and rolls back.*
Worst impact: a switch after a failed migration, or no rollback after a failed health check.

**J8.1 Migration fails → abort before switch** — I `TestMigrationFailureAbortsBeforeSwitch`
**J8.2 Health fails → image-only rollback** — I `TestHealthFailureRollsBackImageOnly`
**J8.3 Unknown profile refused** — I `TestExecuteRejectsUnknownProfile`
**J8.4 The profile file says exactly what the operator meant** — U `TestLoadShippedExample`, `TestLoadRefuses`
- Expected: `deploy/eoslab/delivery.json.example` loads as shipped; a misspelled field, a
  file that is not JSON, or one with no profiles is refused at startup.

## J9 — Transient provider failures · medium

*The model provider answers 429/503 or the connection resets; the run retries and continues.*

**J9.1 Classification and backoff** — U `TestClassifyRetry`, `TestRetryDelay`, `TestWaitRetryStopsOnCancel`
- Expected: timeouts, resets, 429 and 5xx retry; 401/403/quota/permission do not;
  `Retry-After` up to 300 s is honoured; backoff doubles up to 16× the base.

**J9.2 The checkpoint carries what the next attempt needs** — U `TestCheckpointPreservesRecoveryStateAndDiff`

**J9.3 The run retries and continues** — I `TestTransientProviderFailureIsRetried`,
`TestPermanentProviderFailureIsNotRetried`, `TestTransientFailuresExhaustAttempts`,
`TestFailedChecksGetACorrectiveAttempt`
- Data: a scripted driver answering `HTTP 503` once, then working; `HTTP 401`; `connection
  reset` on every attempt; checks that fail on the first attempt only.
- Expected: 503 → a `retry_wait` event, a second attempt that receives attempt 2 and a
  checkpoint naming the 503, review with attempts `recovered`/`passed` and evidence for
  both; 401 → one attempt, `provider_permanent`; resets → as many attempts as allowed, then
  `failed`; failed checks → a corrective attempt that reaches review.

## J10 — Drivers · medium

**J10.1 `echo`, `exec`, tools fenced to the worktree** — U/I `TestEcho`, `TestExec`, `TestToolsStayInside`
**J10.2 `drivers` lists them** — E `TestRunEcho`

## J11 — Release · low

**J11.1 The binary reports the version that shipped** — U `TestVersionMatchesTheNewestChangelogEntry`
