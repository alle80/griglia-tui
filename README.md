# Griglia TUI

Griglia TUI is a local, terminal-first todo list with both an interactive,
keyboard-first interface and deterministic non-interactive commands.

## Build and use

Go 1.25 or newer is required.

```bash
go build -o griglia ./cmd/griglia
./griglia init
./griglia task add "First task"
./griglia task list
./griglia task show 1
./griglia task edit 1 --title "Updated title" --description "Details" --priority high
./griglia task ready 1
./griglia task done 1
./griglia task cancel 2 --reason "Superseded"
./griglia task claim-next --agent codex --instance session-123 --json
./griglia task claim 1 --agent codex --instance session-123 --json
./griglia task progress 1 40 --agent codex --instance session-123 --message "Implementing"
./griglia task release 1 --agent codex --instance session-123 --json
./griglia task done 1 --agent codex --instance session-123 --comment "Implemented and tested" --json
./griglia
```

In the TUI, use `j`/`k` or the arrow keys to move, `enter` for task detail,
`n` to create, `e` to edit, `a` to mark ready, `d` to complete, and `x` to
cancel with an optional reason. Press `?` for help and `Q` to quit.
The list shows ready tasks as `available` or `working`, including the active
agent, progress, and phase. Press `r` to reload changes made by external agents.

Agent identity is an explicit name/instance pair. Claims do not expire: only
the owner may update progress, release, or complete a claimed task. Repeating a
claim by the same owner is idempotent. Human edit, completion, and cancellation
conflict while a claim is active. `claim-next --json` returns the stable
`no_eligible_task` error with exit code 4 when no ready unclaimed task exists.

Griglia stores local state in `.griglia/griglia.db` and discovers it by walking
upward from the current directory. Use `--project PATH` or `GRIGLIA_PROJECT` to
select a project explicitly.

Machine-readable commands use protocol version 1:

```bash
./griglia task list --json
```

```json
{"protocol_version":"1","ok":true,"data":{"tasks":[]},"error":null}
```

Run the checks with `go test ./...` and `go vet ./...`.
