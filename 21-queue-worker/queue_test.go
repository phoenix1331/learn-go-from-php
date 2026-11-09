package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

// miniredis is a real Redis server in memory, so these tests use the actual
// commands, transactions and Lua. Each test gets its own, so they can all run
// in parallel. That matters because workers wait up to a second to stop.

const testPrefix = "app-"

type fixture struct {
	rdb  *redis.Client
	q    *Queue
	keys Keys
	now  time.Time

	jobTimeout time.Duration
}

func newFixture(t *testing.T) *fixture {
	t.Helper()
	t.Parallel()
	m := miniredis.RunT(t)
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr()})
	t.Cleanup(func() { rdb.Close() })

	f := &fixture{
		rdb: rdb, q: NewQueue(rdb, testPrefix, "w1"), keys: NewKeys(testPrefix, "w1"),
		jobTimeout: 50 * time.Millisecond,
	}
	f.now = time.Date(2026, 1, 2, 3, 0, 0, 0, time.UTC)
	f.q.now = func() time.Time { return f.now }
	return f
}

func (f *fixture) push(t *testing.T, id, jobType, payload string, attempts, maxAttempts int) {
	t.Helper()
	raw, err := json.Marshal(Job{
		ID: id, Type: jobType, Payload: json.RawMessage(payload),
		Attempts: attempts, MaxAttempts: maxAttempts,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := f.rdb.RPush(t.Context(), f.keys.Jobs, raw).Err(); err != nil {
		t.Fatal(err)
	}
}

func (f *fixture) reserve(t *testing.T) Job {
	t.Helper()
	job, ok, err := f.q.Reserve(t.Context(), time.Second)
	if err != nil || !ok {
		t.Fatalf("Reserve: ok=%v err=%v", ok, err)
	}
	return job
}

func (f *fixture) listLen(t *testing.T, key string) int64 {
	t.Helper()
	n, err := f.rdb.LLen(t.Context(), key).Result()
	if err != nil {
		t.Fatal(err)
	}
	return n
}

// laravelEnvelope mirrors the JSON Laravel's worker reads.
type laravelEnvelope struct {
	UUID     string  `json:"uuid"`
	Job      string  `json:"job"`
	Attempts *int    `json:"attempts"`
	Data     *Result `json:"data"`
}

func (f *fixture) results(t *testing.T) []laravelEnvelope {
	t.Helper()
	raws, err := f.rdb.LRange(t.Context(), f.keys.Results, 0, -1).Result()
	if err != nil {
		t.Fatal(err)
	}
	out := make([]laravelEnvelope, len(raws))
	for i, raw := range raws {
		if err := json.Unmarshal([]byte(raw), &out[i]); err != nil {
			t.Fatal(err)
		}
	}
	return out
}

func TestKeysUseLaravelPrefix(t *testing.T) {
	k := NewKeys("laravel-database-", "w1")
	if k.Jobs != "laravel-database-go:jobs" || k.Results != "laravel-database-queues:go-results" {
		t.Errorf("unexpected keys: %+v", k)
	}
}

func TestReserveMovesJobToProcessing(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)

	job := f.reserve(t)

	if job.ID != "a" {
		t.Errorf("ID = %q", job.ID)
	}
	if f.listLen(t, f.keys.Jobs) != 0 || f.listLen(t, f.keys.Processing) != 1 {
		t.Error("job should have moved from jobs to processing")
	}
}

func TestReserveIsFIFO(t *testing.T) {
	f := newFixture(t)
	f.push(t, "first", "sum", `{}`, 0, 3)
	f.push(t, "second", "sum", `{}`, 0, 3)

	if got := f.reserve(t).ID; got != "first" {
		t.Errorf("got %q, want first", got)
	}
}

func TestReserveEmptyQueue(t *testing.T) {
	f := newFixture(t)
	_, ok, err := f.q.Reserve(t.Context(), time.Second)
	if ok || err != nil {
		t.Errorf("ok=%v err=%v, want false, nil", ok, err)
	}
}

func TestMalformedJobsAreBuried(t *testing.T) {
	for _, raw := range []string{`not json`, `{"id":"x"}`, `{"type":"sum"}`} {
		t.Run(raw, func(t *testing.T) {
			f := newFixture(t)
			f.rdb.RPush(t.Context(), f.keys.Jobs, raw)

			_, ok, err := f.q.Reserve(t.Context(), time.Second)
			if ok || !errors.Is(err, ErrMalformed) {
				t.Fatalf("ok=%v err=%v, want ErrMalformed", ok, err)
			}
			if f.listLen(t, f.keys.Processing) != 0 || f.listLen(t, f.keys.Failed) != 1 {
				t.Error("should be moved to failed")
			}
			var entry failedEntry
			first := f.rdb.LIndex(t.Context(), f.keys.Failed, 0).Val()
			if err := json.Unmarshal([]byte(first), &entry); err != nil || entry.Raw != raw {
				t.Errorf("failed entry %q, err %v", first, err)
			}
		})
	}
}

func TestFinishWritesLaravelEnvelope(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)
	job := f.reserve(t)

	r := Result{JobID: "a", Type: "sum", Status: "done", Output: json.RawMessage(`{"total":6}`)}
	if err := f.q.Finish(t.Context(), job, r); err != nil {
		t.Fatal(err)
	}

	if f.listLen(t, f.keys.Processing) != 0 {
		t.Error("job should be removed from processing")
	}
	// Laravel's blocking pop waits on this list.
	if f.listLen(t, f.keys.ResultsNotify) != 1 {
		t.Error("notify list not pushed")
	}
	if f.listLen(t, f.keys.Failed) != 0 {
		t.Error("a successful job must not be on the failed list")
	}

	got := f.results(t)[0]
	if got.Job != `App\Queue\GoResultHandler@handle` {
		t.Errorf("job = %q", got.Job)
	}
	// Laravel's Lua pop script does attempts + 1. Missing means an error.
	if got.Attempts == nil || *got.Attempts != 0 {
		t.Errorf("attempts = %v, want 0", got.Attempts)
	}
	if got.UUID == "" || got.Data == nil || got.Data.JobID != "a" {
		t.Errorf("unexpected envelope: %+v", got)
	}
}

