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
the authoritative `.griglia` database — and to nothing else. Dogfooding also
showed that agents benefit from knowing *explicitly* which Griglia project
they operate against, rather than relying on implicit discovery.

Today that setup is manual. This design makes Griglia able to model, create,
list, and clean up such workspaces, without turning Griglia into an agent
launcher.

## 2. Goals and non-goals

Goals (v0.2 candidate):

- a persisted **workspace model** tied to the existing task model;
- race-safe **allocation** of one Git worktree + branch per task;
- deterministic directory and branch naming, **outside the main checkout and
  outside `.griglia`**;
- **explicit project identity** for agents in isolated workspaces
  (`--project` / `GRIGLIA_PROJECT` pinning as the recommended mode);
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

1. **Claims stay the only source of ownership.** A workspace never grants,
   implies, or *stores* ownership; existing claim/eligibility semantics are
   unchanged, and workspace rows carry no ownership state to drift out of
   sync (§7).
2. **At most one live workspace per task.** Enforced by a partial unique
   index, exactly like `one_active_claim_per_task`.
3. **Workspace paths and branches are unique** across live workspaces —
   two allocations can never receive the same directory or branch.
4. **One authoritative database per project.** A worktree never contains its
   own `.griglia`; every workspace operates against the main project's
   database, addressed explicitly (§6).
5. **Griglia only touches what it created.** It never adopts, reuses, or
   deletes a branch or directory it has no record of; conflicts with foreign
   branches/paths are typed errors, not silent reuse.
6. **Every mutation is one SQLite transaction plus an audit event.** Git side
   effects happen outside the transaction; the database records intent first
   and outcome after (two-phase allocation, §7).
7. **Claim transitions never mutate workspace rows.** `claim`, `claim-next`,
   `release`, `done`, and `cancel` keep their exact current write sets; a
   workspace survives all of them without a state change.
8. **Workspace support is optional.** Projects without Git, and agents that
   ignore workspaces entirely, keep working exactly as today.

## 4. Workspace identity

**A workspace is keyed by task; usage and ownership are derived from
claims.**

- The row records `task_id`, the resource facts (path, branch, base commit),
  and — as audit only — the identity of the claim that allocated it. It does
  **not** record a current owner: whoever holds the task's active claim is,
  by derivation, the workspace's current user.
- **One agent instance may use multiple workspaces** — one per task it has
  claimed. Nothing associates a workspace with an instance directly; the
  association always goes through the task's active claim.
- **A workspace survives claim release, completion, and cancellation**
  without any state transition (§7): the worktree, branch, and
  work-in-progress commits stay in place. A later claim of the same task —
  by the same or a different identity — simply resumes in the existing
  workspace; no adoption step, no identity rewrite, because there is no
  stored identity to rewrite. `workspace create` on a task that already has
  a live workspace returns that workspace (idempotent, mirroring
  "re-claiming an owned task is idempotent").

Why not key by agent instance (the round-2 layout)? A long-lived per-agent
worktree must switch branches between tasks, which breaks the natural
workspace ↔ branch ↔ PR mapping: the moment the agent picks up task B, the
worktree no longer matches task A's PR under review. Per-task workspaces keep
review/rework trivial (the branch and directory are still there) and make
cleanup decisions local to one task. Why not key by claim id? Claims are
closed rows; tying the resource to a dead claim id complicates reuse for no
benefit. Task identity is stable across the whole review/rework cycle.

## 5. Git strategy

### 5.1 Location: a sibling workspace root

Worktrees live **outside the main checkout and outside `.griglia`**, under a
deterministic per-project workspace root that defaults to a sibling of the
project directory:

```text
<project-parent>/.griglia-worktrees/<project-name>/task-<id>/
```

For a project at `/home/alle/Projects/griglia-tui`, task 7's workspace is
`/home/alle/Projects/.griglia-worktrees/griglia-tui/task-7/`.
`<project-name>` is the project directory's basename, which is unique within
its parent, so the layout is deterministic and collision-free.

Rationale:

- **`.griglia` stays coordination state, not working copies.** The database
  (plus `-wal`/`-shm`) remains small, self-contained, and trivially
  backed up or copied; potentially large source checkouts never mix with it,
  and "delete the workspace root" is a clean, unambiguous cleanup action
  that cannot touch board state.
