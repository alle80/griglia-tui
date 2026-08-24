# Isolated multi-agent workspaces — design proposal

Status: **design, not implemented**. Target: v0.2. This document proposes how
Griglia models and manages isolated per-task workspaces (Git worktrees) so
several coding agents can work concurrently in one repository without sharing
a working tree. It is grounded in two real concurrency experiments (Claude +
Codex) and in the current architecture described by `DESIGN.md`,
[PROTOCOL.md](PROTOCOL.md), and [AGENT_INTEGRATION.md](AGENT_INTEGRATION.md).

## 1. Motivation

Round 1 of the experiments ran two agents in the same checkout. Griglia's
coordination layer held up — exclusive claims, identities, progress,
questions, completion, TUI auto-refresh all worked — but Git did not: both
agents saw each other's branch switches and untracked files, so safe parallel
Git work was impossible.

Round 2 gave each agent its own `git worktree` beside the main repository:

```text
main repository / authoritative Griglia board
    ├── Claude worktree
    └── Codex worktree
```

This allowed genuinely concurrent, independent implementation work. It also
surfaced the sandbox requirements: an isolated agent needs writable access to
its own worktree, to the shared Git metadata that worktree depends on, and to
the authoritative `.griglia` database — and to nothing else.

Today that setup is manual. This design makes Griglia able to model, create,
list, and clean up such workspaces, without turning Griglia into an agent
launcher.

## 2. Goals and non-goals

Goals (v0.2 candidate):

- a persisted **workspace model** tied to the existing task/claim model;
- race-safe **allocation** of one Git worktree + branch per task;
- deterministic directory and branch naming;
- **project-root discovery** that works unchanged from inside a worktree;
- a **CLI/read model** (`griglia workspace …`) with JSON protocol support;
- **TUI visibility** without cluttering the task list;
- documented **failure/recovery** behavior and **sandbox permission**
  derivation for external launchers.

Non-goals (explicitly deferred):

- launching or supervising Claude/Codex/Gemini processes;
- remote agents, remote filesystems, or multi-host coordination;
- automatic PR creation or merging, CI orchestration;
- non-Git workspace backends (directory copies, containers);
- fetching, pulling, pushing, or any network operation — Griglia stays
  offline; publishing branches remains the agent's job.

## 3. Invariants

These extend, and never weaken, the existing invariants:

1. **Claims stay authoritative for ownership.** A workspace never grants or
   implies a claim; existing claim/eligibility semantics are unchanged.
2. **At most one live workspace per task.** Enforced by a partial unique
   index, exactly like `one_active_claim_per_task`.
3. **Workspace paths and branches are unique** across live workspaces —
   two allocations can never receive the same directory or branch.
4. **One authoritative database.** A worktree never contains its own
   `.griglia`; every workspace operates against the main project's database.
5. **Griglia only touches what it created.** It never adopts, reuses, or
   deletes a branch or directory it has no record of; conflicts with foreign
   branches/paths are typed errors, not silent reuse.
6. **Every mutation is one SQLite transaction plus an audit event.** Git side
   effects happen outside the transaction; the database records intent first
   and outcome after (two-phase allocation, §7).
7. **Workspace support is optional.** Projects without Git, and agents that
   ignore workspaces entirely, keep working exactly as today.

## 4. Workspace identity

**A workspace is keyed by task, stamped with the allocating claim.**

- The row records `task_id` plus the `agent_name`/`instance_id` of the claim
  that allocated it. While the task is claimed, ownership checks for mutating
  workspace commands reuse the existing claim-owner check (`conflict` for
  everyone else, consistent with `progress`/`release`).
- **One agent instance may own multiple workspaces** — one per task it has
  claimed. Nothing associates a workspace with an instance directly; the
  association always goes through the task.
- **A released or completed task keeps its workspace** (state `retained`,
  §7). Work-in-progress commits and the branch under review are valuable; a
  later claim of the same task — by the same or a different identity —
  *adopts* the retained workspace instead of allocating a new one. Adoption
  updates the recorded identity and appends an event, so history shows the
  hand-over.

Why not key by agent instance (the round-2 layout)? A long-lived per-agent
worktree must switch branches between tasks, which breaks the natural
workspace ↔ branch ↔ PR mapping: the moment the agent picks up task B, the
worktree no longer matches task A's PR under review. Per-task workspaces keep
review/rework trivial (the branch and directory are still there) and make
cleanup decisions local to one task. Why not key by claim id? Claims are
closed rows; tying reuse to a dead claim id complicates adoption for no
benefit. Task identity is stable across the whole review/rework cycle.

