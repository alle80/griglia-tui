package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

// AddDependency records that task depends on prerequisite. The cycle check
// and the insert share one immediate write transaction, so concurrent
// writers serialize and can never commit opposite edges. Re-adding an
// existing edge is idempotent and writes no event.
func (s *Store) AddDependency(ctx context.Context, taskID, dependsOnTaskID int64, now time.Time) (domain.DependencyView, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.DependencyView{}, err
	}
	defer tx.Rollback()
	if taskID == dependsOnTaskID {
		return domain.DependencyView{}, fmt.Errorf("a task cannot depend on itself: %w", domain.ErrInvalid)
	}
	if _, err = taskFromTx(ctx, tx, taskID); err != nil {
		return domain.DependencyView{}, err
	}
	prerequisite, err := taskFromTx(ctx, tx, dependsOnTaskID)
	if err != nil {
		if errors.Is(err, domain.ErrNotFound) {
			return domain.DependencyView{}, fmt.Errorf("prerequisite task not found: %w", domain.ErrNotFound)
		}
		return domain.DependencyView{}, err
	}
	claim, err := claimFromTx(ctx, tx, taskID)
	if err != nil {
		return domain.DependencyView{}, err
	}
	if claim != nil {
		return domain.DependencyView{}, fmt.Errorf("dependencies of an actively claimed task cannot change: %w", domain.ErrConflict)
	}
	view := domain.DependencyView{TaskID: taskID, DependsOnTaskID: dependsOnTaskID, Title: prerequisite.Title, Lifecycle: prerequisite.Lifecycle}
	var created string
	scanErr := tx.QueryRowContext(ctx, `SELECT created_at FROM dependencies WHERE task_id=? AND depends_on_task_id=?`, taskID, dependsOnTaskID).Scan(&created)
	if scanErr == nil {
		if view.CreatedAt, err = parseTime(created); err != nil {
			return domain.DependencyView{}, err
		}
		return view, nil
	}
	if !errors.Is(scanErr, sql.ErrNoRows) {
		return domain.DependencyView{}, scanErr
	}
	var cycle int
	err = tx.QueryRowContext(ctx, `WITH RECURSIVE reachable(id) AS (
SELECT depends_on_task_id FROM dependencies WHERE task_id=?
UNION
SELECT d.depends_on_task_id FROM dependencies d JOIN reachable r ON d.task_id=r.id
) SELECT 1 FROM reachable WHERE id=? LIMIT 1`, dependsOnTaskID, taskID).Scan(&cycle)
	if err == nil {
		return domain.DependencyView{}, fmt.Errorf("dependency would create a cycle: %w", domain.ErrConflict)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return domain.DependencyView{}, err
	}
	if _, err = tx.ExecContext(ctx, `INSERT INTO dependencies(task_id,depends_on_task_id,created_at) VALUES(?,?,?)`, taskID, dependsOnTaskID, formatTime(now)); err != nil {
		return domain.DependencyView{}, err
	}
	if err = insertEvent(ctx, tx, taskID, "dependency_added", map[string]any{"depends_on_task_id": dependsOnTaskID}, now); err != nil {
		return domain.DependencyView{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.DependencyView{}, err
	}
	view.CreatedAt = now
	return view, nil
}

// RemoveDependency deletes exactly one edge. Removing an edge that does not
// exist is idempotent and writes no event.
func (s *Store) RemoveDependency(ctx context.Context, taskID, dependsOnTaskID int64, now time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = taskFromTx(ctx, tx, taskID); err != nil {
		return err
	}
	claim, err := claimFromTx(ctx, tx, taskID)
	if err != nil {
		return err
	}
	if claim != nil {
		return fmt.Errorf("dependencies of an actively claimed task cannot change: %w", domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `DELETE FROM dependencies WHERE task_id=? AND depends_on_task_id=?`, taskID, dependsOnTaskID)
	if err != nil {
		return err
	}
	if n, _ := r.RowsAffected(); n == 0 {
		return nil
	}
	if err = insertEvent(ctx, tx, taskID, "dependency_removed", map[string]any{"depends_on_task_id": dependsOnTaskID}, now); err != nil {
		return err
	}
	return tx.Commit()
}

func (s *Store) ListDependencies(ctx context.Context, taskID int64) ([]domain.DependencyView, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	rows, err := s.db.QueryContext(ctx, `SELECT d.task_id,d.depends_on_task_id,p.title,p.lifecycle,d.created_at
FROM dependencies d JOIN tasks p ON p.id=d.depends_on_task_id
WHERE d.task_id=? ORDER BY d.depends_on_task_id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	dependencies := make([]domain.DependencyView, 0)
	for rows.Next() {
		var d domain.DependencyView
		var created string
		if err = rows.Scan(&d.TaskID, &d.DependsOnTaskID, &d.Title, &d.Lifecycle, &created); err != nil {
			return nil, err
		}
		if d.CreatedAt, err = parseTime(created); err != nil {
			return nil, err
		}
		dependencies = append(dependencies, d)
	}
	return dependencies, rows.Err()
}

func hasUnsatisfiedDependencies(ctx context.Context, db queryRower, taskID int64) (bool, error) {
	var blocked bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM dependencies JOIN tasks prerequisite ON prerequisite.id=dependencies.depends_on_task_id WHERE dependencies.task_id=? AND prerequisite.lifecycle<>'done')`, taskID).Scan(&blocked)
	return blocked, err
}
