package sqlite

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func TestAskQuestionOwnershipAndAtomicity(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "ask", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	other := domain.AgentIdentity{AgentName: "claude", InstanceID: "two"}
	now := time.Now().UTC()

	if _, err := s.AskQuestion(ctx, task.ID, "unclaimed", true, owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("unclaimed ask err=%v", err)
	}
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AskQuestion(ctx, task.ID, "not yours", true, other, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("non-owner ask err=%v", err)
	}
	if _, err := s.AskQuestion(ctx, 999, "ghost", true, owner, now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown task err=%v", err)
	}

	if _, err := s.db.Exec(`CREATE TRIGGER reject_ask BEFORE INSERT ON events WHEN NEW.kind='question_asked' BEGIN SELECT RAISE(ABORT, 'reject ask'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AskQuestion(ctx, task.ID, "atomic?", true, owner, now); err == nil {
		t.Fatal("expected audit failure")
	}
	var count int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM questions`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("questions=%d err=%v", count, err)
	}
	if _, err := s.db.Exec(`DROP TRIGGER reject_ask`); err != nil {
		t.Fatal(err)
	}

	q, err := s.AskQuestion(ctx, task.ID, "Should malformed nodes be preserved?", true, owner, now.Add(time.Second))
	if err != nil || q.ID == 0 || q.TaskID != task.ID || !q.Blocking || q.AskedBy != owner || q.Answered() || q.Acknowledged() {
		t.Fatalf("question=%+v err=%v", q, err)
	}
	var kind, actorKind, actorName, payload string
	if err = s.db.QueryRow(`SELECT kind,actor_kind,actor_name,payload_json FROM events WHERE task_id=? AND kind='question_asked'`, task.ID).Scan(&kind, &actorKind, &actorName, &payload); err != nil {
		t.Fatal(err)
	}
	if actorKind != "agent" || actorName != "codex" || !strings.Contains(payload, `"blocking":true`) || !strings.Contains(payload, fmt.Sprintf(`"question_id":%d`, q.ID)) {
		t.Fatalf("event actor=%s/%s payload=%s", actorKind, actorName, payload)
	}
}

