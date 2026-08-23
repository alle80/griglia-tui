package sqlite

// Additional concurrency invariants for the v1 release: real SQLite handles,
// no mocks. These complement the claim, dependency, and post-acknowledgement
// races proven in the earlier milestone suites.

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

func openSharedStores(t *testing.T, count int) []*Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "shared.db")
	stores := make([]*Store, count)
	for i := range stores {
		s, err := Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = s.Close() })
		stores[i] = s
	}
	return stores
}

// A human re-answer and the asking agent's acknowledgement race on the same
// answered question. Both transactions are serialized by the immediate write
// lock: either the new answer lands before the acknowledgement (both
// succeed), or the acknowledgement freezes the previous answer and the
// re-answer reports a conflict. No interleaving may corrupt the final state.
func TestAnswerVersusAcknowledgeRace(t *testing.T) {
	stores := openSharedStores(t, 2)
	ctx := context.Background()
	owner := domain.AgentIdentity{AgentName: "codex", InstanceID: "one"}
	now := time.Now().UTC()
	task := createReadyTask(t, stores[0], "answer-vs-ack", domain.PriorityNormal)
	if _, err := stores[0].ClaimTask(ctx, task.ID, owner, now); err != nil {
		t.Fatal(err)
	}
	question, err := stores[0].AskQuestion(ctx, task.ID, "which?", true, owner, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = stores[0].AnswerQuestion(ctx, question.ID, "original", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}

	start := make(chan struct{})
	answerErr := make(chan error, 1)
	ackErr := make(chan error, 1)
	go func() {
		<-start
		_, e := stores[0].AnswerQuestion(ctx, question.ID, "revised", time.Now().UTC())
		answerErr <- e
	}()
	go func() {
		<-start
		_, e := stores[1].AcknowledgeQuestion(ctx, question.ID, owner, time.Now().UTC())
		ackErr <- e
	}()
	close(start)

	reAnswer := <-answerErr
	if e := <-ackErr; e != nil {
		t.Fatalf("acknowledge err=%v", e)
	}
	if reAnswer != nil && !errors.Is(reAnswer, domain.ErrConflict) {
		t.Fatalf("re-answer err=%v", reAnswer)
	}

	final, err := stores[0].ListQuestions(ctx, task.ID, domain.QuestionsAll)
	if err != nil || len(final) != 1 {
		t.Fatalf("final=%v err=%v", final, err)
	}
	q := final[0]
	if !q.Acknowledged() || !q.Answered() {
		t.Fatalf("final question=%+v", q)
	}
	want := "original"
	if reAnswer == nil {
		want = "revised"
	}
	if *q.Answer != want {
		t.Fatalf("answer=%q want=%q (reAnswer err=%v)", *q.Answer, want, reAnswer)
	}
}

// More agents than eligible work: every task is claimed exactly once, the
// surplus agents receive the stable no-eligible-task error, and no agent
// observes a partially claimed task.
func TestClaimNextStormAssignsEachTaskOnce(t *testing.T) {
	const agents, tasks = 4, 3
	stores := openSharedStores(t, agents)
	for i := 0; i < tasks; i++ {
		createReadyTask(t, stores[0], fmt.Sprintf("storm-%d", i), domain.PriorityNormal)
	}
	start := make(chan struct{})
	type result struct {
		view domain.TaskView
		err  error
	}
	results := make(chan result, agents)
	for i := 0; i < agents; i++ {
		go func(i int) {
			<-start
			identity := domain.AgentIdentity{AgentName: fmt.Sprintf("agent-%d", i), InstanceID: fmt.Sprintf("instance-%d", i)}
			view, err := stores[i].ClaimNext(context.Background(), identity, time.Now().UTC())
			results <- result{view, err}
		}(i)
	}
	close(start)

	claimed := map[int64]bool{}
	misses := 0
	for i := 0; i < agents; i++ {
		r := <-results
		switch {
		case r.err == nil:
			if claimed[r.view.ID] {
				t.Fatalf("task %d claimed twice", r.view.ID)
			}
			if r.view.ActiveClaim == nil || *r.view.OperationalState != domain.OperationalWorking {
				t.Fatalf("claimed view=%+v", r.view)
			}
			claimed[r.view.ID] = true
		case errors.Is(r.err, domain.ErrNoEligible):
			misses++
		default:
			t.Fatalf("unexpected err=%v", r.err)
		}
	}
	if len(claimed) != tasks || misses != agents-tasks {
		t.Fatalf("claimed=%d misses=%d", len(claimed), misses)
	}
	var active, distinct int
	if err := stores[0].db.QueryRow(`SELECT COUNT(*),COUNT(DISTINCT task_id) FROM claims WHERE released_at IS NULL`).Scan(&active, &distinct); err != nil || active != tasks || distinct != tasks {
		t.Fatalf("active=%d distinct=%d err=%v", active, distinct, err)
	}
}
