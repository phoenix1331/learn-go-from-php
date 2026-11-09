// LESSON 21 — LARAVEL <-> GO QUEUE WORKER
//
// Run with:  go run ./21-queue-worker
// Test with: go test -race -v ./21-queue-worker
//
// The tour needs no Redis: it starts miniredis, a Redis server written in Go,
// inside the program. To run a real worker that Laravel can talk to:
//
//	go run ./21-queue-worker -serve                     # needs Redis on :6379
//	go run ./21-queue-worker -serve -embedded           # or bring its own
//
// The files:
//   - queue.go    the Redis operations, and what makes them crash-safe
//   - worker.go   the equivalent of `php artisan queue:work`
//   - main.go     this tour and the job handlers
//   - laravel/    the PHP side, to copy into your app
//
// THE FLOW
//
//	Laravel                          Redis                       Go
//	GoQueue::push('sum', [...]) ---> go:jobs -----------------> Reserve
//	                                 go:jobs:processing:w1 <--- (in progress)
//	queue:work <-------------------- queues:go-results <------- Finish
//	GoResultHandler::handle()
//
// WHY NOT A NORMAL LARAVEL JOB? When you dispatch() a job, Laravel stores
// the class with PHP's serialize():
//
//	"command":"O:16:\"App\\Jobs\\Resize\":1:{s:4:\"path\";s:5:\"a.png\";}"
//
// Go would have to decode PHP's private format, and renaming a PHP class
// would break it. So the two languages share a small JSON contract instead.
// The way back is neater: Go writes the JSON envelope Laravel's own queue
// expects, so results arrive through a plain `queue:work`.
//
// AT-LEAST-ONCE. A crash can land after the work but before Finish, and
// then the job runs again. No queue can fully prevent that, Laravel's
// included. So handlers need to be safe to run twice, and GoResultHandler
// should cope with seeing the same job_id twice.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	serve := flag.Bool("serve", false, "run a worker until Ctrl+C, instead of the tour")
	embedded := flag.Bool("embedded", false, "with -serve, start an in-memory Redis on -redis")
	addr := flag.String("redis", "localhost:6379", "Redis address")
	// Check yours with: php artisan tinker --execute="echo config('database.redis.options.prefix');"
	prefix := flag.String("prefix", "laravel-database-", "Laravel's Redis key prefix")
	name := flag.String("name", "worker-1", "unique, stable worker name")
	concurrency := flag.Int("concurrency", 4, "jobs to run at once")
	grace := flag.Duration("grace", 10*time.Second, "time for in-flight jobs after a signal")
	flag.Parse()

	if !*serve {
		return tour()
	}

	if *embedded {
		m := miniredis.NewMiniRedis()
		if err := m.StartAddr(*addr); err != nil {
			return err
		}
		defer m.Close()
	}

	rdb := redis.NewClient(&redis.Options{Addr: *addr})
	defer rdb.Close()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	context.AfterFunc(ctx, stop) // a second Ctrl+C kills it (lesson 19)

	// Fail now with a clear message rather than logging errors forever.
	if err := rdb.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("connecting to redis at %s: %w", *addr, err)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	logger.Info("worker started", "name", *name, "queue", NewKeys(*prefix, *name).Jobs)

	worker := newWorker(NewQueue(rdb, *prefix, *name), *name, *concurrency, *grace, DefaultBackoff, logger)
	return worker.Run(ctx)
}

func newWorker(q *Queue, name string, concurrency int, grace time.Duration, backoff func(int) time.Duration, logger *slog.Logger) *Worker {
	return &Worker{
		Queue:       q,
		Handlers:    handlers(),
		Name:        name,
		Concurrency: concurrency,
		JobTimeout:  time.Minute,
		Grace:       grace,
		Block:       time.Second,
		Backoff:     backoff,
		Logger:      logger,
	}
}

