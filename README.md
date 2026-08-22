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
./griglia
```

In the TUI, use `j`/`k` or the arrow keys to move, `enter` for task detail,
`n` to create a task, `?` for help, and `Q` to quit.

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
