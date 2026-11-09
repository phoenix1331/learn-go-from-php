package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// Job is the contract with Laravel, and the only thing the two sides share.
// It's deliberately plain JSON. See "WHY NOT A NORMAL LARAVEL JOB?" in main.go.
type Job struct {
	ID          string          `json:"id"`
	Type        string          `json:"type"`
	Payload     json.RawMessage `json:"payload"`
	Attempts    int             `json:"attempts"` // failed tries so far
	MaxAttempts int             `json:"max_attempts"`

	// raw is the exact text we popped. LREM removes by value, so finishing
	// a job needs the original bytes, not a re-encoded copy.
	raw string
}

// Result is what goes back to Laravel, as the `$data` array in
// GoResultHandler::handle (see the laravel/ folder).
type Result struct {
	JobID      string          `json:"job_id"`
	Type       string          `json:"type"`
	Status     string          `json:"status"` // "done" or "failed"
	Output     json.RawMessage `json:"output,omitempty"`
	Error      string          `json:"error,omitempty"`
	Attempts   int             `json:"attempts"`
	Worker     string          `json:"worker"`
	FinishedAt time.Time       `json:"finished_at"`
}

// ErrMalformed means a queue entry couldn't be understood. It has been moved
// to the failed list, because retrying it would never help.
var ErrMalformed = errors.New("malformed job")

// Keys are the Redis keys we use. The prefix is the most common thing to get
// wrong: Laravel silently adds database.redis.options.prefix to every key,
// so Redis::rpush('go:jobs', ...) really writes "laravel-database-go:jobs".
// Go has to use that full name, or the worker waits forever on an empty list.
type Keys struct {
	Jobs          string // waiting jobs: Laravel RPUSHes here
	Processing    string // reserved by this worker
	Delayed       string // waiting for a retry: sorted set, score = due time
	Failed        string // gave up: kept for a human to look at
	Results       string // a queue that Laravel's own worker reads
	ResultsNotify string // Laravel's blocking pop watches this
}

func NewKeys(prefix, worker string) Keys {
	return Keys{
		Jobs:          prefix + "go:jobs",
		Processing:    prefix + "go:jobs:processing:" + worker,
		Delayed:       prefix + "go:jobs:delayed",
		Failed:        prefix + "go:jobs:failed",
		Results:       prefix + "queues:go-results",
		ResultsNotify: prefix + "queues:go-results:notify",
	}
}

// Queue is the Redis side of the worker.
type Queue struct {
	rdb    *redis.Client
	keys   Keys
	worker string
	now    func() time.Time // a field so tests can control retry timing
}

func NewQueue(rdb *redis.Client, prefix, worker string) *Queue {
	return &Queue{rdb: rdb, keys: NewKeys(prefix, worker), worker: worker, now: time.Now}
}

// Reserve waits up to block for a job and moves it to our processing list.
//
// THE KEY IDEA. A plain BLPOP removes the job from Redis, so if the worker
// dies a moment later the job is gone forever. BLMOVE takes it off one list
// and puts it on another as a single atomic step, so at every instant the
// job is in Redis somewhere. (Laravel does the same with its :reserved set.)
//
// ok is false if nothing arrived in time. Note that go-redis rounds block
// up to a minimum of one second.
func (q *Queue) Reserve(ctx context.Context, block time.Duration) (job Job, ok bool, err error) {
	raw, err := q.rdb.BLMove(ctx, q.keys.Jobs, q.keys.Processing, "LEFT", "RIGHT", block).Result()
	if errors.Is(err, redis.Nil) {
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("reserving job: %w", err)
	}

	if err := json.Unmarshal([]byte(raw), &job); err != nil {
		return Job{}, false, q.bury(ctx, raw, err.Error())
	}
	if job.ID == "" || job.Type == "" {
		return Job{}, false, q.bury(ctx, raw, "id and type are required")
	}
	job.raw = raw
	return job, true, nil
}

// failedEntry is what the failed list holds. Keeping raw means a person can
// see exactly what was sent, and push it back after a fix.
type failedEntry struct {
	Raw      string    `json:"raw"`
	Reason   string    `json:"reason"`
	FailedAt time.Time `json:"failed_at"`
}

func (q *Queue) failedJSON(raw, reason string) ([]byte, error) {
	return json.Marshal(failedEntry{Raw: raw, Reason: reason, FailedAt: q.now()})
}

// bury moves something unreadable to the failed list. There's no job ID to
// report back to Laravel with, so that list is all there is.
func (q *Queue) bury(ctx context.Context, raw, reason string) error {
	entry, err := q.failedJSON(raw, reason)
	if err != nil {
		return err
	}
	_, err = q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.RPush(ctx, q.keys.Failed, entry)
		p.LRem(ctx, q.keys.Processing, 1, raw)
		return nil
	})
	if err != nil {
		return fmt.Errorf("burying job: %w", err)
	}
	return fmt.Errorf("%w: %s", ErrMalformed, reason)
}

