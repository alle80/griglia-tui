CREATE TABLE claims(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  agent_name TEXT NOT NULL,
  instance_id TEXT NOT NULL,
  claimed_at TEXT NOT NULL,
  last_activity_at TEXT NOT NULL,
  released_at TEXT,
  release_reason TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX one_active_claim_per_task
  ON claims(task_id) WHERE released_at IS NULL;

CREATE INDEX claims_active_owners
  ON claims(agent_name, instance_id) WHERE released_at IS NULL;