## 5. Git strategy

### 5.1 Location: inside `.griglia/`

Worktrees live under the main project:

```text
<project>/.griglia/worktrees/task-<id>/
```

Rationale:

- **Discovery works with zero changes.** Walking upward from
  `.griglia/worktrees/task-7/src/` finds `<project>/.griglia/griglia.db`
  exactly like it does from the main checkout (§6).
- **Invisible to tooling in the main checkout.** `.griglia/` is already
  gitignored, so git-aware search tools skip it; Go tooling ignores
  dot-directories, so `go build ./...`/`go test ./...` at the project root
  never descend into other agents' worktrees.
- **Self-contained.** No pollution of the parent directory, no second
  location to migrate or back up, and sandbox rules stay within one root.

Rejected alternative: a sibling directory (`../<project>-workspaces/`). It
keeps the project directory "clean" but breaks walk-up discovery (forcing
`GRIGLIA_PROJECT` everywhere), leaks state into the parent directory, and
widens the sandbox write surface beyond the project root.

`griglia init` will additionally ignore `worktrees/` in the `.gitignore` it
drops inside `.griglia/` (existing projects: the migration note in §12).

### 5.2 Naming

- Directory: `task-<id>` — stable even if the title is edited later.
- Branch: `griglia/task-<id>-<slug>`, where `<slug>` is derived once from the
  task title at allocation (lowercase, non-alphanumerics collapsed to `-`,
  truncated to ~40 chars) and **persisted** in the workspace row. Determinism
  comes from persistence, not recomputation: a title edit never renames a
  branch. The `griglia/` prefix namespaces managed branches away from human
  branches (`feat/…`, `fix/…`).

### 5.3 Base commit and dirty repositories

`workspace create` resolves the base commit as the main checkout's current
`HEAD` at allocation time, records it, and creates the branch there
(`git worktree add <path> -b <branch> <base>`). `--base REF` overrides it
(e.g. `--base main`) for repositories where the checkout may sit on an
unrelated branch.

- A **dirty main checkout is not an error**: `git worktree add` never touches
  uncommitted state, and the new worktree simply does not see it. The human
  output notes it ("base is HEAD; uncommitted changes in the main checkout
  are not included"); JSON reports the resolved `base_commit`.
- An **unborn or detached-orphan HEAD** (no resolvable commit) is a typed
  error: there is nothing to base a worktree on.

### 5.4 Existing branches

- Branch exists **and belongs to a retained workspace for the same task**:
  this is adoption (§4) — reuse it, do not re-create it.
- Branch exists **with no workspace record** (a human or foreign-tool
  branch): fail with `conflict`. Griglia never silently adopts a branch it
  did not create (invariant 5). The human can delete/rename the branch or
  the agent can be pointed elsewhere; an explicit `--branch NAME` escape
  hatch may be added later if real use demands it, but is out of v0.2.

### 5.5 No remote, no network

Workspace operations are purely local: no fetch, no pull, no push, ever —
consistent with "Griglia never talks to a network". A repository without a
remote is fully supported; publishing the branch and opening PRs remain the
agent's (or launcher's) responsibility.

### 5.6 Is Git required?

For workspace commands, yes: they fail with a typed error when the project
root is not inside a Git repository (detected via
`git rev-parse --git-common-dir` from the project root). Everything else in
Griglia remains Git-free. Non-Git workspace backends are a non-goal.

Griglia shells out to the `git` binary (minimum version documented at
implementation time; `git worktree` needs ≥ 2.5, practically ≥ 2.17 for
`worktree remove`). No Git library dependency: worktree semantics are subtle
and the CLI is the compatibility surface every agent already has.

## 6. Shared project identity and discovery

The authoritative database is always `<project>/.griglia/griglia.db` of the
main repository. Three mechanisms, in the existing precedence order:

1. `--project PATH` flag;
2. `GRIGLIA_PROJECT` environment variable;
3. upward walk from the current directory.

Because worktrees live under `<project>/.griglia/worktrees/`, mechanism 3
already resolves correctly from anywhere inside a worktree — an agent that
just runs `griglia task …` in its workspace hits the right database with no
configuration. This is the main reason the in-`.griglia` layout wins.

Explicit pinning stays recommended for launchers: `workspace show --json`
exposes `project_root` and `database` (§10), so a launcher can set
`GRIGLIA_PROJECT=<project_root>` in the agent's environment. This is more
robust than discovery when a sandbox remaps or restricts paths.

Guard rails:

