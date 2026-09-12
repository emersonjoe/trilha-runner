---
name: coder
role: Implements a task inside its own worktree and produces evidence.
driver: exec
command: ""
tools:
  - read
  - write
  - run
constraints:
  - Stay inside the worktree of the task.
  - Do not touch tasks other than the one assigned.
  - Run the checks listed in the task before reporting.
---

# coder

Reads the context pack, implements the task, runs its checks and reports what
changed. `driver` and `command` are how the runner starts it; see the
trilha-runner documentation for the drivers available.
