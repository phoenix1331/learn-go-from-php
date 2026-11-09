package main

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"
)

// Job is a unit of background work. Work says how long it pretends to take.
type Job struct {
	ID   int
	Work time.Duration
}

var (
	ErrQueueFull   = errors.New("queue full")
	ErrQueueClosed = errors.New("queue closed")
)

// Queue is an in-memory job queue that is safe to close while other
// goroutines are still pushing.
//
// Why not a bare channel? Sending on a closed channel PANICS, and with many
// HTTP handlers sending there's no safe moment to call close() without
// something like this lock. Handlers hold the read lock while sending; Close
// takes the write lock, so it can never land in the middle of a send.
type Queue struct {
	mu     sync.RWMutex
	closed bool
	jobs   chan Job
}

func NewQueue(size int) *Queue {
	return &Queue{jobs: make(chan Job, size)}
}

// Push adds a job without blocking. A full queue is reported straight away,
// so the caller can answer 503 instead of hanging (and so Close never has
// to wait behind a blocked sender).
func (q *Queue) Push(j Job) error {
	q.mu.RLock()
	defer q.mu.RUnlock()
	if q.closed {
		return ErrQueueClosed
	}
	select {
	case q.jobs <- j:
		return nil
	default:
		return ErrQueueFull
	}
}

// Jobs is the receiving end, for the workers.
func (q *Queue) Jobs() <-chan Job { return q.jobs }

// Close stops new pushes. Jobs already in the queue can still be received,
// and once they run out, receivers see ok == false. Calling it twice is safe.
func (q *Queue) Close() {
	q.mu.Lock()
	defer q.mu.Unlock()
	if !q.closed {
		q.closed = true
		close(q.jobs)
	}
}

// Leftover returns the jobs nobody started. Call it only after Close, once
// the workers have stopped: on an open queue it would block forever.
func (q *Queue) Leftover() []Job {
	var left []Job
	for j := range q.jobs {
		left = append(left, j)
	}
	return left
}

// Stats is what a Pool got through.
type Stats struct {
	Done    int64
	Aborted int64 // Handle returned an error. Here, only aborts do that.
}

// Pool runs jobs on a fixed number of goroutines.
type Pool struct {
	Workers int
	Grace   time.Duration // how long in-flight jobs get after ctx is cancelled
	Handle  func(ctx context.Context, j Job) error
}

// Run blocks until the workers have stopped. They stop when
//   - jobs is closed and empty (everything got done), or
//   - ctx is cancelled (stop taking jobs, finish the ones in hand).
//
// THE KEY IDEA: shutting down needs TWO separate signals.
//
//	ctx      "stop picking up new work"       (fires at the signal)
//	jobCtx   "abandon the work you're doing"  (fires only if Grace runs out)
//
// If jobs ran on ctx, Ctrl+C would cancel every in-flight database query
// and API call instantly. That's the opposite of graceful.
func (p *Pool) Run(ctx context.Context, jobs <-chan Job) Stats {
	// WithoutCancel (lesson 14's family) keeps ctx's values but not its
	// cancellation, so jobCtx lives on after ctx is cancelled.
	jobCtx, abort := context.WithCancel(context.WithoutCancel(ctx))
	defer abort()

	var done, aborted atomic.Int64
	var wg sync.WaitGroup
	for range p.Workers {
		wg.Go(func() {
			for {
				// select picks RANDOMLY when several cases are ready. Without
				// this first check, a worker could keep taking jobs after
				// the signal, for as long as the queue has some.
				select {
				case <-ctx.Done():
					return
				default:
				}

				select {
				case <-ctx.Done():
					return
				case job, ok := <-jobs:
					if !ok {
						return // closed and empty
					}
					if err := p.Handle(jobCtx, job); err != nil {
						aborted.Add(1)
					} else {
						done.Add(1)
					}
				}
			}
		})
	}

	// wg.Wait can't go in a select, so turn "finished" into a channel.
	finished := make(chan struct{})
	go func() {
		wg.Wait()
		close(finished)
	}()

	// Wait for the workers to run out of jobs, or for the signal.
	select {
	case <-finished:
	case <-ctx.Done():
		// The signal came. In-flight jobs get Grace to finish.
		select {
		case <-finished:
		case <-time.After(p.Grace):
			abort()
			// Go can't kill a goroutine: a job that ignores ctx blocks here
			// until the platform sends SIGKILL. Always honour ctx in jobs.
			<-finished
		}
	}
	return Stats{Done: done.Load(), Aborted: aborted.Load()}
}