func TestRetryWaitsUntilDue(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)
	job := f.reserve(t)

	if err := f.q.Retry(t.Context(), job, time.Minute); err != nil {
		t.Fatal(err)
	}
	if f.listLen(t, f.keys.Processing) != 0 {
		t.Error("job should have left processing")
	}

	if n, err := f.q.PromoteDue(t.Context()); n != 0 || err != nil {
		t.Fatalf("too early: promoted %d, err %v", n, err)
	}

	f.now = f.now.Add(time.Minute)
	if n, err := f.q.PromoteDue(t.Context()); n != 1 || err != nil {
		t.Fatalf("promoted %d, err %v", n, err)
	}
	if got := f.reserve(t); got.Attempts != 1 {
		t.Errorf("Attempts = %d, want 1", got.Attempts)
	}
}

func TestReleaseGoesToFrontWithoutUsingAnAttempt(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)
	job := f.reserve(t)
	f.push(t, "b", "sum", `{}`, 0, 3)

	if err := f.q.Release(t.Context(), job); err != nil {
		t.Fatal(err)
	}
	got := f.reserve(t)
	if got.ID != "a" || got.Attempts != 0 {
		t.Errorf("got %+v, want job a with 0 attempts", got)
	}
}

func TestRecoverRestoresJobsInOrder(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)
	f.push(t, "b", "sum", `{}`, 0, 3)
	f.reserve(t)
	f.reserve(t)
	f.push(t, "c", "sum", `{}`, 0, 3)

	n, err := f.q.Recover(t.Context())
	if err != nil || n != 2 {
		t.Fatalf("Recover = %d, %v", n, err)
	}
	for _, want := range []string{"a", "b", "c"} {
		if got := f.reserve(t).ID; got != want {
			t.Errorf("got %q, want %q", got, want)
		}
	}
}

func TestRecoverOnlyTouchesOwnList(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "sum", `{}`, 0, 3)
	f.reserve(t)

	other := NewQueue(f.rdb, testPrefix, "w2")
	if n, err := other.Recover(t.Context()); n != 0 || err != nil {
		t.Errorf("another worker recovered %d jobs, err %v", n, err)
	}
}

// startWorker runs a worker with no delay between retries.
func (f *fixture) startWorker(t *testing.T, h map[string]Handler, grace time.Duration) (stop func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	worker := &Worker{
		Queue: f.q, Handlers: h, Name: "w1", Concurrency: 2,
		JobTimeout: f.jobTimeout, Grace: grace, Block: time.Second,
		Backoff: func(int) time.Duration { return 0 },
		Logger:  slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	done := make(chan error, 1)
	go func() { done <- worker.Run(ctx) }()

	var stopped bool
	stop = func() {
		if stopped {
			return
		}
		stopped = true
		cancel()
		if err := <-done; err != nil {
			t.Errorf("Run: %v", err)
		}
	}
	t.Cleanup(stop)
	return stop
}

// waitResult blocks until one result has arrived.
func (f *fixture) waitResult(t *testing.T) *Result {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for f.listLen(t, f.keys.Results) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("no result arrived")
		}
		time.Sleep(5 * time.Millisecond)
	}
	return f.results(t)[0].Data
}

