// LESSON 14 — CONTEXT: timeouts, cancellation, and passing them through calls
//
// Run with:  go run ./14-context
// Test with: go test -v ./14-context
//
// WHY THIS EXISTS. A PHP request is short-lived: if it runs too long,
// max_execution_time kills the whole process and everything is cleaned up.
// A Go server is one long-running process handling thousands of requests
// in goroutines. Nothing kills a slow goroutine for you. If a user closes
// their browser, or a database call hangs, you need a way to tell every
// goroutine working on that request to give up.
//
// That's what context.Context is: a value you pass down through your calls
// that carries
//   - a CANCELLATION signal  (ctx.Done() closes when it's time to stop)
//   - an optional DEADLINE   (cancel automatically after N seconds)
//   - a few request-scoped VALUES (request ID, trace ID — use sparingly)
//
// THE GOLDEN RULES
//  1. Functions that do I/O or slow work take `ctx context.Context` as their
//     FIRST parameter, named ctx.
//  2. Pass it down; never store it in a struct.
//  3. Whoever creates a context with a cancel func must call cancel
//     (almost always `defer cancel()`).
//  4. Never pass nil. If you have nothing better, use context.Background().
//
// Rough Laravel comparison: Http::timeout(5), DB query timeouts, and
// set_time_limit() all rolled into one value that EVERY layer respects.
package main

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// slowOperation stands in for a database query or API call that takes d.
// It's the core pattern of this lesson: wait for EITHER the work to finish
// OR the context to be cancelled, whichever comes first.
func slowOperation(ctx context.Context, name string, d time.Duration) (string, error) {
	select {
	case <-time.After(d):
		return name + " ok", nil
	case <-ctx.Done():
		// ctx.Err() says WHY it stopped:
		//   context.Canceled          — someone called cancel()
		//   context.DeadlineExceeded  — the timeout passed
		return "", fmt.Errorf("%s: %w", name, ctx.Err())
	}
}

func main() {
	// context.Background() is the empty root context: never cancelled, no
	// deadline, no values. Every context tree starts from one of these
	// (in main, in tests via t.Context(), or in HTTP handlers via r.Context()).
	root := context.Background()

	// ---------------------------------------------------------------
	// 1. TIMEOUT — give up after a fixed time
	// ---------------------------------------------------------------
	fmt.Println("1. WithTimeout")
	{
		// WithTimeout returns a CHILD context and a cancel func.
		// The child is cancelled automatically after 100ms, or earlier
		// if we call cancel(). `defer cancel()` frees its timer right away
		// instead of waiting for the timeout. `go vet` warns if you forget it.
		ctx, cancel := context.WithTimeout(root, 100*time.Millisecond)
		defer cancel()

		res, err := slowOperation(ctx, "fast query", 20*time.Millisecond)
		fmt.Println("   ", res, err)

		res, err = slowOperation(ctx, "slow query", 500*time.Millisecond)
		fmt.Printf("    %q %v\n", res, err)

		// Because we wrapped with %w, errors.Is still sees the cause.
		if errors.Is(err, context.DeadlineExceeded) {
			fmt.Println("    -> timed out (would be a 504 in an HTTP handler)")
		}
	}

	// ---------------------------------------------------------------
	// 2. MANUAL CANCEL — first answer wins, stop the rest
	// ---------------------------------------------------------------
	fmt.Println("\n2. WithCancel: ask three mirrors, keep the fastest")
	{
		res, err := firstResult(root, map[string]time.Duration{
			"mirror-eu": 60 * time.Millisecond,
			"mirror-us": 20 * time.Millisecond,
			"mirror-ap": 90 * time.Millisecond,
		})
		fmt.Println("    winner:", res, err)
	}

	// ---------------------------------------------------------------
	// 3. PASSING IT THROUGH LAYERS — handler -> service -> queries
	// ---------------------------------------------------------------
	fmt.Println("\n3. One deadline shared by every layer")
	{
		err := handleRequest(root, 7)
		fmt.Println("    handler got:", err)
	}

	// ---------------------------------------------------------------
	// 4. LONG LOOPS — check ctx.Err() between items
	// ---------------------------------------------------------------
	fmt.Println("\n4. Stopping a batch job part-way")
	{
		rows := make([]string, 20)
		ctx, cancel := context.WithTimeout(root, 110*time.Millisecond)
		defer cancel()

		done, err := processBatch(ctx, rows)
		fmt.Printf("    processed %d rows, err: %v\n", done, err)
	}

	// ---------------------------------------------------------------
	// 5. THE TREE — cancelling a parent cancels all its children
	// ---------------------------------------------------------------
	fmt.Println("\n5. Parents and children")
	{
		parent, cancelParent := context.WithCancel(root)
		child, cancelChild := context.WithTimeout(parent, time.Hour)
		defer cancelChild()

		cancelParent() // child's 1-hour timeout is irrelevant now
		fmt.Println("    parent:", parent.Err(), "| child:", child.Err())

		// It only flows DOWN. Cancelling a child never affects its parent.
		p2, cancelP2 := context.WithCancel(root)
		defer cancelP2()
		c2, cancelC2 := context.WithCancel(p2)
		cancelC2()
		fmt.Println("    child cancelled:", c2.Err(), "| parent still:", p2.Err())

		// WithoutCancel: a child that IGNORES its parent's cancellation but
		// keeps its values. Useful for "finish writing this audit log even
		// though the request has ended".
		detached := context.WithoutCancel(parent)
		fmt.Println("    detached from cancelled parent:", detached.Err())
	}

	// ---------------------------------------------------------------
	// 6. CAUSES — say WHY something was cancelled
	// ---------------------------------------------------------------
	fmt.Println("\n6. Cancellation causes")
	{
		// ctx.Err() is always one of the two generic errors. A cause lets
		// you attach your own, read back with context.Cause(ctx).
		errSlowPayments := errors.New("payment API took longer than 30ms")
		ctx, cancel := context.WithTimeoutCause(root, 30*time.Millisecond, errSlowPayments)
		defer cancel()
		<-ctx.Done() // block until it fires
		fmt.Println("    Err:  ", ctx.Err())
		fmt.Println("    Cause:", context.Cause(ctx))

		errUserLeft := errors.New("user closed the page")
		ctx2, cancel2 := context.WithCancelCause(root)
		cancel2(errUserLeft)
		fmt.Println("    Err:  ", ctx2.Err())
		fmt.Println("    Cause:", context.Cause(ctx2))
	}
}