- **The main checkout stays untouched.** No generated directories inside the
  repository tree, nothing new to ignore, and repository-wide tooling
  (search, builds, linters) can never wander into another agent's worktree.
- **It mirrors what actually worked.** The round-2 experiment used sibling
  worktrees; this default reproduces that proven layout, managed.
- **The root can become configurable later** (a project setting choosing a
  different base directory) without changing the model: the workspace row
  records each worktree's actual path, so a root change only affects new
  allocations.

Rejected alternative: `<project>/.griglia/worktrees/`. It would make upward
database discovery work implicitly from inside a worktree, but it stuffs
working copies into the coordination directory — muddying backup/copy/cleanup
semantics — and hides checkouts inside a dot-directory of the repository.
Explicit project pinning (§6) removes the discovery advantage, and the
separation of state from working copies wins.

Because the recorded worktree paths (and Git's own worktree links) are
absolute, moving or renaming the project directory strands existing
workspaces; health surfaces this (§11) and `git worktree repair` covers the
Git side. A `workspace repair` command is deferred.

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

- Branch exists **and belongs to the task's live workspace**: this is the
  idempotent-reuse case (§4) — return the existing workspace, do not
  re-create anything.
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

## 6. Explicit project identity

The authoritative database is always `<project>/.griglia/griglia.db` of the
main repository. Griglia resolves the target project with the existing
precedence, unchanged:

1. `--project PATH` flag (project root or database path);
2. `GRIGLIA_PROJECT` environment variable;
3. upward walk from the current directory.

With worktrees outside the project tree, upward discovery (3) no longer
finds the board from inside a workspace — and worse, it could silently
resolve an *unrelated* ancestor project if one exists above the workspace
root. **For isolated workspaces, explicit pinning is therefore the
recommended and documented mode, not a fallback:**

```bash
GRIGLIA_PROJECT=/absolute/project/root griglia task progress 7 40 …
# or per command:
griglia --project /absolute/project/root task progress 7 40 …
```

This is a feature, not a workaround: real dogfooding showed agents work
better when they know exactly which board they are operating against, and an
explicit absolute root is robust where sandboxes remap or restrict paths and
where cwd is ambiguous. Recommended launcher behavior:

1. run `griglia workspace create <task-id> … --json` (from the project, or
   with `--project`);
2. read `project_root` and `path` from the payload (§10);
3. start the agent with cwd = `path` and `GRIGLIA_PROJECT=<project_root>`
   exported in its environment.

Upward discovery remains what it is today for humans working inside the main
checkout; nothing changes there.

Guard rails:

- `griglia init` refuses to run inside a directory that is a **linked Git
  worktree whose main checkout contains `.griglia`** (detected via
  `git rev-parse --git-common-dir`), with an error pointing at the existing
  project and the pinning syntax above — so a confused agent cannot create a
  second, nested board.
- No `.griglia` directory, marker file, or database copy is ever created
  inside a worktree (invariant 4). A per-worktree pointer file was
  considered and rejected: it duplicates what the launcher already pins via
  environment, and it would be one more thing to repair after manual
  deletions.

## 7. Data model and state machine

### 7.1 Persisted state describes the resource, not ownership

The workspace row models the lifecycle of the Git resource itself:

| State | Meaning |
|---|---|
| `allocating` | row reserved in the database; git side effects in flight |
| `ready` | worktree and branch exist and are usable |
| `failed` | git operations failed after reservation; directory/branch not usable |
| `removed` | worktree pruned; terminal |

There is deliberately **no `active`/`retained` split and no ownership
state**: whether a workspace is currently in use is a fact about the task's
claim, not about the Git resource, and persisting it would duplicate the
claim table — exactly the "persist `working` next to a claim" mistake the
core design rejected. This mirrors Griglia's founding principle: persist
durable lifecycle facts, derive operational state from current authoritative
facts.

Read models derive **usage** on demand:

- `in_use` — the task has an active claim (its identity is reported
  alongside, so callers can tell whether *they* are the user);
- `idle` — no active claim; the workspace is parked (post-release,
  post-done, post-cancel, or simply not picked up yet).

Consequently claim transitions never touch workspace rows (invariant 7):
`done`, `release`, and `cancel` keep their exact current transactions, and a
`ready` workspace stays `ready` through all of them. Authorization for
workspace *mutations* still consults the active claim (§10) — derivation at
check time, not stored state.

A separate derived **health** (§11) reports what the filesystem/Git actually
look like: `state` records resource intent, `usage` records current claim
facts, `health` records filesystem reality. Three orthogonal axes, never
conflated in one field.

### 7.2 Schema (migration 006)

```sql
CREATE TABLE workspaces(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN ('allocating','ready','failed','removed')),
  path TEXT NOT NULL,            -- absolute: <parent>/.griglia-worktrees/<name>/task-7
  branch TEXT NOT NULL,          -- griglia/task-7-fix-paste
  base_commit TEXT NOT NULL DEFAULT '',
  created_by_agent TEXT NOT NULL,    -- audit: identity of the allocating claim
  created_by_instance TEXT NOT NULL, -- audit only; never an ownership check input
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  removed_at TEXT,
  error TEXT NOT NULL DEFAULT '' -- last git failure, for state 'failed'
);

CREATE UNIQUE INDEX one_live_workspace_per_task
  ON workspaces(task_id) WHERE state IN ('allocating','ready');
CREATE UNIQUE INDEX one_live_workspace_per_path
  ON workspaces(path) WHERE state IN ('allocating','ready');
CREATE UNIQUE INDEX one_live_workspace_per_branch
  ON workspaces(branch) WHERE state IN ('allocating','ready');
```

`created_by_*` is provenance for the audit trail, like `asked_by_*` on
questions; ownership checks always go to the live `claims` table. Rows in
`failed`/`removed` are kept for audit (events reference them); uniqueness
only constrains live rows, so a failed allocation can be retried.

Events: `workspace_allocating`, `workspace_ready`, `workspace_failed`,
`workspace_removed` — appended in the same transaction as the corresponding
row change, per the existing one-transaction-one-event rule. Claim events
are unchanged.

### 7.3 Lifecycle flows

```text
resource axis (persisted):
  workspace create   reserve row (allocating) ──git ok──► ready
                                └──git fails──► failed (retryable)
  workspace remove   ready | failed ──► removed (worktree pruned; branch kept by default)

claim axis (existing, untouched):
  available → claim → working … → done | release | cancel → (maybe re-claim)

derived usage = join of the two axes:
  ready + active claim   → in_use
  ready + no claim       → idle
```

Per scenario:

- **Successful completion** (`task done`): claim closes as today; the
  workspace row is untouched and stays `ready`, now derived `idle`. The
  branch almost certainly needs to outlive the task (PR review), so nothing
  is deleted automatically.
- **Release** (`task release`): same — `ready`/`idle`. The next claimant
  resumes in the same worktree with the partial work in place.
- **Cancellation**: same — `ready`/`idle`; the human decides when to
  `workspace remove`.
- **Agent crash**: nothing changes anywhere (claims never expire — existing
  policy). The workspace reads as `in_use` by the crashed identity until the
  human releases the claim (existing flow), at which point it derives `idle`
  — with no workspace write in between.
- **Abandoned claim**: identical to crash; `workspace list` shows the active
  claim's `last_activity_at` beside the workspace so staleness is visible.
- **Failed creation**: row → `failed` with the git error recorded; the claim
  is untouched, and `workspace create` can be retried — the failed row does
  not block the partial unique indexes. Retry first attempts to clean any
  half-created directory/registration.
- **Review/rework after done**: `done` is terminal for the task lifecycle,
  so rework happens either on a follow-up task (fresh workspace, optionally
  based on the existing branch via `--base`) or by the human working
  directly in the idle worktree. Reopening tasks is out of scope here.

### 7.4 Should claim allocate the workspace?

**No — allocation stays an explicit operation** (`workspace create`), for
v0.2:

- `claim`/`claim-next` keep their exact current semantics, write sets, and
  JSON — no behavior change for existing agents, non-Git projects, or agents
  that don't want worktrees (invariants 7 and 8).
- Claim is a pure database transaction today; bundling git side effects into
  it would turn its failure modes from "transactional" into "partial".
- The launcher usually wants to create the workspace *before* the agent
  starts (to derive sandbox paths and pin `GRIGLIA_PROJECT`), which the
  explicit command supports: `claim-next … && workspace create <id> …` is
  two lines.

A convenience flag (`task claim-next --workspace`) that performs
claim-then-allocate as two steps with a combined payload can be added later
without protocol changes; it is sugar, not model.

## 8. Concurrency

Allocation uses the same reservation pattern as `claim-next`:

1. open an immediate write transaction;
2. verify the caller owns the active claim on the task (else `conflict`) —
   an ownership *check* against the claims table, not a workspace-row fact;
3. insert the workspace row in `allocating` with path and branch — the three
   partial unique indexes make double-allocation, path collision, and branch
   collision impossible under any interleaving;
4. append the `workspace_allocating` event; commit.
5. outside the transaction, run the git operations;
6. second write transaction: state → `ready` (+event), or → `failed` with
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
eligibility, and no claim transition writes a workspace row.

## 9. Sandbox integration

Griglia does not sandbox anything itself; it publishes the facts a launcher
needs. Because worktrees live outside the project tree, a sandbox profile
spans two disjoint roots: the workspace directory, and the narrow slice of
the project the agent still needs. The profile an external launcher should
derive for an agent working on task N of project `<project>` (workspace root
`<parent>/.griglia-worktrees/<name>/`):

| Path | Access | Why |
|---|---|---|
| `<parent>/.griglia-worktrees/<name>/task-N/` | read/write | the agent's own worktree |
| `<project>/.git/` | read/write | shared objects/refs, plus `.git/worktrees/<dir>/` (the worktree's index and HEAD live here) |
| `<project>/.griglia/` db files (`griglia.db`, `-wal`, `-shm`) | read/write | the authoritative board; WAL needs write to all three |
| `<parent>/.griglia-worktrees/<name>/` (other entries) | none | isolation between agents |
| `<project>` main checkout files | none, or read-only | read-only is convenient for reference; write would recreate the round-1 failure |

Notes for launcher authors:

- Derive every path from `workspace show --json` (§10) — `path`,
  `git_common_dir`, `database`, `project_root` are all in the payload
  precisely so launchers never hardcode layout, and `project_root` doubles
  as the value to pin via `GRIGLIA_PROJECT` (§6).
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

- `create` requires the identity of the task's active claim owner (agent
  command, like `progress`); the check reads the claims table at call time.
  On a task whose live workspace already exists, `create` by the claim owner
  returns that workspace — idempotent reuse (§4).
- `show`/`list` are reads, open to humans and agents alike; `show` addresses
  by task id because that is the stable key users hold (workspace row ids
  stay internal).
- `remove` is a human-side command (no identity required) when the
  workspace derives `idle`; when the task has an active claim (`in_use`),
  it requires the owning identity or explicit `--force`, mirroring the
  claim model's human-override philosophy. It runs `git worktree remove` +
  `git worktree prune`, refuses (without `--force`) when the worktree has
  uncommitted changes, and keeps the branch unless `--delete-branch`.

Workspace DTO (new payloads only — see below on the Task DTO):

```json
{
  "task_id": 7,
  "state": "ready",
  "usage": "in_use",
  "health": "ok",
  "active_claim": {"agent_name":"claude","instance_id":"design-v02-001","claimed_at":"2026-08-24T09:58:00.000000Z"},
  "path": "/home/alle/Projects/.griglia-worktrees/griglia-tui/task-7",
  "branch": "griglia/task-7-fix-paste",
  "base_commit": "dfe0349…",
  "created_by": {"agent_name":"claude","instance_id":"design-v02-001"},
  "project_root": "/home/alle/Projects/griglia-tui",
  "database": "/home/alle/Projects/griglia-tui/.griglia/griglia.db",
  "git_common_dir": "/home/alle/Projects/griglia-tui/.git",
  "created_at": "2026-08-24T10:00:00.000000Z",
  "updated_at": "2026-08-24T10:00:01.000000Z",
  "error": ""
}
```

`state` is the persisted resource lifecycle; `usage` (`in_use` | `idle`) and
`active_claim` (existing Claim DTO shape, `null` when idle) are derived from
the claims table at read time; `health` is derived from filesystem/Git
(§11); `created_by` is audit provenance. The six fields a launcher needs —
`task_id`, `path`, `branch`, `project_root`, `database`, `git_common_dir` —
are always present. Paths are absolute in JSON and in storage.

Payloads: `workspace create|show|remove` → `{"workspace": Workspace}`;
`workspace list` → `{"workspaces": [Workspace]}`.

**The Task DTO does not change in this slice.** Workspace facts are exposed
only through the explicit `workspace show`/`workspace list` read models: the
feature is opt-in and still experimental, and the core task protocol should
not grow until real usage proves agents need workspace metadata embedded in
every task response. If that need materializes, a reduced summary (e.g.
`"workspace": {"state","branch","path"}`) can be added later as a purely
additive protocol-v1 change under the documented compatibility policy —
agents already parse tolerantly and ignore unknown keys. `protocol_version`
stays `"1"` either way.

Errors reuse the existing stable codes: `invalid_input` (bad task id, task
not claimable-state), `not_found`, `conflict` (not claim owner, live
workspace exists for a racing creator, foreign branch collision), `busy`.
One new code, `git_error` (exit 1 family, message carries the git stderr),
covers step-5 failures; it is additive surface on new commands only, so
protocol v1 is preserved under the documented compatibility policy.

## 11. Recovery

`state` records resource intent; **`health`** is derived on read (never
persisted) by cross-checking the filesystem and
`git worktree list --porcelain`:

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
- **Griglia restart is a non-event**: the database is the only state; usage
  and health are recomputed on demand.
- **Crashed agents** are handled through the existing claim flow (§7.3);
  workspaces add visibility (the active claim's `last_activity_at` shown in
  `workspace list`), not new expiry semantics.
- **Task/workspace state cannot disagree, by construction**: since claim
  transitions never write workspace rows and usage is derived at read time,
  there is no cross-entity transition to miss. A `ready` workspace on a
  done task is not an anomaly to detect — it is the designed `idle` case.
- **Moved/renamed project directory**: recorded absolute paths and Git's
  worktree links both break; health reports `missing_dir`/`unregistered`.
  Repair is `git worktree repair` plus re-check, or `workspace remove`; a
  guided `workspace repair` command is deferred (§5.1).

## 12. TUI, migration, compatibility

**TUI (v0.2 scope):**

- Task **detail** gains a workspace section: path, branch, state, usage,
  health — next to the existing claim block. The application layer serves it
  through a bounded, dedicated workspace read-model query (the same use-case
  the CLI's `workspace show` consumes); the Task DTO and task queries are
  untouched.
- The task **list** gains at most a one-cell indicator on wide layouts
  (e.g. `⌂` when a live workspace exists, dimmed/marked when health ≠ ok),
  fed by the same bounded query for the listed tasks, following the existing
  "symbols + text, never color-only" rule. No new columns, no workspace
  grouping — the list stays a task list.
- A dedicated workspaces screen is deferred until real use shows the detail
  view is not enough.

**Migration and compatibility:**

- Migration `006_workspaces.sql` is purely additive; existing databases
  upgrade transparently under the existing checksummed runner.
- Protocol v1 is preserved: new commands and new payload shapes only; the
  Task DTO is unchanged in this slice (§10), and `protocol_version` stays
  `"1"`.
- No `.gitignore` changes are needed anywhere: worktrees live entirely
  outside the repository tree, so there is nothing new to ignore.
- Everything is opt-in: projects that never run `workspace create` see no
  behavior change anywhere.

## 13. Implementation phases

1. **Model + storage**: migration 006, domain entity/states, allocation and
   removal use cases with the two-phase pattern, derived usage in the
   workspace read model, git runner behind a small port in `internal/app`
   (mockable; real implementation shells out). Concurrency tests mirroring
   the claim-race suite (two processes racing `workspace create`; crash
   between phases; retry after `failed`; claim close leaving workspace rows
   untouched).
2. **CLI + protocol**: the four `workspace` commands, DTOs, `git_error`
   mapping, golden protocol tests (including asserting the Task DTO is
   unchanged), PROTOCOL.md and AGENT_INTEGRATION.md updates — the launcher
   permission table from §9 and the explicit `--project`/`GRIGLIA_PROJECT`
   pinning guidance from §6.
3. **TUI**: workspace section in detail via the bounded read model, list
   indicator, usage/health surfaced in the existing ~1 s refresh.
4. **Polish**: `init`-inside-worktree guard, recovery UX
   (`stuck_allocating` threshold, `remove --force` paths), configurable
   workspace root (if demanded), docs for launcher authors.

Each phase lands with docs and tests, per the existing milestone discipline;
phase 1+2 are the minimum useful slice (launchers can already automate
round-2 manually today, so shipping the model + CLI first is what removes
the manual steps).
