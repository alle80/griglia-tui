# Griglia TUI

Griglia TUI is a local, terminal-first todo list. Milestone 1 provides the
non-interactive project and task commands; the TUI arrives in a later milestone.

## Build and use

Go 1.24 or newer is required.

```bash
go build -o griglia ./cmd/griglia
./griglia init
./griglia task add "First task"
./griglia task list
./griglia task show 1
```

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
