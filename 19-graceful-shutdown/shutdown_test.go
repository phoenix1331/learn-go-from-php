package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// Most of these tests coordinate with channels rather than sleeps: the
// handler says when it has started, and the test says when it may finish.
// That makes them fast and stops them being flaky.

func listen(t *testing.T) net.Listener {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return ln
}

func TestServeFinishesInFlightRequest(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-release
		io.WriteString(w, "finished")
	})}
	ln := listen(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, srv, ln, 5*time.Second) }()

	reply := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		reply <- describe(resp, err)
	}()

	<-entered
	cancel()

	// The whole point: Serve must still be waiting for the handler.
	select {
	case err := <-served:
		t.Fatalf("Serve returned before the request finished: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	if got := <-reply; got != "200 finished" {
		t.Errorf("client got %q", got)
	}
	if err := <-served; err != nil {
		t.Errorf("Serve: %v", err)
	}
}

func TestServeForcesCloseAfterGrace(t *testing.T) {
	entered, cancelled := make(chan struct{}), make(chan struct{})
	srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(entered)
		<-r.Context().Done()
		close(cancelled)
	})}
	ln := listen(t)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	served := make(chan error, 1)
	go func() { served <- Serve(ctx, srv, ln, 20*time.Millisecond) }()

	reply := make(chan string, 1)
	go func() {
		resp, err := http.Get("http://" + ln.Addr().String())
		reply <- describe(resp, err)
	}()

	<-entered
	cancel()

	if err := <-served; !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("Serve = %v, want DeadlineExceeded", err)
	}
	<-cancelled // the handler's r.Context() was cancelled by srv.Close
	if got := <-reply; !strings.HasPrefix(got, "ERROR") {
		t.Errorf("client got %q, want an error", got)
	}
}

func TestServeReportsServerFailure(t *testing.T) {
	ln := listen(t)
	ln.Close() // Serve will fail straight away

	err := Serve(t.Context(), &http.Server{}, ln, time.Second)
	if err == nil || errors.Is(err, http.ErrServerClosed) {
		t.Errorf("Serve = %v, want a real failure", err)
	}
}

func TestQueue(t *testing.T) {
	q := NewQueue(1)
	if err := q.Push(Job{ID: 1}); err != nil {
		t.Fatal(err)
	}
	if err := q.Push(Job{ID: 2}); !errors.Is(err, ErrQueueFull) {
		t.Errorf("full queue: %v", err)
	}
	q.Close()
	q.Close() // twice must not panic
	if err := q.Push(Job{ID: 3}); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("closed queue: %v", err)
	}
	if left := q.Leftover(); len(left) != 1 || left[0].ID != 1 {
		t.Errorf("Leftover = %v", left)
	}
}

// Without the lock in Queue, this panics with "send on closed channel".
func TestQueueCloseDuringPush(t *testing.T) {
	q := NewQueue(1)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			for range 100 {
				q.Push(Job{})
			}
		})
	}
	wg.Go(q.Close)
	wg.Wait()
}

// startedHandler reports each job on started, then blocks until release is
// closed or ctx is cancelled.
func startedHandler(started chan<- struct{}, release <-chan struct{}) func(context.Context, Job) error {
	return func(ctx context.Context, j Job) error {
		started <- struct{}{}
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
}

func fullQueue(n int) *Queue {
	q := NewQueue(n)
	for id := range n {
		q.Push(Job{ID: id})
	}
	return q
}

func TestPoolFinishesInFlightJobs(t *testing.T) {
	started, release := make(chan struct{}, 4), make(chan struct{})
	pool := &Pool{Workers: 2, Grace: 5 * time.Second, Handle: startedHandler(started, release)}
	q := fullQueue(4)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	result := make(chan Stats, 1)
	go func() { result <- pool.Run(ctx, q.Jobs()) }()

	<-started
	<-started
	cancel()
	time.Sleep(20 * time.Millisecond) // the signal must not have aborted them
	close(release)

	if got, want := <-result, (Stats{Done: 2}); got != want {
		t.Errorf("Stats = %+v, want %+v", got, want)
	}
}

func TestPoolStopsTakingJobs(t *testing.T) {
	// Already cancelled before it starts, with jobs waiting: the first
	// select in the worker loop means none of them may be picked up.
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	pool := &Pool{Workers: 2, Grace: time.Second, Handle: func(context.Context, Job) error {
		t.Error("a job started after the signal")
		return nil
	}}
	q := fullQueue(4)
	pool.Run(ctx, q.Jobs())

	q.Close()
	if left := q.Leftover(); len(left) != 4 {
		t.Errorf("%d jobs left, want 4", len(left))
	}
}

func TestPoolAbortsAfterGrace(t *testing.T) {
	started := make(chan struct{}, 2)
	never := make(chan struct{})
	pool := &Pool{Workers: 2, Grace: 20 * time.Millisecond, Handle: startedHandler(started, never)}
	q := fullQueue(2)

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-started
		<-started
		cancel()
	}()

	if got, want := pool.Run(ctx, q.Jobs()), (Stats{Aborted: 2}); got != want {
		t.Errorf("Stats = %+v, want %+v", got, want)
	}
}

func TestPoolDrainsClosedQueue(t *testing.T) {
	pool := &Pool{Workers: 2, Grace: time.Second, Handle: func(context.Context, Job) error {
		return nil
	}}
	q := fullQueue(5)
	q.Close()

	if got, want := pool.Run(t.Context(), q.Jobs()), (Stats{Done: 5}); got != want {
		t.Errorf("Stats = %+v, want %+v", got, want)
	}
}

// One test with a real signal, end to end. The logs say what happened.
func TestRunAppDrainsOnSIGTERM(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("needs Unix signals")
	}

	var logs bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logs, nil))
	ln := listen(t)
	base := "http://" + ln.Addr().String()

	ctx, stop := signal.NotifyContext(t.Context(), syscall.SIGTERM)
	defer stop()
	appDone := make(chan error, 1)
	go func() { appDone <- RunApp(ctx, ln, logger, 5*time.Second) }()

	// The default client can leave a spare connection open, and Shutdown
	// waits on those for 5 seconds. See the comment in Serve.
	client := oneShotClient()
	for range 3 {
		resp, err := client.Post(base+"/jobs?ms=30", "text/plain", nil)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusAccepted {
			t.Fatalf("POST /jobs: %d", resp.StatusCode)
		}
	}

	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		t.Fatal(err)
	}
	if err := p.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	if err := <-appDone; err != nil {
		t.Fatalf("RunApp: %v", err)
	}

	// Safe to read now: RunApp has returned, so nothing else is logging.
	var stopped map[string]any
	for line := range strings.Lines(logs.String()) {
		var m map[string]any
		if err := json.Unmarshal([]byte(line), &m); err != nil {
			t.Fatalf("not JSON: %q", line)
		}
		if m["msg"] == "workers stopped" {
			stopped = m
		}
	}
	if stopped["done"] != 3.0 || stopped["aborted"] != 0.0 || stopped["not_started"] != 0.0 {
		t.Errorf("workers stopped: %v, want done=3", stopped)
	}

	if _, err := client.Get(base + "/health"); err == nil {
		t.Error("server still accepting requests after shutdown")
	}
}