// handlers is the equivalent of your app/Jobs folder.
func handlers() map[string]Handler {
	return map[string]Handler{
		// The kind of number-crunching you'd move out of PHP.
		"sum": func(ctx context.Context, job Job) (any, error) {
			var in struct {
				Numbers []float64 `json:"numbers"`
			}
			if err := json.Unmarshal(job.Payload, &in); err != nil {
				// Bad input will be just as bad next time: don't retry.
				return nil, Permanent(fmt.Errorf("bad payload: %w", err))
			}
			total := 0.0
			for _, n := range in.Numbers {
				total += n
			}
			return map[string]any{"total": total, "count": len(in.Numbers)}, nil
		},

		// Fails the first time, like an API having a bad moment.
		"flaky": func(ctx context.Context, job Job) (any, error) {
			if job.Attempts == 0 {
				return nil, errors.New("upstream timed out")
			}
			return "worked on a later attempt", nil
		},

		// Takes a while, and respects ctx, as every handler must.
		"sleep": func(ctx context.Context, job Job) (any, error) {
			var in struct {
				MS int `json:"ms"`
			}
			if err := json.Unmarshal(job.Payload, &in); err != nil {
				return nil, Permanent(fmt.Errorf("bad payload: %w", err))
			}
			select {
			case <-time.After(time.Duration(in.MS) * time.Millisecond):
				return "slept", nil
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		},
	}
}

func tour() error {
	ctx := context.Background()

	m, err := miniredis.Run()
	if err != nil {
		return err
	}
	defer m.Close()
	rdb := redis.NewClient(&redis.Options{Addr: m.Addr()})
	defer rdb.Close()

	const prefix = "laravel-database-"
	keys := NewKeys(prefix, "w1")
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	}))

	// ---------------------------------------------------------------
	// 1. WHAT LARAVEL SENDS
	// ---------------------------------------------------------------
	fmt.Println("1. A job on the wire (this is what GoQueue::push writes)")
	first := `{"id":"job-1","type":"sum","payload":{"numbers":[1,2,3.5]},"attempts":0,"max_attempts":3}`
	if err := rdb.RPush(ctx, keys.Jobs, first).Err(); err != nil {
		return err
	}
	fmt.Printf("    RPUSH %s\n    %s\n", keys.Jobs, first)

	// ---------------------------------------------------------------
	// 2. RESERVING
	// ---------------------------------------------------------------
	fmt.Println("\n2. Reserving moves the job, it never leaves Redis")
	q := NewQueue(rdb, prefix, "w1")
	counts := func() string {
		return fmt.Sprintf("jobs=%d processing=%d delayed=%d failed=%d results=%d",
			rdb.LLen(ctx, keys.Jobs).Val(), rdb.LLen(ctx, keys.Processing).Val(),
			rdb.ZCard(ctx, keys.Delayed).Val(), rdb.LLen(ctx, keys.Failed).Val(),
			rdb.LLen(ctx, keys.Results).Val())
	}
	fmt.Println("    before:", counts())
	job, ok, err := q.Reserve(ctx, time.Second)
	if err != nil || !ok {
		return fmt.Errorf("reserve: ok=%v err=%v", ok, err)
	}
	fmt.Println("    after: ", counts(), "| got", job.ID)

	// ---------------------------------------------------------------
	// 3. A CRASH
	// ---------------------------------------------------------------
	fmt.Println("\n3. The worker dies here, then restarts")
	// We just walk away from the job, as a kill -9 would. With BLPOP this
	// job would now be lost.
	restarted := NewQueue(rdb, prefix, "w1")
	n, err := restarted.Recover(ctx)
	if err != nil {
		return err
	}
	fmt.Printf("    recovered %d job: %s\n", n, counts())

	// ---------------------------------------------------------------
	// 4. EVERY OUTCOME
	// ---------------------------------------------------------------
	fmt.Println("\n4. A worker meets good, flaky and bad jobs")
	for _, raw := range []string{
		`{"id":"job-2","type":"flaky","payload":{},"attempts":0,"max_attempts":3}`,
		`{"id":"job-3","type":"resize","payload":{},"attempts":0,"max_attempts":3}`,
		// PHP's json_encode([]) gives [] not {}, which can't fill a Go
		// struct. It's why GoQueue::push casts the payload to an object.
		`{"id":"job-4","type":"sum","payload":[],"attempts":0,"max_attempts":3}`,
		`this is not JSON`,
	} {
		if err := rdb.RPush(ctx, keys.Jobs, raw).Err(); err != nil {
			return err
		}
	}

	quick := func(int) time.Duration { return 100 * time.Millisecond }
	workerCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	worker := newWorker(q, "w1", 2, time.Second, quick, logger)
	done := make(chan error, 1)
	go func() { done <- worker.Run(workerCtx) }()

	// job-1, job-2, job-3 and job-4 each report one result.
	if err := waitForResults(ctx, rdb, keys.Results, 4); err != nil {
		return err
	}
	cancel()
	if err := <-done; err != nil {
		return err
	}
	fmt.Println("    final:", counts())

	// ---------------------------------------------------------------
	// 5. WHAT LARAVEL RECEIVES
	// ---------------------------------------------------------------
	fmt.Println("\n5. The results, in Laravel's own job format")
	raws, err := rdb.LRange(ctx, keys.Results, 0, -1).Result()
	if err != nil {
		return err
	}
	for i, raw := range raws {
		// Declared inside the loop on purpose. Unmarshal leaves fields that
		// aren't in the JSON untouched, so a reused variable would keep the
		// previous result's output.
		var sample struct {
			Job  string `json:"job"`
			Data Result `json:"data"`
		}
		if err := json.Unmarshal([]byte(raw), &sample); err != nil {
			return err
		}
		if i == 0 {
			fmt.Println("    each one calls:", sample.Job)
		}
		d := sample.Data
		fmt.Printf("    %-6s %-6s attempts=%d output=%s error=%q\n", d.JobID, d.Status, d.Attempts, string(d.Output), d.Error)
	}

	// ---------------------------------------------------------------
	// 6. SHUTTING DOWN WITH JOBS RUNNING
	// ---------------------------------------------------------------
	fmt.Println("\n6. Shutdown: one job finishes in time, one doesn't")
	rdb.Del(ctx, keys.Results, keys.ResultsNotify, keys.Failed)
	for _, raw := range []string{
		`{"id":"short","type":"sleep","payload":{"ms":100},"attempts":0,"max_attempts":3}`,
		`{"id":"long","type":"sleep","payload":{"ms":60000},"attempts":0,"max_attempts":3}`,
	} {
		rdb.RPush(ctx, keys.Jobs, raw)
	}
	workerCtx, cancel = context.WithCancel(ctx)
	defer cancel()
	worker = newWorker(q, "w1", 2, 300*time.Millisecond, quick, logger)
	go func() { done <- worker.Run(workerCtx) }()

	time.Sleep(50 * time.Millisecond) // let both jobs start
	fmt.Println("    --- signal ---")
	cancel()
	if err := <-done; err != nil {
		return err
	}
	fmt.Println("    final:", counts())
	fmt.Println("    long job is back on the queue, attempts unused")
	return nil
}

