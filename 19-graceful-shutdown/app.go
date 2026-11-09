package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

const (
	workers   = 2
	queueSize = 16
)

// Serve runs srv until ctx is cancelled or the server fails, then shuts it
// down gracefully. It's the reusable core of this lesson.
//
// It only returns once shutdown has finished. That matters: srv.Serve
// returns ErrServerClosed the MOMENT Shutdown is called, while requests are
// still running. A main that exits when Serve returns kills those requests.
func Serve(ctx context.Context, srv *http.Server, ln net.Listener, grace time.Duration) error {
	// Buffered, so this goroutine can always finish even if nobody reads.
	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(ln) }()

	select {
	case err := <-serveErr:
		// It stopped by itself, before any signal: a real failure.
		return fmt.Errorf("server failed: %w", err)
	case <-ctx.Done():
	}

	// The classic mistake is passing ctx to Shutdown. ctx is ALREADY
	// cancelled (that's why we're here), so Shutdown would give up at once.
	// The grace period needs a context of its own.
	graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), grace)
	defer cancel()

	// Shutdown closes the listener, closes idle connections, then waits for
	// active requests to finish, or for graceCtx to expire.
	//
	// One surprise: a connection that was opened but hasn't sent a request
	// yet counts as active for its first 5 seconds. Browsers and HTTP
	// clients do open spare connections, so a grace period under 5s can
	// occasionally end in an error here even though no request was lost.
	if err := srv.Shutdown(graceCtx); err != nil {
		// Out of time. Close cuts the remaining connections, which also
		// cancels each handler's r.Context().
		return errors.Join(fmt.Errorf("graceful shutdown: %w", err), srv.Close())
	}

	if err := <-serveErr; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("server failed: %w", err)
	}
	return nil
}

// RunApp is a small service: an HTTP API feeding a pool of background
// workers. It runs until ctx is cancelled, then stops the parts in order.
//
// THE ORDER IS THE POINT. Stop things from the outside in, so nothing is
// still using a part when it goes away:
//
//  1. stop HTTP       no new work can arrive
//  2. close the queue the workers finish the backlog
//  3. stop workers    bounded by grace
//  4. close the rest  database pools, flushing logs (none here, but this is
//     where `defer db.Close()` runs)
//
// It's the reverse of startup, which is exactly what defer gives you.
//
// Each of the three stages can take up to grace, so the worst case is about
// 3 x grace. Keep that below what your platform allows (see main.go).
func RunApp(ctx context.Context, ln net.Listener, logger *slog.Logger, grace time.Duration) error {
	queue := NewQueue(queueSize)
	pool := &Pool{
		Workers: workers,
		Grace:   grace,
		Handle: func(ctx context.Context, j Job) error {
			return doJob(ctx, logger, j)
		},
	}

	// The pool gets its own context, NOT ctx. The signal shouldn't stop the
	// workers: they have a backlog to finish after HTTP has stopped.
	poolCtx, stopPool := context.WithCancel(context.WithoutCancel(ctx))
	defer stopPool()
	poolDone := make(chan Stats, 1)
	go func() { poolDone <- pool.Run(poolCtx, queue.Jobs()) }()

	srv := &http.Server{
		Handler:           newMux(queue, logger),
		ReadHeaderTimeout: 5 * time.Second,
	}
	logger.Info("listening", "addr", ln.Addr().String())

	// Blocks here for the life of the service.
	serveErr := Serve(ctx, srv, ln, grace)
	logger.Info("http stopped")

	queue.Close()

	// If the backlog outlasts the grace period, tell the workers to stop
	// taking jobs. time.AfterFunc runs stopPool later, on its own goroutine.
	backlog := time.AfterFunc(grace, stopPool)
	defer backlog.Stop()

	stats := <-poolDone
	left := queue.Leftover()
	logger.Info("workers stopped", "done", stats.Done, "aborted", stats.Aborted, "not_started", len(left))
	// An in-memory queue loses these on exit. With Redis (lesson 21) they
	// just wait in the queue for the next worker.

	return serveErr
}

// doJob stands in for real work. Like lesson 14's slowOperation, it waits
// for the work to finish OR for ctx to say give up.
func doJob(ctx context.Context, logger *slog.Logger, j Job) error {
	select {
	case <-time.After(j.Work):
		logger.Info("job finished", "job", j.ID)
		return nil
	case <-ctx.Done():
		logger.Warn("job aborted", "job", j.ID)
		return fmt.Errorf("job %d: %w", j.ID, ctx.Err())
	}
}

func newMux(queue *Queue, logger *slog.Logger) *http.ServeMux {
	mux := http.NewServeMux()
	var nextID atomic.Int64

	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})

	// GET /slow?ms=5000 takes that long to answer.
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(msParam(r)):
			fmt.Fprintln(w, "finished")
		case <-r.Context().Done():
			logger.Warn("request aborted", "path", r.URL.Path)
		}
	})

	// POST /jobs?ms=2000 queues a job and answers straight away, the way
	// a Laravel controller calls dispatch().
	mux.HandleFunc("POST /jobs", func(w http.ResponseWriter, r *http.Request) {
		id := int(nextID.Add(1))
		if err := queue.Push(Job{ID: id, Work: msParam(r)}); err != nil {
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprintf(w, "queued job %d\n", id)
	})

	return mux
}

// msParam reads ?ms=, falling back to one second if it's missing or invalid.
func msParam(r *http.Request) time.Duration {
	ms, err := strconv.Atoi(r.URL.Query().Get("ms"))
	if err != nil || ms < 0 {
		return time.Second
	}
	return time.Duration(ms) * time.Millisecond
}