- `griglia init` refuses to run inside `.griglia/worktrees/…` (clear error
  pointing at the existing project) so a confused agent cannot create a
  nested board. It keeps warning-and-proceeding elsewhere as today.
- No `.griglia` directory, marker file, or database copy is ever created
  inside a worktree. A per-worktree pointer file was considered and rejected:
  it duplicates state that discovery already derives, and it would be one
  more thing to repair after manual deletions.

## 7. Data model and state machine

### 7.1 Schema (migration 006)

```sql
CREATE TABLE workspaces(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN
    ('allocating','active','retained','failed','removed')),
  path TEXT NOT NULL,            -- relative to project root: .griglia/worktrees/task-7
  branch TEXT NOT NULL,          -- griglia/task-7-fix-paste
  base_commit TEXT NOT NULL DEFAULT '',
  agent_name TEXT NOT NULL,      -- identity of the allocating/adopting claim
  instance_id TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  removed_at TEXT,
  error TEXT NOT NULL DEFAULT '' -- last git failure, for state 'failed'
);

CREATE UNIQUE INDEX one_live_workspace_per_task
  ON workspaces(task_id) WHERE state IN ('allocating','active','retained');
CREATE UNIQUE INDEX one_live_workspace_per_path
  ON workspaces(path) WHERE state IN ('allocating','active','retained');
CREATE UNIQUE INDEX one_live_workspace_per_branch
  ON workspaces(branch) WHERE state IN ('allocating','active','retained');
```

Rows in `failed`/`removed` are kept for audit (events reference them);
uniqueness only constrains live rows, so a failed allocation can be retried.

### 7.2 States

| State | Meaning |
|---|---|
| `allocating` | row reserved in the database; git side effects in flight |
| `active` | worktree exists and the task is claimed by the recorded identity |
| `retained` | worktree exists; the allocating claim ended (done/release/cancel) |
| `failed` | git operations failed after reservation; directory/branch not usable |
| `removed` | worktree pruned; terminal |

`active` vs `retained` is derived-adjacent but persisted deliberately: the
transition happens in the same transaction that closes the claim, so the two
can never disagree (invariant 6 of the existing model: one transaction, one
event).

As with tasks, a separate derived **health** (§11) reports what the
filesystem/Git actually look like; `state` records intent, `health` records
reality, and they are never conflated in one field — the same
lifecycle/operational-state separation the task model already uses.

### 7.3 Lifecycle flows

```text
task available
  → claim (unchanged)
  → workspace create        reserve row (allocating) ──git ok──► active
                                       └──git fails──► failed (retryable)
  → work, progress, ask/answer …
  → done | release | cancel  ──► workspace active → retained (same tx as claim close)
  → review/rework            ──► claim again → adopt retained workspace → active
  → workspace remove         ──► removed (worktree pruned; branch kept by default)
```

Per scenario:

- **Successful completion** (`task done`): claim closes, workspace →
  `retained`. The branch almost certainly needs to outlive the task (PR
  review), so nothing is deleted automatically.
- **Release** (`task release`): same — `retained`. The next claimant adopts
  the workspace with the partial work in place.
- **Cancellation**: same — `retained`; the human decides when to
  `workspace remove`.
- **Agent crash**: nothing changes (claims never expire — existing policy).
  The workspace stays `active` with the crashed identity. The human releases
  the claim (existing flow), which moves the workspace to `retained`.
- **Abandoned claim**: identical to crash; `workspace list` shows the
  claim's `last_activity_at` beside the workspace so staleness is visible.
- **Failed creation**: row → `failed` with the git error recorded; the claim
  is untouched (workspace and claim are decoupled), and `workspace create`
  can be retried — the failed row does not block the partial unique index.
  Retry first attempts to clean any half-created directory/registration.
- **Review/rework after done**: `done` is terminal for the task lifecycle,
  so rework happens either on a follow-up task (fresh workspace, optionally
  based on the retained branch via `--base`) or by the human working directly
  in the retained worktree. Reopening tasks is out of scope here.

### 7.4 Should claim allocate the workspace?

**No — allocation stays an explicit operation** (`workspace create`), for
v0.2:

- `claim`/`claim-next` keep their exact current semantics and JSON — no
  behavior change for existing agents, non-Git projects, or agents that
  don't want worktrees (invariant 7).
- Claim is a pure database transaction today; bundling git side effects into
  it would turn its failure modes from "transactional" into "partial".
- The launcher usually wants to create the workspace *before* the agent
  starts (to derive sandbox paths), which the explicit command supports:
  `claim-next … && workspace create <id> …` is two lines.