// firstResult races several slow operations and returns the first success.
func firstResult(ctx context.Context, mirrors map[string]time.Duration) (string, error) {
	// Defers run last-in, first-out. This order means: on return, cancel()
	// runs FIRST (telling losers to stop), THEN wg.Wait() waits for them
	// to exit. Swap the two lines and wg.Wait() would sit waiting for the
	// slowest mirror to finish.
	var wg sync.WaitGroup
	defer wg.Wait()
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Buffered so a goroutine can send its result and exit even if nobody
	// is reading any more. An unbuffered channel here would leak goroutines.
	results := make(chan string, len(mirrors))

	for name, d := range mirrors {
		wg.Go(func() {
			res, err := slowOperation(ctx, name, d)
			if err != nil {
				fmt.Println("    stopped early:", err)
				return
			}
			results <- res
		})
	}

	select {
	case res := <-results:
		return res, nil // the deferred cancel() stops the losers
	case <-ctx.Done():
		return "", ctx.Err() // our caller gave up first
	}
}

// --- Layers: each one takes ctx first and passes it on -------------------

// requestIDKey is an unexported type used as a context key. Using your own
// type (not a plain string) means no other package can collide with it.
type requestIDKey struct{}

func handleRequest(ctx context.Context, userID int) error {
	// In a real server, ctx would be r.Context(), which Go cancels for you
	// when the client disconnects. We add a request ID and a 150ms budget.
	ctx = context.WithValue(ctx, requestIDKey{}, "req-42")
	ctx, cancel := context.WithTimeout(ctx, 150*time.Millisecond)
	defer cancel()

	return loadDashboard(ctx, userID)
}

func loadDashboard(ctx context.Context, userID int) error {
	logf(ctx, "loading dashboard for user %d", userID)

	profile, err := slowOperation(ctx, "profile query", 60*time.Millisecond)
	if err != nil {
		return fmt.Errorf("loadDashboard: %w", err)
	}
	logf(ctx, "%s", profile)

	// The deadline is shared, so each step gets whatever time is LEFT.
	// No need to work out per-query timeouts by hand.
	if deadline, ok := ctx.Deadline(); ok {
		logf(ctx, "time left: about %v", time.Until(deadline).Round(10*time.Millisecond))
	}

	orders, err := slowOperation(ctx, "orders query", 200*time.Millisecond)
	if err != nil {
		return fmt.Errorf("loadDashboard: %w", err)
	}
	logf(ctx, "%s", orders)
	return nil
}

// logf reads the request ID back out of the context.
//
// ctx.Value returns `any`, so we type-assert with "comma ok" (lesson 06).
// CONTEXT VALUES ARE NOT A BAG FOR PARAMETERS. Only put request-scoped
// metadata in them (request IDs, auth info, tracing). Real inputs like
// userID belong in normal function arguments where the compiler checks them.
func logf(ctx context.Context, format string, args ...any) {
	id, ok := ctx.Value(requestIDKey{}).(string)
	if !ok {
		id = "no-request-id"
	}
	fmt.Printf("    [%s] "+format+"\n", append([]any{id}, args...)...)
}

// processBatch handles rows one at a time, checking for cancellation before
// each one. Code that doesn't block on a channel can't use select, so it
// polls ctx.Err() instead. It returns how many rows it finished, so the
// caller knows where to resume. (We'll use this in the CSV importer.)
func processBatch(ctx context.Context, rows []string) (int, error) {
	for i := range rows {
		if err := ctx.Err(); err != nil {
			return i, fmt.Errorf("stopped after %d of %d rows: %w", i, len(rows), err)
		}
		time.Sleep(20 * time.Millisecond) // pretend to do the work
	}
	return len(rows), nil
}

// THINGS TO TRY
//   - Section 3: raise the handler's timeout to 300ms and watch the orders
//     query succeed.
//   - In firstResult, swap the `defer wg.Wait()` and `defer cancel()` lines.
//     Section 2 now waits for the slowest mirror (90ms) instead of the
//     fastest (20ms), and the losers finish instead of stopping early.
//   - In section 1, change `ctx, cancel :=` to `ctx, _ :=`, delete its
//     `defer cancel()`, and run `go vet ./14-context` to see the leak warning.
//   - Run the tests with the race detector: go test -race -v ./14-context
//
// COMING UP: lesson 16 sends ctx to real HTTP calls with
// http.NewRequestWithContext, and lesson 19 uses signal.NotifyContext to
// cancel a context when you press Ctrl+C.
