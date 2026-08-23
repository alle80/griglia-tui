# Griglia JSON protocol, version 1

Every command run with `--json` emits **exactly one JSON document on stdout**
and nothing else there. stderr is reserved for opt-in diagnostics and never
carries protocol data. This contract is pinned by
`internal/cli/protocol_test.go`; keep this document and that suite in sync.

## Envelope

```json
{"protocol_version":"1","ok":true,"data":{...},"error":null}
```

| Field | Type | Notes |
|---|---|---|
| `protocol_version` | string | always `"1"` for this protocol |
| `ok` | boolean | `true` on success |
| `data` | object or null | command payload; `null` on error |
| `error` | object or null | `{"code","message"}`; `null` on success |

Errors use the same envelope:

```json
{"protocol_version":"1","ok":false,"data":null,"error":{"code":"conflict","message":"task is already claimed: conflict"}}
```

`error.code` is stable and machine-matchable; `error.message` is human
diagnostic text and may change between releases.

## Error codes and exit codes

| Exit | `error.code` | Meaning |
|---|---|---|
| 0 | — | success |
| 1 | `internal_error` | unexpected failure (detail on stderr) |
| 2 | `invalid_input` | usage, argument, or validation error |
| 3 | `project_not_initialized` | no `.griglia/griglia.db` found |
| 4 | `not_found` | task or question does not exist |
| 4 | `no_eligible_task` | `claim-next` found no available work |
| 5 | `conflict` | claim/ownership/lifecycle/version conflict |
| 6 | `busy` | SQLite write contention outlived the busy timeout; retry |

## Conventions

- IDs are JSON numbers; enums are lowercase strings.
- Timestamps are UTC RFC 3339 with microsecond precision:
  `2026-08-23T14:11:40.680742Z`.
- Absent values are `null`, never omitted keys and never empty-string
  stand-ins for time fields.
- Lists are always arrays (empty arrays when there are no items).

## Task DTO

```json
{
  "id": 1,
  "uid": "42b12e9c-8e8f-4fd8-8ca7-2d619c4ca9f2",
  "title": "Build feature",
  "description": "",
  "lifecycle": "ready",
  "operational_state": "working",
  "priority": "high",
  "progress": 40,
  "phase": "implementing",
  "completion_summary": "",
  "created_at": "2026-08-23T14:11:38.000000Z",
  "updated_at": "2026-08-23T14:11:40.680742Z",
  "completed_at": null,
  "cancelled_at": null,
  "version": 2,
  "active_claim": {"agent_name":"codex","instance_id":"session-1","claimed_at":"2026-08-23T14:11:40.680742Z"}
}
```

- `lifecycle` ∈ `backlog | ready | done | cancelled` (persisted intent).
- `operational_state` is derived and only non-null for `ready` tasks:
  `available | working | waiting_for_human | blocked`, with precedence
  blocked > waiting_for_human > working > available.
- `priority` ∈ `low | normal | high | urgent`.
- `version` increments on every mutation (optimistic concurrency).
- `active_claim` is `null` when unclaimed.

## Claim DTO

```json
{"agent_name":"codex","instance_id":"session-1","claimed_at":"2026-08-23T14:11:40.680742Z"}
```

## Question DTO

```json
{
  "id": 1,
  "task_id": 1,
  "body": "Deploy target?",
  "blocking": true,
  "asked_by": {"agent_name":"codex","instance_id":"session-1"},
  "asked_at": "2026-08-23T14:11:41.000000Z",
  "answer": "staging",
  "answered_at": "2026-08-23T14:12:00.000000Z",
  "acknowledged_at": null
}
```

`answer`/`answered_at` are set together; `acknowledged_at` is only ever set
after an answer exists and freezes it.

## Dependency DTO

```json
{"task_id":2,"depends_on_task_id":1,"title":"Build feature","lifecycle":"done","satisfied":true}
```

`satisfied` is true only when the prerequisite's lifecycle is `done`;
cancelled prerequisites stay unsatisfied. `task undepend` returns a reduced
edge object: `{"task_id","depends_on_task_id"}`.

## Command payloads

| Command | `data` shape |
|---|---|
| `version` | `{"version","commit","build_date"}` |
| `init` | `{"project":{"name","database"}}` |
| `task add` / `edit` / `ready` / `done` / `cancel` / `show` | `{"task": Task}` |
| `task list` | `{"tasks": [Task]}` |
| `task claim` / `claim-next` / `progress` / `release` / `done --agent` | `{"task": Task, "claim": Claim or null}` |
| `task ask` / `answer` / `acknowledge` | `{"question": Question}` |
| `task questions` | `{"questions": [Question]}` |
| `task depend` | `{"dependency": Dependency}` |
| `task undepend` | `{"dependency":{"task_id","depends_on_task_id"}}` |
| `task dependencies` | `{"dependencies": [Dependency]}` |

Commands intended for agents (identity required): `claim`, `claim-next`,
`progress`, `release`, `ask`, `acknowledge`, and `done` with `--agent`.
Commands intended for humans: everything else; `answer` is explicitly the
human side of the question flow.

## Compatibility policy

- **Additive changes are allowed within protocol v1**: new fields and new
  `data` keys may appear at any time. Parse tolerantly; ignore unknown keys.
- **Breaking changes require a protocol version bump**: removing or renaming
  fields, changing types or semantics, changing error codes or exit codes.
- `protocol_version` versions only this machine-readable contract. It is
  independent of the binary version and of the SQLite schema version.
