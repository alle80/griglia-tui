CREATE TABLE workspaces(
  id INTEGER PRIMARY KEY AUTOINCREMENT,
  task_id INTEGER NOT NULL REFERENCES tasks(id) ON DELETE RESTRICT,
  state TEXT NOT NULL CHECK(state IN ('allocating','ready','failed','removed')),
  path TEXT NOT NULL,
  branch TEXT NOT NULL,
  base_commit TEXT NOT NULL DEFAULT '',
  created_by_agent TEXT NOT NULL,
  created_by_instance TEXT NOT NULL,
  created_at TEXT NOT NULL,
  updated_at TEXT NOT NULL,
  removed_at TEXT,
  error TEXT NOT NULL DEFAULT ''
);

CREATE UNIQUE INDEX one_live_workspace_per_task
  ON workspaces(task_id) WHERE state IN ('allocating','ready');

CREATE UNIQUE INDEX one_live_workspace_per_path
  ON workspaces(path) WHERE state IN ('allocating','ready');

CREATE UNIQUE INDEX one_live_workspace_per_branch
  ON workspaces(branch) WHERE state IN ('allocating','ready');