func waitForResults(ctx context.Context, rdb *redis.Client, key string, want int64) error {
	deadline := time.Now().Add(10 * time.Second)
	for rdb.LLen(ctx, key).Val() < want {
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %d results", want)
		}
		time.Sleep(10 * time.Millisecond)
	}
	return nil
}

// GOING FURTHER
//   - STREAMS: Redis Streams (XADD, XREADGROUP, XAUTOCLAIM) have consumer
//     groups and acknowledgements built in. Lists are simpler, and they're
//     what Laravel's queue uses.
//   - ABANDONED WORKERS: if a worker is retired forever, nothing recovers its
//     processing list. Use fixed names (Supervisor's %(process_num)02d) or
//     have a heartbeat key and let others claim the list when it expires.
//   - POISON JOBS: Recover doesn't count an attempt, so a job that kills
//     the whole process (out of memory, say) loops forever. Count attempts
//     in Reserve if your jobs could do that.
//   - HIGH TRAFFIC: Finish uses LREM, which scans the list. That's fine while
//     the processing list is short, as it is here (at most Concurrency long).
//   - SECURITY: Redis is effectively code execution for a queue worker. Keep
//     it off the public internet and set a password.
//
// THINGS TO TRY
//   - In Worker.Run, replace `w.Queue.Recover(ctx)` with `0, error(nil)`.
//     TestWorkerRecoversCrashedJobs fails: the job is stuck forever.
//   - Delete the "attempts" line from laravelPayload, then run Laravel's
//     queue:work against it. The Lua error is in storage/logs/laravel.log.
//   - In the "sum" handler's bad payload branch, return the plain error
//     without Permanent(...) and watch job-4 use up all of its attempts.
//   - Follow the Laravel steps in the README and watch a result come back.
