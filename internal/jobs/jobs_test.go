package jobs

import (
	"context"
	"errors"
	"testing"
	"time"

	"go.uber.org/zap"
)

func wait(t *testing.T, m *Manager, id string) Job {
	t.Helper()
	for i := 0; i < 200; i++ {
		if j, _ := m.Get(id); j.Status != StatusRunning {
			return j
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("job did not finish")
	return Job{}
}

func TestCompletedFailedAndLogicalFailure(t *testing.T) {
	m := New(zap.NewNop(), 5, 50)
	defer m.Shutdown()

	ok, _ := m.Start("a", func(ctx context.Context) (any, bool, string, error) { return "res", false, "", nil })
	if j := wait(t, m, ok.ID); j.Status != StatusCompleted || j.Result != "res" || j.Progress.Percent != 100 {
		t.Fatalf("completed: %+v", j)
	}
	bad, _ := m.Start("b", func(ctx context.Context) (any, bool, string, error) { return nil, false, "", errors.New("boom") })
	if j := wait(t, m, bad.ID); j.Status != StatusFailed || j.Error != "boom" {
		t.Fatalf("failed: %+v", j)
	}
	logical, _ := m.Start("c", func(ctx context.Context) (any, bool, string, error) { return "r", true, "xe said no", nil })
	if j := wait(t, m, logical.ID); j.Status != StatusFailed || j.Error != "xe said no" || j.Result != "r" {
		t.Fatalf("logical failure: %+v", j)
	}
}

func TestProgressAndCancel(t *testing.T) {
	m := New(zap.NewNop(), 5, 50)
	defer m.Shutdown()
	started := make(chan struct{})
	j, _ := m.Start("slow", func(ctx context.Context) (any, bool, string, error) {
		Report(ctx, 50, 200, "writing")
		close(started)
		<-ctx.Done()
		return nil, false, "", ctx.Err()
	})
	<-started
	got, _ := m.Get(j.ID)
	if got.Status != StatusRunning || got.Progress.BytesDone != 50 || got.Progress.Percent != 25 {
		t.Fatalf("progress: %+v", got)
	}
	if !m.Cancel(j.ID) {
		t.Fatal("cancel of running job must succeed")
	}
	if fin := wait(t, m, j.ID); fin.Status != StatusCancelled {
		t.Fatalf("want cancelled, got %+v", fin)
	}
	if m.Cancel(j.ID) || m.Cancel("nope") {
		t.Fatal("cancel of finished/unknown job must return false")
	}
}

func TestConcurrencyLimitAndEviction(t *testing.T) {
	m := New(zap.NewNop(), 1, 2)
	defer m.Shutdown()
	release := make(chan struct{})
	j1, err := m.Start("one", func(ctx context.Context) (any, bool, string, error) { <-release; return nil, false, "", nil })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.Start("two", func(ctx context.Context) (any, bool, string, error) { return nil, false, "", nil }); !errors.Is(err, ErrTooManyJobs) {
		t.Fatalf("want ErrTooManyJobs, got %v", err)
	}
	close(release)
	wait(t, m, j1.ID)
	for i := 0; i < 4; i++ {
		j, _ := m.Start("n", func(ctx context.Context) (any, bool, string, error) { return nil, false, "", nil })
		wait(t, m, j.ID)
	}
	if n := len(m.List()); n > 3 { // keep=2 finished + the newest
		t.Fatalf("eviction: %d jobs retained", n)
	}
}

func TestProgressEstimateExceededAndCompletionUsesRealSize(t *testing.T) {
	m := New(zap.NewNop(), 5, 50)
	defer m.Shutdown()
	hold := make(chan struct{})
	reported := make(chan struct{})
	j, _ := m.Start("x", func(ctx context.Context) (any, bool, string, error) {
		Report(ctx, 300, 100, "writing") // estimate too low
		close(reported)
		<-hold
		return nil, false, "", nil
	})
	<-reported
	got, _ := m.Get(j.ID)
	if got.Progress.BytesTotal != 0 || got.Progress.Percent != 0 || got.Progress.BytesDone != 300 {
		t.Fatalf("estimate exceeded must drop total/percent, got %+v", got.Progress)
	}
	close(hold)
	fin := wait(t, m, j.ID)
	if fin.Progress.Percent != 100 || fin.Progress.BytesTotal != 300 || fin.Progress.BytesDone != 300 {
		t.Fatalf("completion must settle on real size, got %+v", fin.Progress)
	}
}

func TestReportOutsideJobIsNoop(t *testing.T) {
	Report(context.Background(), 1, 2, "x") // must not panic
	if InJob(context.Background()) {
		t.Fatal("not in a job")
	}
}