func TestWorkerOutcomes(t *testing.T) {
	boom := errors.New("boom")
	tests := []struct {
		name         string
		jobType      string
		maxAttempts  int
		handler      Handler
		wantStatus   string
		wantAttempts int
		wantFailed   int64
	}{
		{
			name: "success", jobType: "t", maxAttempts: 3,
			handler:    func(context.Context, Job) (any, error) { return 42, nil },
			wantStatus: "done", wantAttempts: 1,
		},
		{
			name: "succeeds on retry", jobType: "t", maxAttempts: 3,
			handler: func(_ context.Context, j Job) (any, error) {
				if j.Attempts < 2 {
					return nil, boom
				}
				return 42, nil
			},
			wantStatus: "done", wantAttempts: 3,
		},
		{
			name: "runs out of attempts", jobType: "t", maxAttempts: 2,
			handler:    func(context.Context, Job) (any, error) { return nil, boom },
			wantStatus: "failed", wantAttempts: 2, wantFailed: 1,
		},
		{
			name: "permanent error stops retries", jobType: "t", maxAttempts: 5,
			handler:    func(context.Context, Job) (any, error) { return nil, Permanent(boom) },
			wantStatus: "failed", wantAttempts: 1, wantFailed: 1,
		},
		{
			name: "unknown type", jobType: "nope", maxAttempts: 5,
			handler:    func(context.Context, Job) (any, error) { return 1, nil },
			wantStatus: "failed", wantAttempts: 1, wantFailed: 1,
		},
		{
			name: "panic is contained", jobType: "t", maxAttempts: 1,
			handler:    func(context.Context, Job) (any, error) { panic("oh no") },
			wantStatus: "failed", wantAttempts: 1, wantFailed: 1,
		},
		{
			name: "job timeout counts as a failure", jobType: "t", maxAttempts: 1,
			handler: func(ctx context.Context, _ Job) (any, error) {
				<-ctx.Done()
				return nil, ctx.Err()
			},
			wantStatus: "failed", wantAttempts: 1, wantFailed: 1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := newFixture(t)
			// Real time here, because the worker's promote loop needs retries
			// to come due.
			f.q.now = time.Now
			f.push(t, "a", tt.jobType, `{}`, 0, tt.maxAttempts)
			f.startWorker(t, map[string]Handler{"t": tt.handler}, time.Second)

			got := f.waitResult(t)
			if got.Status != tt.wantStatus || got.Attempts != tt.wantAttempts {
				t.Errorf("status=%s attempts=%d, want %s and %d (error: %s)",
					got.Status, got.Attempts, tt.wantStatus, tt.wantAttempts, got.Error)
			}
			if n := f.listLen(t, f.keys.Failed); n != tt.wantFailed {
				t.Errorf("failed list has %d, want %d", n, tt.wantFailed)
			}
		})
	}
}

func TestWorkerRecoversCrashedJobs(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "t", `{}`, 0, 3)
	f.reserve(t) // reserved, then the old process "dies"

	f.startWorker(t, map[string]Handler{
		"t": func(context.Context, Job) (any, error) { return "ok", nil },
	}, time.Second)

	if got := f.waitResult(t); got.JobID != "a" || got.Status != "done" {
		t.Errorf("got %+v", got)
	}
}

func TestShutdownFinishesJobInHand(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "t", `{}`, 0, 3)

	started, release := make(chan struct{}), make(chan struct{})
	stop := f.startWorker(t, map[string]Handler{"t": func(context.Context, Job) (any, error) {
		close(started)
		<-release
		return "ok", nil
	}}, 5*time.Second)

	<-started
	stopped := make(chan struct{})
	go func() {
		stop()
		close(stopped)
	}()

	select {
	case <-stopped:
		t.Fatal("worker stopped with a job still running")
	case <-time.After(50 * time.Millisecond):
	}
	close(release)
	<-stopped

	if got := f.waitResult(t); got.Status != "done" {
		t.Errorf("status = %s", got.Status)
	}
}

func TestShutdownReleasesJobAfterGrace(t *testing.T) {
	f := newFixture(t)
	f.push(t, "a", "t", `{}`, 0, 3)

	f.jobTimeout = time.Minute // so only the grace period can stop it

	started := make(chan struct{})
	stop := f.startWorker(t, map[string]Handler{"t": func(ctx context.Context, _ Job) (any, error) {
		close(started)
		<-ctx.Done()
		return nil, ctx.Err()
	}}, 20*time.Millisecond)

	<-started
	stop()

	if n := f.listLen(t, f.keys.Results); n != 0 {
		t.Errorf("%d results reported for an abandoned job", n)
	}
	if n := f.listLen(t, f.keys.Processing); n != 0 {
		t.Errorf("processing list has %d, want 0", n)
	}
	if got := f.reserve(t); got.ID != "a" || got.Attempts != 0 {
		t.Errorf("got %+v, want job a back with 0 attempts", got)
	}
}
