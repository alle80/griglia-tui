# Integrating a coding agent with Griglia

Any coding agent that can run shell commands can coordinate work through
Griglia. There is no SDK and no vendor coupling: the contract is the CLI with
`--json`, specified in [PROTOCOL.md](PROTOCOL.md).

## Ground rules

- **Always pass `--json`.** Human output is for humans and may change; the
  JSON envelope is the stable contract (protocol v1).
- **Identity is required and asserted.** Every agent-side command takes
  `--agent NAME --instance ID`. Pick a stable agent name and a per-session
  instance (for example a session UUID). The same pair must be used for the
  whole life of a claim: ownership checks compare both fields exactly.
- **Check exit codes, then `error.code`.** 0 is success; 4 with
  `no_eligible_task` means "no work right now", which is a normal outcome,
  not a failure. 5 (`conflict`) means someone else got there first. 6
  (`busy`) is transient contention: retry after a short pause.
- **Never parse stderr.** It carries diagnostics only.

## The canonical loop

```text
claim-next → inspect task → progress … → ask (if a human decision is needed)
          → poll for the answer → acknowledge → continue → done
```

```bash
AGENT="codex"; INSTANCE="session-$$"
ID_FLAGS=(--agent "$AGENT" --instance "$INSTANCE")

# 1. Acquire the next eligible task (atomic: no race with other agents).
claim=$(griglia task claim-next "${ID_FLAGS[@]}" --json)
if [ $? -eq 4 ]; then
  echo "no eligible work — dependencies may still be blocking"; exit 0
fi
task_id=$(echo "$claim" | jq -r '.data.task.id')

# 2. Inspect what you claimed.
griglia task show "$task_id" --json | jq '.data.task | {title, description, priority}'

# 3. Report progress as you work (0-100; decreases are allowed).
griglia task progress "$task_id" 30 "${ID_FLAGS[@]}" --message "writing tests" --json

# 4. Blocked on a human decision? Ask a blocking question and stop working.
question=$(griglia task ask "$task_id" "Which auth provider?" --blocking "${ID_FLAGS[@]}" --json)
question_id=$(echo "$question" | jq -r '.data.question.id')

# 5. Poll for the answer (the task shows operational_state "waiting_for_human").
until griglia task questions "$task_id" --unacknowledged --json \
      | jq -e ".data.questions[] | select(.id == $question_id)" >/dev/null; do
  sleep 30
done
answer=$(griglia task questions "$task_id" --unacknowledged --json \
      | jq -r ".data.questions[] | select(.id == $question_id) | .answer")

# 6. Acknowledge to freeze the answer, then continue.
griglia task acknowledge "$question_id" "${ID_FLAGS[@]}" --json

# 7. Finish: completion atomically sets done and releases your claim.
griglia task done "$task_id" "${ID_FLAGS[@]}" --comment "implemented and tested" --json
```

If you cannot finish, hand the task back instead:

```bash
griglia task release "$task_id" "${ID_FLAGS[@]}" --reason "needs a different toolchain" --json
```

## Dependency-aware "no work" behavior

`claim-next` only ever returns tasks whose prerequisites are all `done`. If
everything ready is blocked or claimed you get exit 4 / `no_eligible_task` —
treat it as "come back later", not as an error. You can inspect why with
`griglia task list --json` (look at `operational_state`) and
`griglia task dependencies ID --json`.

## Semantics worth knowing

- **Ownership**: only the claiming `agent`+`instance` pair can progress, ask,
  release, or complete the task. Everyone else gets `conflict`.
- **No claim expiration**: claims survive crashes and never time out. If your
  agent restarts with the same identity, re-claiming its own task is
  idempotent; with a new instance ID you must release the old claim first
  (same identity) or ask the human to intervene.
- **Idempotency**: re-claiming an owned task, re-adding an existing
  dependency edge, and re-acknowledging an acknowledged question all succeed
  without side effects.
- **Blocking questions gate completion**: `done` and `release` are refused
  while an unanswered blocking question is open — the human must answer
  first. A question stops blocking when answered, before acknowledgement.
- **Answers freeze on acknowledge**: the human can revise an answer until you
  acknowledge it; acknowledge only after you have actually consumed it.
- **Optimistic versions**: every task mutation bumps `version`. A `conflict`
  means your view was stale — re-read with `task show` and retry
  deliberately.
- **Protocol version**: assert `protocol_version == "1"` and ignore unknown
  JSON keys; additive fields may appear at any time.
