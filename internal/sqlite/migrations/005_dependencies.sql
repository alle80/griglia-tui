CREATE TABLE dependencies(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  depends_on_task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  created_at TEXT NOT NULL,
  CHECK(task_id <> depends_on_task_id)
);

CREATE UNIQUE INDEX one_edge_per_pair
  ON dependencies(task_id, depends_on_task_id);

CREATE INDEX dependencies_reverse
  ON dependencies(depends_on_task_id);
