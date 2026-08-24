# Griglia TUI

Griglia is a local, transactional todo list shared by a human and independent
coding agents. One binary, two interfaces over the same core:

- an interactive, keyboard-first **TUI** for exploring, editing, and answering
  agent questions;
- deterministic **CLI commands with a stable JSON protocol**, designed for
  automation: discover eligible work, claim it without races, report progress,
  and stop for a human decision when needed.

**What Griglia is not**: a task runner, an agent supervisor or launcher, an LLM
SDK, or a distributed system. It never starts processes, never talks to a
network, and makes no assumptions about which agent tooling you use.

## Local-first model

State lives in a single SQLite database at `.griglia/griglia.db` inside your
project. Commands discover it by walking upward from the current directory;
`--project PATH` or the `GRIGLIA_PROJECT` environment variable select a project
explicitly. The database (plus its `-wal`/`-shm` companions) is local working
state, not something to commit: `griglia init` drops a `.gitignore` inside
`.griglia/` for you. WAL mode is designed for concurrent processes on one
machine — network filesystems are not supported.

## Installation

### Download a release binary

Grab the archive for your platform from the GitHub releases page, verify it
against `checksums.txt`, extract it, and put `griglia` on your `PATH`:

```bash
tar -xzf griglia_<version>_linux_amd64.tar.gz
install -m 0755 griglia_<version>_linux_amd64/griglia ~/.local/bin/griglia
```

On Windows, extract the `.zip` and place `griglia.exe` somewhere on `PATH`.

### go install

```bash
go install github.com/alle80/griglia-tui/cmd/griglia@latest
```

Requires Go 1.25 or newer. The binary lands in `$(go env GOPATH)/bin`.

### Build from source

```bash
git clone https://github.com/alle80/griglia-tui.git
cd griglia-tui
CGO_ENABLED=0 go build -o griglia ./cmd/griglia
```

Griglia uses pure-Go SQLite, so no C toolchain is ever needed.

## Quick start

```bash
griglia init --name "My project"
griglia task add "Design the schema" --priority high
griglia task add "Implement API" --description "REST endpoints"
griglia task ready 1
griglia            # open the TUI
```

An agent picks up work like this:

```bash
griglia task claim-next --agent codex --instance session-1 --json
griglia task progress 1 40 --agent codex --instance session-1 --message "implementing"
griglia task done 1 --agent codex --instance session-1 --comment "done and tested" --json
```

## Lifecycle and operational states

A task's **lifecycle** records human intent and only ever holds `backlog`,
`ready`, `done`, or `cancelled`. Valid transitions: backlog → ready or
cancelled; ready → done or cancelled; `done` and `cancelled` are terminal.

For `ready` tasks Griglia derives an **operational state** from persisted
facts, with deterministic precedence:

| State | Meaning |
|---|---|
| `blocked` | at least one dependency is not `done` |
| `waiting_for_human` | claimed, with an unanswered blocking question |
| `working` | claimed, no open blocking question |
| `available` | unclaimed and unblocked: eligible for `claim-next` |

The invalid combination "working without an owner" cannot exist because the
claim itself is the only source of ownership.

## Agent claim workflow

- `task claim-next` atomically selects the highest-priority available task and
  claims it in the same transaction. Two racing agents can never obtain the
  same task; when nothing is eligible the stable `no_eligible_task` error is
  returned with exit code 4.
- `task claim ID` claims an explicitly chosen ready task.
- Identity is the pair `--agent NAME --instance ID`; both are required. Only
  the exact owner can update progress, ask questions, release, or complete a
  claimed task. Re-claiming a task you already own is idempotent.
- Claims never expire on their own; release or completion is always explicit.

## Isolated workspaces

In Git-backed projects, the owner of a claimed task can allocate an isolated
`git worktree` plus a managed branch instead of working in the main checkout:

```bash
griglia workspace create 7 --agent claude --instance session-1 --json
```

The worktree lands outside the repository at
`<project-parent>/.griglia-worktrees/<project-name>/task-7/` on branch
`griglia/task-7-<slug>`, based on the main checkout's `HEAD` (override with
`--base REF`). Because the worktree lives outside the main checkout, run
further commands from it with the board pinned explicitly:
`GRIGLIA_PROJECT=/abs/project/root` or `--project` (the flag wins over the
environment variable).

The workspace row records the resource only; whoever holds the task's active
claim is, by derivation, its current user (`usage` `in_use`/`idle` in the
read models). Workspaces survive release, completion, and cancellation —
work-in-progress and the branch stay put for review, and a later claim
resumes in place. `workspace show ID` / `workspace list` report state, usage,
and the absolute launcher facts; `workspace remove ID` prunes the worktree
(branch kept unless `--delete-branch`, uncommitted changes and in-use
removal guarded unless `--force` or the owning identity). Everything is
purely local: no fetch, pull, or push, ever. See
[docs/WORKSPACES.md](docs/WORKSPACES.md) for the design.

