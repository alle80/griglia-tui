package sqlite

// Read-path regression benchmarks. These protect the bounded task-list and
// detail reads (no N+1 queries) at realistic sizes. They report time and
// allocations; CI and reviews compare trends rather than asserting brittle
// machine-specific thresholds.

import (
	"context"
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/alle80/griglia-tui/internal/domain"
)

// seedBenchmarkTasks populates count tasks with a representative mix of
// lifecycles plus claims, questions, and dependencies, using one transaction
// so setup stays fast at 10k rows.
func seedBenchmarkTasks(b *testing.B, s *Store, count int) {
	b.Helper()
	tx, err := s.db.Begin()
	if err != nil {
		b.Fatal(err)
	}
	defer tx.Rollback()
	now := formatTime(time.Now().UTC())
	lifecycles := []string{"backlog", "ready", "ready", "ready", "done", "cancelled"}
	priorities := []string{"low", "normal", "normal", "high", "urgent"}
	for i := 1; i <= count; i++ {
		lifecycle := lifecycles[i%len(lifecycles)]
		if _, err = tx.Exec(`INSERT INTO tasks(uid,title,description,lifecycle,priority,progress,phase,completion_summary,created_at,updated_at,version) VALUES(?,?,?,?,?,?,?,?,?,?,1)`,
			fmt.Sprintf("bench-%d", i), fmt.Sprintf("Benchmark task %d", i), "description", lifecycle, priorities[i%len(priorities)], i%101, "", "", now, now); err != nil {
			b.Fatal(err)
		}
		if lifecycle == "ready" && i%5 == 0 {
			if _, err = tx.Exec(`INSERT INTO claims(task_id,agent_name,instance_id,claimed_at,last_activity_at) VALUES(?,?,?,?,?)`, i, "bench-agent", fmt.Sprintf("instance-%d", i), now, now); err != nil {
				b.Fatal(err)
			}
			if i%10 == 0 {
				if _, err = tx.Exec(`INSERT INTO questions(task_id,body,blocking,asked_by_agent_name,asked_by_instance_id,asked_at) VALUES(?,?,1,?,?,?)`, i, "blocking question", "bench-agent", fmt.Sprintf("instance-%d", i), now); err != nil {
					b.Fatal(err)
				}
			}
		}
		if i > 1 && i%4 == 0 {
			if _, err = tx.Exec(`INSERT INTO dependencies(task_id,depends_on_task_id,created_at) VALUES(?,?,?)`, i, i-1, now); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
}

func openBenchmarkStore(b *testing.B, count int) *Store {
	b.Helper()
	s, err := Open(filepath.Join(b.TempDir(), "bench.db"))
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = s.Close() })
	seedBenchmarkTasks(b, s, count)
	return s
}

func BenchmarkListTasks(b *testing.B) {
	for _, count := range []int{100, 1000, 10000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			s := openBenchmarkStore(b, count)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				views, err := s.ListTasks(ctx)
				if err != nil || len(views) != count {
					b.Fatalf("len=%d err=%v", len(views), err)
				}
			}
		})
	}
}

func BenchmarkGetTask(b *testing.B) {
	for _, count := range []int{100, 10000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			s := openBenchmarkStore(b, count)
			ctx := context.Background()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				if _, err := s.GetTask(ctx, int64(i%count+1)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkClaimNextSelection(b *testing.B) {
	// Claim and release inside the loop so the eligible pool stays stable;
	// the measured cost is the selection query plus the claim transaction.
	for _, count := range []int{1000} {
		b.Run(fmt.Sprintf("tasks=%d", count), func(b *testing.B) {
			s := openBenchmarkStore(b, count)
			ctx := context.Background()
			identity := domain.AgentIdentity{AgentName: "bench-agent", InstanceID: "bench-instance"}
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				now := time.Now().UTC()
				view, err := s.ClaimNext(ctx, identity, now)
				if err != nil {
					b.Fatal(err)
				}
				if _, err = s.ReleaseClaim(ctx, view.ID, identity, "bench", now); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}