func TestWaitingForHumanStateTransitions(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "waiting", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}

	state := func() domain.OperationalState {
		t.Helper()
		view, err := s.GetTask(ctx, task.ID)
		if err != nil || view.OperationalState == nil {
			t.Fatalf("view=%+v err=%v", view, err)
		}
		return *view.OperationalState
	}

	// A non-blocking question never leaves working.
	if _, err := s.AskQuestion(ctx, task.ID, "informational", false, owner, now); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != domain.OperationalWorking {
		t.Fatalf("state after non-blocking=%s", got)
	}

	first, err := s.AskQuestion(ctx, task.ID, "first blocking", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	second, err := s.AskQuestion(ctx, task.ID, "second blocking", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if got := state(); got != domain.OperationalWaitingForHuman {
		t.Fatalf("state with blocking=%s", got)
	}

	if _, err = s.AnswerQuestion(ctx, first.ID, "answer one", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if got := state(); got != domain.OperationalWaitingForHuman {
		t.Fatalf("state with one of two answered=%s", got)
	}
	if _, err = s.AnswerQuestion(ctx, second.ID, "answer two", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	// Unacknowledged answers no longer block: the task derives working again.
	if got := state(); got != domain.OperationalWorking {
		t.Fatalf("state with all answered=%s", got)
	}

	views, err := s.ListTasks(ctx)
	if err != nil || len(views) != 1 || *views[0].OperationalState != domain.OperationalWorking {
		t.Fatalf("list views=%+v err=%v", views, err)
	}
}

func TestListTasksDerivesWaitingWithoutPerTaskQueries(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	waiting := createReadyTask(t, s, "waiting", domain.PriorityUrgent)
	working := createReadyTask(t, s, "working", domain.PriorityHigh)
	available := createReadyTask(t, s, "available", domain.PriorityLow)
	for _, id := range []int64{waiting.ID, working.ID} {
		if _, err := s.ClaimTask(ctx, id, owner, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.AskQuestion(ctx, waiting.ID, "blocking", true, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.AskQuestion(ctx, working.ID, "info only", false, owner, now); err != nil {
		t.Fatal(err)
	}
	views, err := s.ListTasks(ctx)
	if err != nil || len(views) != 3 {
		t.Fatalf("views=%d err=%v", len(views), err)
	}
	want := map[int64]domain.OperationalState{waiting.ID: domain.OperationalWaitingForHuman, working.ID: domain.OperationalWorking, available.ID: domain.OperationalAvailable}
	for _, view := range views {
		if view.OperationalState == nil || *view.OperationalState != want[view.ID] {
			t.Fatalf("task %d state=%v want=%s", view.ID, view.OperationalState, want[view.ID])
		}
	}
}

func TestAnswerLifecycleAndAcknowledgement(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "answer", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	other := domain.AgentIdentity{AgentName: "claude", InstanceID: "two"}
	now := time.Now().UTC()
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	q, err := s.AskQuestion(ctx, task.ID, "blocking", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}

	if _, err = s.AnswerQuestion(ctx, 999, "ghost", now); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown question err=%v", err)
	}
	if _, err = s.AcknowledgeQuestion(ctx, q.ID, owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("ack before answer err=%v", err)
	}

	// Human answer + event are atomic.
	if _, err = s.db.Exec(`CREATE TRIGGER reject_answer BEFORE INSERT ON events WHEN NEW.kind='question_answered' BEGIN SELECT RAISE(ABORT, 'reject answer'); END`); err != nil {
		t.Fatal(err)
	}
	if _, err = s.AnswerQuestion(ctx, q.ID, "atomic?", now); err == nil {
		t.Fatal("expected audit failure")
	}
	questions, err := s.ListQuestions(ctx, task.ID, domain.QuestionsUnanswered)
	if err != nil || len(questions) != 1 {
		t.Fatalf("answer persisted despite audit failure: %+v err=%v", questions, err)
	}
	if _, err = s.db.Exec(`DROP TRIGGER reject_answer`); err != nil {
		t.Fatal(err)
	}

	answered, err := s.AnswerQuestion(ctx, q.ID, "Yes, preserve them", now.Add(time.Second))
	if err != nil || !answered.Answered() || *answered.Answer != "Yes, preserve them" {
		t.Fatalf("answered=%+v err=%v", answered, err)
	}
	var actorKind string
	if err = s.db.QueryRow(`SELECT actor_kind FROM events WHERE task_id=? AND kind='question_answered'`, task.ID).Scan(&actorKind); err != nil || actorKind != "human" {
		t.Fatalf("answer actor=%q err=%v", actorKind, err)
	}

	// The answer stays editable until acknowledged.
	edited, err := s.AnswerQuestion(ctx, q.ID, "Actually, drop them", now.Add(2*time.Second))
	if err != nil || *edited.Answer != "Actually, drop them" {
		t.Fatalf("edited=%+v err=%v", edited, err)
	}

	if _, err = s.AcknowledgeQuestion(ctx, q.ID, other, now.Add(3*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("foreign ack err=%v", err)
	}
	acked, err := s.AcknowledgeQuestion(ctx, q.ID, owner, now.Add(3*time.Second))
	if err != nil || !acked.Acknowledged() {
		t.Fatalf("acked=%+v err=%v", acked, err)
	}
	var ackKind, ackName string
	if err = s.db.QueryRow(`SELECT actor_kind,actor_name FROM events WHERE task_id=? AND kind='question_acknowledged'`, task.ID).Scan(&ackKind, &ackName); err != nil || ackKind != "agent" || ackName != "codex" {
		t.Fatalf("ack actor=%s/%s err=%v", ackKind, ackName, err)
	}

	// Repeated acknowledgement is idempotent and writes no second event.
	again, err := s.AcknowledgeQuestion(ctx, q.ID, owner, now.Add(4*time.Second))
	if err != nil || !again.Acknowledged() || formatTime(*again.AcknowledgedAt) != formatTime(*acked.AcknowledgedAt) {
		t.Fatalf("repeated ack=%+v err=%v", again, err)
	}
	var ackEvents int
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM events WHERE kind='question_acknowledged'`).Scan(&ackEvents); err != nil || ackEvents != 1 {
		t.Fatalf("ack events=%d err=%v", ackEvents, err)
	}

	// An acknowledged answer can never be silently overwritten.
	if _, err = s.AnswerQuestion(ctx, q.ID, "too late", now.Add(5*time.Second)); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("post-ack answer err=%v", err)
	}
	final, err := s.ListQuestions(ctx, task.ID, domain.QuestionsAll)
	if err != nil || len(final) != 1 || *final[0].Answer != "Actually, drop them" {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}

func TestQuestionFiltersAndHistory(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "filters", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ListQuestions(ctx, 999, domain.QuestionsAll); !errors.Is(err, domain.ErrNotFound) {
		t.Fatalf("unknown task err=%v", err)
	}

	unanswered, err := s.AskQuestion(ctx, task.ID, "still open", false, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	answeredOnly, err := s.AskQuestion(ctx, task.ID, "answered", false, owner, now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ackedQ, err := s.AskQuestion(ctx, task.ID, "acknowledged", false, owner, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{answeredOnly.ID, ackedQ.ID} {
		if _, err = s.AnswerQuestion(ctx, id, "ok", now.Add(3*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = s.AcknowledgeQuestion(ctx, ackedQ.ID, owner, now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}

	all, err := s.ListQuestions(ctx, task.ID, domain.QuestionsAll)
	if err != nil || len(all) != 3 || all[0].ID != unanswered.ID || all[2].ID != ackedQ.ID {
		t.Fatalf("all=%+v err=%v", all, err)
	}
	open, err := s.ListQuestions(ctx, task.ID, domain.QuestionsUnanswered)
	if err != nil || len(open) != 1 || open[0].ID != unanswered.ID {
		t.Fatalf("unanswered=%+v err=%v", open, err)
	}
	pending, err := s.ListQuestions(ctx, task.ID, domain.QuestionsUnacknowledged)
	if err != nil || len(pending) != 2 || pending[0].ID != unanswered.ID || pending[1].ID != answeredOnly.ID {
		t.Fatalf("unacknowledged=%+v err=%v", pending, err)
	}

	// Terminal completion keeps history readable.
	if _, err = s.CompleteClaimedTask(ctx, task.ID, "done", owner, now.Add(5*time.Second)); err != nil {
		t.Fatal(err)
	}
	all, err = s.ListQuestions(ctx, task.ID, domain.QuestionsAll)
	if err != nil || len(all) != 3 {
		t.Fatalf("history after completion=%+v err=%v", all, err)
	}
}

func TestBlockingQuestionsPreventReleaseAndCompletion(t *testing.T) {
	s := openTestStore(t)
	ctx := context.Background()
	task := createReadyTask(t, s, "guard", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	if _, err := s.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	q, err := s.AskQuestion(ctx, task.ID, "blocking", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = s.ReleaseClaim(ctx, task.ID, owner, "give up", now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("release with pending question err=%v", err)
	}
	if _, err = s.CompleteClaimedTask(ctx, task.ID, "done", owner, now); !errors.Is(err, domain.ErrConflict) {
		t.Fatalf("complete with pending question err=%v", err)
	}
	if _, err = s.AnswerQuestion(ctx, q.ID, "resolved", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	// Answered-but-unacknowledged questions no longer block completion.
	view, err := s.CompleteClaimedTask(ctx, task.ID, "done", owner, now.Add(2*time.Second))
	if err != nil || view.Lifecycle != domain.LifecycleDone {
		t.Fatalf("complete after answer=%+v err=%v", view, err)
	}
}

func TestConcurrentAnswersCannotCorruptAcknowledgedState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "answers.db")
	first, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer first.Close()
	second, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer second.Close()
	ctx := context.Background()
	task := createReadyTask(t, first, "race", domain.PriorityNormal)
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	if _, err = first.ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	q, err := first.AskQuestion(ctx, task.ID, "race", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = first.AnswerQuestion(ctx, q.ID, "final", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = first.AcknowledgeQuestion(ctx, q.ID, owner, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}

	stores := []*Store{first, second}
	start := make(chan struct{})
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func(i int) {
			<-start
			_, e := stores[i].AnswerQuestion(ctx, q.ID, fmt.Sprintf("overwrite-%d", i), time.Now().UTC())
			results <- e
		}(i)
	}
	close(start)
	for i := 0; i < 2; i++ {
		if e := <-results; !errors.Is(e, domain.ErrConflict) {
			t.Fatalf("post-ack overwrite err=%v", e)
		}
	}
	final, err := first.ListQuestions(ctx, task.ID, domain.QuestionsAll)
	if err != nil || len(final) != 1 || *final[0].Answer != "final" || !final[0].Acknowledged() {
		t.Fatalf("final=%+v err=%v", final, err)
	}
}