## Human-in-the-loop questions

An owning agent asks with `task ask ID "text"` (add `--blocking` to pause the
flow). Blocking questions put the task in `waiting_for_human`; the human
answers from the TUI (`w`) or with `task answer QUESTION_ID "text"`. The
answer can be revised until the agent runs `task acknowledge QUESTION_ID`,
after which it is frozen. A blocking question stops blocking when it is
answered, not when it is acknowledged.

## Dependencies

`task depend ID --on OTHER` records a prerequisite edge; cycles and
self-edges are rejected, and `task undepend` removes an edge. A task is
eligible only when all prerequisites are `done` — a cancelled prerequisite
keeps its dependents blocked. Dependencies of an actively claimed task cannot
change.

## Common CLI commands

```text
griglia                          open the TUI
griglia init [--name NAME]       initialize .griglia/ in this directory
griglia version                  version, commit, build date
griglia task add TITLE           create a task (backlog by default)
griglia task list | show ID      inspect tasks
griglia task edit ID ...         edit title/description/priority
griglia task ready|done|cancel   lifecycle transitions
griglia task claim|claim-next    acquire work (agent)
griglia task progress|release    report or hand back work (agent)
griglia task ask|acknowledge     question flow (agent)
griglia task answer|questions    question flow (human)
griglia task depend|undepend|dependencies
griglia workspace create ID      allocate the task's worktree (claim owner)
griglia workspace show ID | list inspect workspaces and derived usage
griglia workspace remove ID      prune a worktree (--delete-branch, --force)
```

Every command accepts `--json`. Example:

```bash
$ griglia task list --json
{"protocol_version":"1","ok":true,"data":{"tasks":[]},"error":null}
```

The JSON contract — envelope, DTOs, error codes, exit codes, and the
compatibility policy — is specified in [docs/PROTOCOL.md](docs/PROTOCOL.md).
Integration guidance for agents lives in
[docs/AGENT_INTEGRATION.md](docs/AGENT_INTEGRATION.md).

## TUI keyboard reference

| Key | Action |
|---|---|
| `j`/`k` or arrows | move selection |
| `enter` | task detail |
| `n` | new task |
| `e` | edit task |
| `a` | mark backlog task ready |
| `d` | complete ready task |
| `x` | cancel with optional reason |
| `w` | view and answer questions |
| `b` | inspect and edit dependencies |
| `r` | refresh immediately (external agent activity is also picked up automatically about once per second) |
| `?` | help |
| `q`/`esc` | back |
| `Q` / `ctrl+c` | quit |

Every state is distinguishable without color, and errors are recoverable in
place.

## Concurrency guarantees

Every mutation is one SQLite transaction that also appends an audit event —
partial writes are impossible. A partial unique index guarantees at most one
active claim per task; claim eligibility, cycle checks, and question state
changes run inside immediate write transactions, so concurrent CLI processes
serialize instead of corrupting state. Tasks carry a version for optimistic
conflict detection; conflicting writes fail with the stable `conflict` error
rather than last-writer-wins.

## Security and trust model

Griglia is a local coordination tool, not a security boundary:

- `.griglia/griglia.db` contains project coordination data in plain SQLite;
- there is no network listener and no remote authentication;
- agent identity (`--agent`/`--instance`) is asserted by the caller, not
  cryptographically verified;
- local filesystem permissions are the trust boundary — anyone who can open
  the database can act as any agent;
- claims are cooperative coordination locks, not security locks.

## Limitations

- One machine, one filesystem; no sync, no server, no multi-host support.
- Claims do not expire: a crashed agent's claim must be released manually
  (`task release` as the same identity).
- No plans/orchestration layer, no comments, and no full-text search yet.

## Development and testing

```bash
gofmt -l .
go vet ./...
go test ./...
go test -race ./...
CGO_ENABLED=0 go build ./cmd/griglia
```

Protocol conformance tests live in `internal/cli/protocol_test.go`; migration
and concurrency suites in `internal/sqlite/`. Benchmarks:
`go test ./internal/sqlite/ -bench . -run '^$'`.

## License

Griglia is released under the MIT License. See [LICENSE](LICENSE).

## Release status

Pre-release: the protocol is v1 and stable, the release pipeline is in place
(see [docs/RELEASING.md](docs/RELEASING.md)), and the first tagged release is
pending.
