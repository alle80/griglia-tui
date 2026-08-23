package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

const questionColumns = `id,task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at,answer,answered_at,acknowledged_at`

func (s *Store) AskQuestion(ctx context.Context, taskID int64, body string, blocking bool, identity domain.AgentIdentity, now time.Time) (domain.Question, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Question{}, err
	}
	defer tx.Rollback()
	task, err := taskFromTx(ctx, tx, taskID)
	if err != nil {
		return domain.Question{}, err
	}
	claim, err := claimFromTx(ctx, tx, taskID)
	if err != nil {
		return domain.Question{}, err
	}
	if task.Lifecycle != domain.LifecycleReady || !owns(claim, identity) {
		return domain.Question{}, fmt.Errorf("only the active owner of a ready task can ask questions: %w", domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `INSERT INTO questions(task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at) VALUES(?,?,?,?,?,?)`, taskID, body, blocking, identity.AgentName, identity.InstanceID, formatTime(now))
	if err != nil {
		return domain.Question{}, err
	}
	id, err := r.LastInsertId()
	if err != nil {
		return domain.Question{}, err
	}
	if err = insertActorEvent(ctx, tx, taskID, "question_asked", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "question_id": id, "blocking": blocking}, now); err != nil {
		return domain.Question{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Question{}, err
	}
	return domain.Question{ID: id, TaskID: taskID, Body: body, Blocking: blocking, AskedBy: identity, AskedAt: now}, nil
}

func (s *Store) AnswerQuestion(ctx context.Context, questionID int64, answer string, now time.Time) (domain.Question, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Question{}, err
	}
	defer tx.Rollback()
	q, err := questionFromTx(ctx, tx, questionID)
	if err != nil {
		return domain.Question{}, err
	}
	if q.Acknowledged() {
		return domain.Question{}, fmt.Errorf("answer is already acknowledged and cannot change: %w", domain.ErrConflict)
	}
	r, err := tx.ExecContext(ctx, `UPDATE questions SET answer=?,answered_at=? WHERE id=? AND acknowledged_at IS NULL`, answer, formatTime(now), questionID)
	if err != nil {
		return domain.Question{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.Question{}, fmt.Errorf("question changed since it was read: %w", domain.ErrConflict)
	}
	if err = insertEvent(ctx, tx, q.TaskID, "question_answered", map[string]any{"question_id": questionID}, now); err != nil {
		return domain.Question{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Question{}, err
	}
	q.Answer, q.AnsweredAt = &answer, &now
	return q, nil
}

func (s *Store) AcknowledgeQuestion(ctx context.Context, questionID int64, identity domain.AgentIdentity, now time.Time) (domain.Question, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return domain.Question{}, err
	}
	defer tx.Rollback()
	q, err := questionFromTx(ctx, tx, questionID)
	if err != nil {
		return domain.Question{}, err
	}
	if q.AskedBy != identity {
		return domain.Question{}, fmt.Errorf("only the asking agent can acknowledge the answer: %w", domain.ErrConflict)
	}
	if !q.Answered() {
		return domain.Question{}, fmt.Errorf("question has no answer to acknowledge: %w", domain.ErrConflict)
	}
	if q.Acknowledged() {
		return q, nil
	}
	r, err := tx.ExecContext(ctx, `UPDATE questions SET acknowledged_at=? WHERE id=? AND acknowledged_at IS NULL`, formatTime(now), questionID)
	if err != nil {
		return domain.Question{}, err
	}
	if n, _ := r.RowsAffected(); n != 1 {
		return domain.Question{}, fmt.Errorf("question changed since it was read: %w", domain.ErrConflict)
	}
	if err = insertActorEvent(ctx, tx, q.TaskID, "question_acknowledged", "agent", identity.AgentName, map[string]any{"instance_id": identity.InstanceID, "question_id": questionID}, now); err != nil {
		return domain.Question{}, err
	}
	if err = tx.Commit(); err != nil {
		return domain.Question{}, err
	}
	q.AcknowledgedAt = &now
	return q, nil
}

func (s *Store) ListQuestions(ctx context.Context, taskID int64, filter domain.QuestionFilter) ([]domain.Question, error) {
	var exists int
	if err := s.db.QueryRowContext(ctx, `SELECT 1 FROM tasks WHERE id=?`, taskID).Scan(&exists); errors.Is(err, sql.ErrNoRows) {
		return nil, domain.ErrNotFound
	} else if err != nil {
		return nil, err
	}
	query := `SELECT ` + questionColumns + ` FROM questions WHERE task_id=?`
	switch filter {
	case domain.QuestionsUnanswered:
		query += ` AND answered_at IS NULL`
	case domain.QuestionsUnacknowledged:
		// Unacknowledged means answered but not yet consumed by the asking
		// agent; questions still awaiting an answer are not included.
		query += ` AND answered_at IS NOT NULL AND acknowledged_at IS NULL`
	}
	rows, err := s.db.QueryContext(ctx, query+` ORDER BY id ASC`, taskID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	questions := make([]domain.Question, 0)
	for rows.Next() {
		q, scanErr := scanQuestion(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		questions = append(questions, q)
	}
	return questions, rows.Err()
}

func questionFromTx(ctx context.Context, tx *sql.Tx, id int64) (domain.Question, error) {
	q, err := scanQuestion(tx.QueryRowContext(ctx, `SELECT `+questionColumns+` FROM questions WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return domain.Question{}, fmt.Errorf("question not found: %w", domain.ErrNotFound)
	}
	return q, err
}

func scanQuestion(row scanner) (domain.Question, error) {
	var q domain.Question
	var asked string
	var answer, answered, acknowledged sql.NullString
	if err := row.Scan(&q.ID, &q.TaskID, &q.Body, &q.Blocking, &q.AskedBy.AgentName, &q.AskedBy.InstanceID, &asked, &answer, &answered, &acknowledged); err != nil {
		return q, err
	}
	var err error
	if q.AskedAt, err = parseTime(asked); err != nil {
		return q, err
	}
	if answer.Valid {
		q.Answer = &answer.String
	}
	if answered.Valid {
		v, parseErr := parseTime(answered.String)
		if parseErr != nil {
			return q, parseErr
		}
		q.AnsweredAt = &v
	}
	if acknowledged.Valid {
		v, parseErr := parseTime(acknowledged.String)
		if parseErr != nil {
			return q, parseErr
		}
		q.AcknowledgedAt = &v
	}
	return q, nil
}

type queryRower interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func hasUnansweredBlocking(ctx context.Context, db queryRower, taskID int64) (bool, error) {
	var pending bool
	err := db.QueryRowContext(ctx, `SELECT EXISTS(SELECT 1 FROM questions WHERE task_id=? AND blocking=1 AND answered_at IS NULL)`, taskID).Scan(&pending)
	return pending, err
}