// Finish reports the result and removes the job, in one MULTI/EXEC
// transaction (like DB::transaction). Without that, a crash between the two
// steps could report a job that then runs again, or lose a result.
func (q *Queue) Finish(ctx context.Context, job Job, r Result) error {
	payload, err := laravelPayload(r)
	if err != nil {
		return err
	}
	var entry []byte
	if r.Status == "failed" {
		if entry, err = q.failedJSON(job.raw, r.Error); err != nil {
			return err
		}
	}

	_, err = q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		// The same two pushes as LuaScripts::push() in Laravel's source.
		p.RPush(ctx, q.keys.Results, payload)
		p.RPush(ctx, q.keys.ResultsNotify, 1)
		if entry != nil {
			p.RPush(ctx, q.keys.Failed, entry)
		}
		p.LRem(ctx, q.keys.Processing, 1, job.raw)
		return nil
	})
	if err != nil {
		return fmt.Errorf("finishing job %s: %w", job.ID, err)
	}
	return nil
}

// laravelPayload wraps a Result in the JSON envelope Laravel's queue expects,
// so a normal `php artisan queue:work` picks it up. No polling code is needed
// in PHP. Laravel's Job::fire() parses "job" as Class@method and calls it
// with ($job, $data).
func laravelPayload(r Result) ([]byte, error) {
	id := uuid.NewString()
	return json.Marshal(map[string]any{
		"uuid":        id,
		"id":          id,
		"displayName": "GoJobResult",
		"job":         `App\Queue\GoResultHandler@handle`,
		"data":        r,
		// Required: Laravel's pop script does attempts + 1 on this field,
		// and a missing value is a Lua error.
		"attempts": 0,
	})
}

// Retry schedules another attempt, like $this->release($delay).
func (q *Queue) Retry(ctx context.Context, job Job, delay time.Duration) error {
	next := job
	next.Attempts++
	encoded, err := json.Marshal(next)
	if err != nil {
		return err
	}
	due := float64(q.now().Add(delay).UnixMilli())

	_, err = q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.ZAdd(ctx, q.keys.Delayed, redis.Z{Score: due, Member: encoded})
		p.LRem(ctx, q.keys.Processing, 1, job.raw)
		return nil
	})
	if err != nil {
		return fmt.Errorf("retrying job %s: %w", job.ID, err)
	}
	return nil
}

// Release puts a job back at the front, unchanged. It's for jobs we had to
// abandon during shutdown: not the job's fault, so it doesn't use an attempt.
func (q *Queue) Release(ctx context.Context, job Job) error {
	_, err := q.rdb.TxPipelined(ctx, func(p redis.Pipeliner) error {
		p.LPush(ctx, q.keys.Jobs, job.raw)
		p.LRem(ctx, q.keys.Processing, 1, job.raw)
		return nil
	})
	if err != nil {
		return fmt.Errorf("releasing job %s: %w", job.ID, err)
	}
	return nil
}

// Recover puts back anything this worker left in its processing list, which
// only happens if the last run died. Call it once, before starting work.
//
// That's why each worker needs a unique, STABLE name: a new name would
// never find the old list, and another worker's name would steal live jobs.
func (q *Queue) Recover(ctx context.Context) (int, error) {
	n := 0
	for {
		err := q.rdb.LMove(ctx, q.keys.Processing, q.keys.Jobs, "RIGHT", "LEFT").Err()
		if errors.Is(err, redis.Nil) {
			return n, nil
		}
		if err != nil {
			return n, fmt.Errorf("recovering jobs: %w", err)
		}
		n++
	}
}

// promoteScript moves due retries back onto the main list. It's a Lua script
// because Redis runs one atomically, so two workers can't both move a job.
// Laravel's LuaScripts::migrateExpiredJobs() is the same idea.
var promoteScript = redis.NewScript(`
local due = redis.call('zrangebyscore', KEYS[1], '-inf', ARGV[1], 'limit', 0, 100)
for _, job in ipairs(due) do
	redis.call('rpush', KEYS[2], job)
	redis.call('zrem', KEYS[1], job)
end
return #due
`)

// PromoteDue moves retries whose time has come back onto the main list.
func (q *Queue) PromoteDue(ctx context.Context) (int, error) {
	keys := []string{q.keys.Delayed, q.keys.Jobs}
	n, err := promoteScript.Run(ctx, q.rdb, keys, q.now().UnixMilli()).Int()
	if err != nil {
		return 0, fmt.Errorf("promoting retries: %w", err)
	}
	return n, nil
}
