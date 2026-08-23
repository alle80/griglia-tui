CREATE TABLE questions(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE CASCADE,
  body TEXT NOT NULL,
  blocking INTEGER NOT NULL CHECK(blocking IN (0,1)),
  asked_by_agent_name TEXT NOT NULL,
  asked_by_instance_id TEXT NOT NULL,
  asked_at TEXT NOT NULL,
  answer TEXT,
  answered_at TEXT,
  acknowledged_at TEXT,
  CHECK((answer IS NULL) = (answered_at IS NULL)),
  CHECK(acknowledged_at IS NULL OR answered_at IS NOT NULL)
);

CREATE INDEX questions_task_history
  ON questions(task_id, id);

CREATE INDEX questions_unanswered_blocking
  ON questions(task_id) WHERE blocking = 1 AND answered_at IS NULL;
