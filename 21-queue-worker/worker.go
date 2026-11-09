package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"
)

// Handler does the work for one job type. Like a Laravel job's handle().
// Whatever it returns becomes the "output" that Laravel receives.
type Handler func(ctx context.Context, job Job) (any, error)

// permanentError marks a failure that retrying can't fix.
type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

// Permanent is Go's version of $this->fail($e): don't retry this.
func Permanent(err error) error { return permanentError{err} }

const promoteEvery = 200 * time.Millisecond

// Worker is the Go equivalent of `php artisan queue:work`.
type Worker struct {
	Queue       *Queue
	Handlers    map[string]Handler
	Name        string
	Concurrency int
	JobTimeout  time.Duration // like --timeout
	Grace       time.Duration // time for in-flight jobs after a signal
	Block       time.Duration // how long each wait for a job lasts
	Backoff     func(attempt int) time.Duration
	Logger      *slog.Logger
}

// DefaultBackoff waits 1s, 2s, 4s... up to a minute.
func DefaultBackoff(attempt int) time.Duration {
	return min(time.Second<<min(attempt, 6), time.Minute)
}

// Run processes jobs until ctx is cancelled and shutdown has finished. It
// uses the two-context idea from lesson 19: ctx means "stop taking jobs" and
// jobCtx means "abandon the jobs you're doing".
func (w *Worker) Run(ctx context.Context) error {
	recovered, err := w.Queue.Recover(ctx)
	if err != nil {
		return err
	}
	if recovered > 0 {
		w.Logger.Warn("recovered jobs from a previous crash", "count", recovered)
	}

	jobCtx, abort := context.WithCancel(context.WithoutCancel(ctx))
	defer abort()

	var wg sync.WaitGroup
	for range w.Concurrency {
		wg.Go(func() { w.loop(ctx, jobCtx) })
	}
	wg.Go(func() { w.promoteLoop(ctx) })

	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	<-ctx.Done()
	w.Logger.Info("stopping, finishing jobs in hand", "grace", w.Grace)
	select {
	case <-finished:
	case <-time.After(w.Grace):
		abort()
		<-finished
	}
	w.Logger.Info("worker stopped")
	return nil
}

func (w *Worker) loop(ctx, jobCtx context.Context) {
	// Reserve doesn't get ctx. If shutdown cancelled it just as Redis handed
	// us a job, we'd never see the reply. Each wait is short anyway.
	reserveCtx := context.WithoutCancel(ctx)

	for ctx.Err() == nil {
		job, ok, err := w.Queue.Reserve(reserveCtx, w.Block)
		switch {
		case errors.Is(err, ErrMalformed):
			w.Logger.Error("job moved to failed list", "err", err)
		case err != nil:
			// Redis is down or restarting. Don't spin: wait and try again.
			w.Logger.Error("redis error", "err", err)
			select {
			case <-time.After(time.Second):
			case <-ctx.Done():
			}
		case ok:
			w.process(jobCtx, job)
		}
	}
}

func (w *Worker) promoteLoop(ctx context.Context) {
	ticker := time.NewTicker(promoteEvery)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := w.Queue.PromoteDue(ctx); err != nil && ctx.Err() == nil {
				w.Logger.Error("promoting retries", "err", err)
			}
		}
	}
}

// process runs one job and decides its fate.
func (w *Worker) process(jobCtx context.Context, job Job) {
	log := w.Logger.With("job", job.ID, "type", job.Type, "attempt", job.Attempts+1)

	ctx, cancel := context.WithTimeout(jobCtx, w.JobTimeout)
	defer cancel()
	output, jobErr := w.call(ctx, job)

	// Bookkeeping must still happen when jobCtx has been cancelled.
	store := context.Background()
	var err error
	switch {
	case jobErr == nil:
		log.Info("job done")
		err = w.Queue.Finish(store, job, w.result(job, "done", output, nil))

	case jobCtx.Err() != nil:
		// Shutdown cut it off: not the job's fault.
		log.Warn("job released for later")
		err = w.Queue.Release(store, job)

	case isPermanent(jobErr) || job.Attempts+1 >= job.MaxAttempts:
		log.Error("job failed", "err", jobErr)
		err = w.Queue.Finish(store, job, w.result(job, "failed", nil, jobErr))

	default:
		delay := w.Backoff(job.Attempts)
		log.Warn("job will retry", "err", jobErr, "delay", delay)
		err = w.Queue.Retry(store, job, delay)
	}

	if err != nil {
		// The job is still in our processing list, so it isn't lost: it runs
		// again after a restart. That's "at least once" delivery.
		log.Error("could not record job outcome", "err", err)
	}
}

// call runs the handler and encodes its output. A panic is turned into an
// error, so one bad job can't take down every other job in the process.
func (w *Worker) call(ctx context.Context, job Job) (output json.RawMessage, err error) {
	defer func() {
		if p := recover(); p != nil {
			err = fmt.Errorf("panic: %v", p)
		}
	}()

	handler, ok := w.Handlers[job.Type]
	if !ok {
		return nil, Permanent(fmt.Errorf("no handler for job type %q", job.Type))
	}
	value, err := handler(ctx, job)
	if err != nil {
		return nil, err
	}
	output, err = json.Marshal(value)
	if err != nil {
		return nil, Permanent(fmt.Errorf("encoding output: %w", err))
	}
	return output, nil
}

func (w *Worker) result(job Job, status string, output json.RawMessage, jobErr error) Result {
	r := Result{
		JobID:      job.ID,
		Type:       job.Type,
		Status:     status,
		Output:     output,
		Attempts:   job.Attempts + 1,
		Worker:     w.Name,
		FinishedAt: w.Queue.now().UTC(),
	}
	if jobErr != nil {
		r.Error = jobErr.Error()
	}
	return r
}

func isPermanent(err error) bool {
	var p permanentError
	return errors.As(err, &p)
}
