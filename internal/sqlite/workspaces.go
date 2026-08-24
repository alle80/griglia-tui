package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
	sqlite3 "modernc.org/sqlite"
)

// Workspace persistence models the lifecycle of the Git workspace resource
// only (allocating → ready | failed, then removed). Ownership and usage are
// never stored here: they are derived from the live claims table, so claim
// transitions (claim, release, done, cancel) never touch workspace rows.

const workspaceColumns = `id,task_id,state,path,branch,base_commit,created_by_agent,created_by_instance,created_at,updated_at,removed_at,error`

// ReserveWorkspace inserts the workspace row in state allocating — the
// transactional first phase of allocation; Git side effects happen outside,
// after commit. Only the active claim owner of a ready task may reserve
// (an ownership check against the claims table, not a workspace-row fact).
// The partial unique indexes make a duplicate live workspace, path, or
// branch a stable conflict under any interleaving.
func (s *Store) ReserveWorkspace(ctx context.Context, taskID int64, path, branch, baseCommit string, identity domain.AgentIdentity, now time.Time) (domain.Workspace, error) {
	if err := domain.ValidateWorkspacePath(path); err != nil {
		return domain.Workspace{}, fmt.Errorf("workspace path is required: %w", err)
	}
	if err := domain.ValidateWorkspaceBranch(branch); err != nil {
		return domain.Workspace{}, fmt.Errorf("workspace branch is required: %w", err)
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	claim, err := claimFromTx(ctx, tx, taskID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if task.Lifecycle != domain.LifecycleReady || !owns(claim, identity) {
		return domain.Workspace{}, fmt.Errorf("only the active owner of a ready task can reserve a workspace: %w", domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO workspaces(task_id,state,path,branch,base_commit,created_by_agent,created_by_instance,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?)`, taskID, domain.WorkspaceAllocating, path, branch, baseCommit, identity.AgentName, identity.InstanceID, formatTime(now), formatTime(now))
	if err != nil {
		if isUniqueConstraint(err) {
			return domain.Workspace{}, fmt.Errorf("a live workspace already exists for this task, path, or branch: %w", domain.ErrConflict)
		}
		return domain.Workspace{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return domain.Workspace{}, err
	}
	if err = insertActorEvent(ctx, tx, taskID, "workspace_allocating", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "workspace_id": id, "path": path, "branch": branch, "base_commit": baseCommit}, now); err != nil {
		return domain.Workspace{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	return domain.Workspace{ID: id, TaskID: taskID, State: domain.WorkspaceAllocating, Path: path, Branch: branch, BaseCommit: baseCommit, CreatedBy: identity, CreatedAt: now, UpdatedAt: now}, nil
}

// MarkWorkspaceReady records that the Git side effects of an allocation
// succeeded: allocating → ready.
func (s *Store) MarkWorkspaceReady(ctx context.Context, workspaceID int64, now time.Time) (domain.Workspace, error) {
	return s.finishAllocation(ctx, workspaceID, domain.WorkspaceReady, "", "workspace_ready", now)
}

// MarkWorkspaceFailed records that the Git side effects of an allocation
// failed: allocating → failed, keeping the error for diagnosis. Failed rows
// leave the partial unique indexes, so the reservation can be retried.
func (s *Store) MarkWorkspaceFailed(ctx context.Context, workspaceID int64, message string, now time.Time) (domain.Workspace, error) {
	return s.finishAllocation(ctx, workspaceID, domain.WorkspaceFailed, message, "workspace_failed", now)
}

func (s *Store) finishAllocation(ctx context.Context, workspaceID int64, state domain.WorkspaceState, message, eventKind string, now time.Time) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	w, err := workspaceFromTx(ctx, tx, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if w.State != domain.WorkspaceAllocating {
		return domain.Workspace{}, fmt.Errorf("only an allocating workspace can become %s: %w", state, domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `UPDATE workspaces SET state=?,error=?,updated_at=? WHERE id=? AND state=?`, state, message, formatTime(now), workspaceID, domain.WorkspaceAllocating)
	if err != nil {
		return domain.Workspace{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.Workspace{}, fmt.Errorf("workspace changed since it was read: %w", domain.ErrConflict)
	}
	payload := map[string]any{"instance_id": w.CreatedBy.InstanceID, "workspace_id": workspaceID}
	if message != "" {
		payload["error"] = message
	}
	if err = insertActorEvent(ctx, tx, w.TaskID, eventKind, "agent", w.CreatedBy.AgentName, payload, now); err != nil {
		return domain.Workspace{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	w.State, w.Error, w.UpdatedAt = state, message, now
	return w, nil
}

// RemoveWorkspace marks the row removed (terminal), freeing the task, path,
// and branch for a later allocation. Any non-removed state may be removed:
// ready and failed per the designed lifecycle, allocating as the documented
// repair for allocations stuck between phases. Removing an already removed
// workspace is idempotent and writes no event.
func (s *Store) RemoveWorkspace(ctx context.Context, workspaceID int64, now time.Time) (domain.Workspace, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Workspace{}, err
	}
	defer tx.Rollback()
	w, err := workspaceFromTx(ctx, tx, workspaceID)
	if err != nil {
		return domain.Workspace{}, err
	}
	if w.State == domain.WorkspaceRemoved {
		return w, nil
	}
	r, err := tx.ExecContext(ctx, `UPDATE workspaces SET state=?,removed_at=?,updated_at=? WHERE id=? AND state<>?`, domain.WorkspaceRemoved, formatTime(now), formatTime(now), workspaceID, domain.WorkspaceRemoved)
	if err != nil {
		return domain.Workspace{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.Workspace{}, fmt.Errorf("workspace changed since it was read: %w", domain.ErrConflict)
	}
	if err = insertEvent(ctx, tx, w.TaskID, "workspace_removed", map[string]any{"workspace_id": workspaceID}, now); err != nil {
		return domain.Workspace{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Workspace{}, err
	}
	w.State, w.RemovedAt, w.UpdatedAt = domain.WorkspaceRemoved, &now, now
	return w, nil
}

// LiveWorkspaceForTask returns the task's allocating or ready workspace, or
// nil when the task has none — the read future phases use for idempotent
// reuse and removal by task id.
func (s *Store) LiveWorkspaceForTask(ctx context.Context, taskID int64) (*domain.Workspace, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	w, err := scanWorkspace(s.db.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE task_id=? AND state IN (?,?)`, taskID, domain.WorkspaceAllocating, domain.WorkspaceReady))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &w, nil
}

// WorkspacesForTask returns every workspace row ever recorded for the task —
// failed and removed history included — in creation order. Allocation retries
// use it to recognize leftovers of the task's own earlier attempts, and
// removal uses it to address failed rows that are no longer live.
func (s *Store) WorkspacesForTask(ctx context.Context, taskID int64) ([]domain.Workspace, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE task_id=? ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0)
	for rows.Next() {
		w, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

// ListWorkspaces returns every persisted workspace row, including failed and
// removed history, in creation order.
func (s *Store) ListWorkspaces(ctx context.Context) ([]domain.Workspace, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces ORDER BY id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	workspaces := make([]domain.Workspace, 0)
	for rows.Next() {
		w, scanErr := scanWorkspace(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		workspaces = append(workspaces, w)
	}
	return workspaces, rows.Err()
}

// The workspace read model serves `workspace show`/`workspace list`: each
// task's current workspace joined with the task's active claim in one query,
// so derived usage never needs a per-row claim lookup. The current workspace
// is the task's most recent row unless that row is removed — the live
// (allocating or ready) row when one exists, else the latest failed row, whose
// recorded error is what makes a failed allocation diagnosable. Removed rows
// are history and never surface here. A live row is always the task's most
// recent row: reservations only insert when no live row exists, and states
// otherwise change in place.
const workspaceViewQuery = `SELECT w.id,w.task_id,w.state,w.path,w.branch,w.base_commit,w.created_by_agent,w.created_by_instance,w.created_at,w.updated_at,w.removed_at,w.error,
claims.id,claims.task_id,claims.agent_name,claims.instance_id,claims.claimed_at,claims.released_at
FROM workspaces w
LEFT JOIN claims ON claims.task_id=w.task_id AND claims.released_at IS NULL
WHERE w.id=(SELECT MAX(a.id) FROM workspaces a WHERE a.task_id=w.task_id) AND w.state<>'removed'`

// WorkspaceViewForTask returns the task's current workspace with its derived
// claim, or ErrNotFound when the task does not exist or has no current
// workspace.
func (s *Store) WorkspaceViewForTask(ctx context.Context, taskID int64) (domain.WorkspaceView, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return domain.WorkspaceView{}, domain.ErrNotFound
	} else if err != nil {
		return domain.WorkspaceView{}, err
	}
	view, err := scanWorkspaceView(s.db.QueryRowContext(ctx, workspaceViewQuery+` AND w.task_id=?`, taskID))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.WorkspaceView{}, fmt.Errorf("task has no workspace: %w", domain.ErrNotFound)
	}
	return view, err
}

// ListWorkspaceViews returns the current workspace of every task that has
// one, with derived claims, ordered by task id.
func (s *Store) ListWorkspaceViews(ctx context.Context) ([]domain.WorkspaceView, error) {
	rows, err := s.db.QueryContext(ctx, workspaceViewQuery+` ORDER BY w.task_id ASC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	views := make([]domain.WorkspaceView, 0)
	for rows.Next() {
		view, scanErr := scanWorkspaceView(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		views = append(views, view)
	}
	return views, rows.Err()
}

func scanWorkspaceView(row scanner) (domain.WorkspaceView, error) {
	var view domain.WorkspaceView
	var created, updated string
	var removed sql.NullString
	var claimID, claimTaskID sql.NullInt64
	var claimAgent, claimInstance, claimedAt, releasedAt sql.NullString
	if err := row.Scan(&view.ID, &view.TaskID, &view.State, &view.Path, &view.Branch, &view.BaseCommit, &view.CreatedBy.AgentName, &view.CreatedBy.InstanceID, &created, &updated, &removed, &view.Error,
		&claimID, &claimTaskID, &claimAgent, &claimInstance, &claimedAt, &releasedAt); err != nil {
		return view, err
	}
	var err error
	if view.CreatedAt, err = parseTime(created); err != nil {
		return view, err
	}
	if view.UpdatedAt, err = parseTime(updated); err != nil {
		return view, err
	}
	if removed.Valid {
		v, parseErr := parseTime(removed.String)
		if parseErr != nil {
			return view, parseErr
		}
		view.RemovedAt = &v
	}
	if claimID.Valid {
		claim := &domain.Claim{ID: claimID.Int64, TaskID: claimTaskID.Int64, AgentName: claimAgent.String, InstanceID: claimInstance.String}
		if claim.ClaimedAt, err = parseTime(claimedAt.String); err != nil {
			return view, err
		}
		view.ActiveClaim = claim
	}
	return view, nil
}

func workspaceFromTx(ctx context.Context, tx *sql.Tx, id int64) (domain.Workspace, error) {
	w, err := scanWorkspace(tx.QueryRowContext(ctx, `SELECT `+workspaceColumns+` FROM workspaces WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Workspace{}, fmt.Errorf("workspace not found: %w", domain.ErrNotFound)
	}
	return w, err
}

func scanWorkspace(row scanner) (domain.Workspace, error) {
	var w domain.Workspace
	var created, updated string
	var removed sql.NullString
	if err := row.Scan(&w.ID, &w.TaskID, &w.State, &w.Path, &w.Branch, &w.BaseCommit, &w.CreatedBy.AgentName, &w.CreatedBy.InstanceID, &created, &updated, &removed, &w.Error); err != nil {
		return w, err
	}
	var err error
	if w.CreatedAt, err = parseTime(created); err != nil {
		return w, err
	}
	if w.UpdatedAt, err = parseTime(updated); err != nil {
		return w, err
	}
	if removed.Valid {
		v, parseErr := parseTime(removed.String)
		if parseErr != nil {
			return w, parseErr
		}
		w.RemovedAt = &v
	}
	return w, nil
}

// isUniqueConstraint reports whether err is a SQLITE_CONSTRAINT violation —
// how the partial unique indexes surface a lost allocation race.
func isUniqueConstraint(err error) bool {
	var se *sqlite3.Error
	if !errors.As(err, &se) {
		return false
	}
	return se.Code()&0xff == 19 // SQLITE_CONSTRAINT
}