A convenience flag (`task claim-next --workspace`) that performs
claim-then-allocate as two steps with a combined payload can be added later
without protocol changes; it is sugar, not model.

## 8. Concurrency

Allocation uses the same reservation pattern as `claim-next`:

1. open an immediate write transaction;
2. verify the caller owns the active claim on the task (else `conflict`);
3. insert the workspace row in `allocating` with path and branch — the three
   partial unique indexes make double-allocation, path collision, and branch
   collision impossible under any interleaving;
4. append the `workspace_allocating` event; commit.
5. outside the transaction, run the git operations;
6. second write transaction: state → `active` (+event), or → `failed` with
   the error (+event).

Two racing `workspace create` calls for one task: the second insert violates
`one_live_workspace_per_task` → stable `conflict` error, exactly like a lost
claim race. A crash between steps 4 and 6 leaves an `allocating` row whose
age exposes it as stuck; recovery is §11 (no lease/timeout is introduced —
same philosophy as claims: explicit recovery, no magic expiry).

Branch names can still collide with branches Griglia has no record of
(created by humans after the check); that surfaces as a git failure in step
5 → `failed`, never as silent reuse.

Existing task/claim invariants remain authoritative and untouched: no
workspace state ever feeds into `operational_state` derivation or claim
eligibility.

## 9. Sandbox integration

Griglia does not sandbox anything itself; it publishes the facts a launcher
needs. The permission profile an external launcher should derive for an
agent working on task N:

| Path | Access | Why |
|---|---|---|
| `<project>/.griglia/worktrees/task-N/` | read/write | the agent's own worktree |
| `<project>/.git/` | read/write | shared objects/refs, plus `.git/worktrees/<name>/` (the worktree's index and HEAD live here) |
| `<project>/.griglia/` db files (`griglia.db`, `-wal`, `-shm`) | read/write | the authoritative board; WAL needs write to all three |
| `<project>/.griglia/worktrees/` (other entries) | none | isolation between agents |
| `<project>` main checkout files | none, or read-only | read-only is convenient for reference; write would recreate the round-1 failure |

Notes for launcher authors:

- Derive paths from `workspace show --json` (§10) — `path`, `git_common_dir`,
  `database`, `project_root` are all in the payload precisely so launchers
  never hardcode layout.
- `.git` write access is unavoidably shared: commits, refs, and the worktree
  registration all live there. A stricter profile could narrow to
  `objects/`, `refs/`, `logs/`, and `worktrees/task-N/`, but git also
  touches `config.worktree`, `packed-refs`, and lock files at the top level;
  whole-`.git` read/write is the honest baseline.
- The database is the coordination channel, and filesystem permissions are
  the trust boundary (unchanged from the README's trust model): a sandboxed
  agent with database write access can act as any identity. Workspaces
  isolate *file* work, not board authority.
- Nothing here is Claude- or Codex-specific; any launcher that can grant
  per-path read/write can implement the profile.

## 10. CLI and protocol

```text
griglia workspace create TASK_ID --agent NAME --instance ID [--base REF] [--json]
griglia workspace list [--json]
griglia workspace show TASK_ID [--json]
griglia workspace remove TASK_ID [--delete-branch] [--json]
```

- `create` requires the active claim owner's identity (agent command, like
  `progress`). Adoption of a retained workspace is `create` on a task that
  has one — idempotent-by-adoption, mirroring "re-claiming an owned task is
  idempotent".
- `show`/`list` are reads, open to humans and agents alike; `show` addresses
  by task id because that is the stable key users hold (workspace row ids
  stay internal).
- `remove` is a human-side command (no identity required) when the
  workspace is `retained`/`failed`; removing an `active` workspace requires
  the owning identity or explicit `--force`, mirroring the claim model's
  human-override philosophy. It runs `git worktree remove` +
  `git worktree prune`, refuses (without `--force`) when the worktree has
  uncommitted changes, and keeps the branch unless `--delete-branch`.

Workspace DTO (additive, protocol v1):

```json
{
  "task_id": 7,
  "state": "active",
  "health": "ok",
  "path": "/abs/project/.griglia/worktrees/task-7",
  "branch": "griglia/task-7-fix-paste",
  "base_commit": "dfe0349…",
  "agent_name": "claude",
  "instance_id": "design-v02-001",
  "project_root": "/abs/project",
  "database": "/abs/project/.griglia/griglia.db",
  "git_common_dir": "/abs/project/.git",
  "created_at": "2026-08-24T10:00:00.000000Z",
  "updated_at": "2026-08-24T10:00:01.000000Z",
  "error": ""
}
```

Payloads: `workspace create|show|remove` → `{"workspace": Workspace}`;
`workspace list` → `{"workspaces": [Workspace]}`. Paths are absolute in JSON
(launchers consume them directly); the database stores the project-relative
path so the project stays movable.

The Task DTO gains an additive, always-present `workspace` key: `null` or a
reduced object `{"state","branch","path"}` — enough for the TUI and for
agents to notice a workspace exists without a second call.

Errors reuse the existing stable codes: `invalid_input` (bad task id, task
not claimable-state), `not_found`, `conflict` (not claim owner, live
workspace exists, foreign branch collision), `busy`. One new code,
`git_error` (exit 1 family, message carries the git stderr), covers step-5
failures; it is additive surface on new commands only, so protocol v1 is
preserved under the documented compatibility policy.

## 11. Recovery

`state` records intent; **`health`** is derived on read (never persisted) by
cross-checking the filesystem and `git worktree list --porcelain`:

| Health | Detection | Repair |
|---|---|---|
| `ok` | directory exists and is a registered worktree on the recorded branch | — |
| `missing_dir` | directory manually deleted | `workspace remove` (tolerates the absence, prunes git metadata, row → `removed`) |
| `unregistered` | directory exists but git no longer lists it (e.g. `git worktree prune` ran while the dir was on an unmounted disk) | `workspace remove`, or manual `git worktree repair` then re-check |
| `stuck_allocating` | state `allocating` older than a threshold (e.g. 10 min) | retry `workspace create` (cleans partial state) or `workspace remove` |
| `diverged` | worktree checked out on a different branch than recorded | informational; humans/agents may do this deliberately — never auto-"fixed" |

Principles:

- **No background repair.** Griglia is a CLI/TUI, not a daemon; health is
  computed when `workspace list/show` runs and displayed in the TUI's
  refresh cycle. Repair is always an explicit command.
- **Griglia restart is a non-event**: the database is the only state, health
  is recomputed on demand.
- **Crashed agents** are handled through the existing claim flow (§7.3);
  workspaces add visibility (`last_activity_at` shown in `workspace list`),
  not new expiry semantics.
- **Task/workspace disagreement cannot arise** where it matters: the
  active→retained transition shares the claim-closing transaction. Anything
  else (retained workspace on a done task) is a designed, legal state.

## 12. TUI, migration, compatibility

**TUI (v0.2 scope):**

- Task **detail** gains a workspace section: path (project-relative for
  display), branch, state, health, and owning identity — next to the
  existing claim block.
- The task **list** gains at most a one-cell indicator on wide layouts
  (e.g. `⌂` when a live workspace exists, dimmed/marked when health ≠ ok),
  following the existing "symbols + text, never color-only" rule. No new
  columns, no workspace grouping — the list stays a task list.
- A dedicated workspaces screen is deferred until real use shows the detail
  view is not enough.

**Migration and compatibility:**

- Migration `006_workspaces.sql` is purely additive; existing databases
  upgrade transparently under the existing checksummed runner.
- Protocol v1 is preserved: new commands, new payload keys, and the additive
  `workspace` field on Task follow the documented additive policy; agents
  parse tolerantly and ignore unknown keys.
- `griglia init` on new projects writes `worktrees/` into `.griglia/`'s
  `.gitignore`; for existing projects the first `workspace create` appends
  the entry if missing (idempotent, single-line change).
- Everything is opt-in: projects that never run `workspace create` see no
  behavior change anywhere.

## 13. Implementation phases

1. **Model + storage**: migration 006, domain entity/states, allocation and
   removal use cases with the two-phase pattern, git runner behind a small
   port in `internal/app` (mockable; real implementation shells out).
   Concurrency tests mirroring the claim-race suite (two processes racing
   `workspace create`; crash between phases; retry after `failed`).
2. **CLI + protocol**: the four `workspace` commands, DTOs, `git_error`
   mapping, golden protocol tests, PROTOCOL.md and AGENT_INTEGRATION.md
   updates (including the launcher permission table from §9).
3. **TUI**: workspace section in detail, list indicator, health surfaced in
   the existing ~1 s refresh.
4. **Polish**: init/gitignore integration, `init`-inside-worktree guard,
   recovery UX (`stuck_allocating` threshold, `remove --force` paths),
   docs for launcher authors.

Each phase lands with docs and tests, per the existing milestone discipline;
phase 1+2 are the minimum useful slice (launchers can already automate
round-2 manually today, so shipping the model + CLI first is what removes
the manual steps).
