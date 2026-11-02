// LESSON 16 — HTTP CLIENT (part 1): calling APIs, timeouts, retries, limits
//
// Run with:  go run ./16-http-client
// Test with: go test -v ./16-http-client
//
// No internet needed: fakeapi.go starts a small local API for us to call.
//
// In Laravel you'd write:
//
//	Http::timeout(5)->retry(3, 100)->withToken($t)->post($url, $data)->throw()->json();
//
// Go's net/http has every piece of that, but nothing is switched on by
// default. This lesson builds each piece by hand so you know what a
// well-behaved client needs:
//
//  1. a TIMEOUT          (the default client has none, and will wait forever)
//  2. STATUS CHECKS      (a 404 or 500 is NOT an error in Go)
//  3. CLOSING THE BODY   (or connections leak)
//  4. CONTEXT            (so callers can cancel, lesson 14)
//  5. RETRIES            (for the failures worth retrying)
//  6. A CONCURRENCY LIMIT (fast, without flattening the other side)
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand/v2"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"time"
)

func main() {
	// Start the fake API on a random local port. srv.URL is something like
	// http://127.0.0.1:41234
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler())
	defer srv.Close()
	base := srv.URL
	ctx := context.Background()

	// ---------------------------------------------------------------
	// 1. BUILD A CLIENT WITH A TIMEOUT
	// ---------------------------------------------------------------
	// http.Get(url) uses http.DefaultClient, which has NO timeout. If the
	// server accepts the connection and then goes quiet, your goroutine
	// waits forever. Fine for a script, a slow leak in a server.
	//
	// Create one client and reuse it everywhere. It's safe for concurrent
	// use and keeps connections open between requests (like keep-alive in
	// Guzzle). Don't create a new client per request.
	client := &http.Client{Timeout: 5 * time.Second}

	// ---------------------------------------------------------------
	// 2. GET JSON
	// ---------------------------------------------------------------
	fmt.Println("1. GET JSON")
	user, err := getJSON[User](ctx, client, base+"/users/7")
	fmt.Printf("    %+v err=%v\n", user, err)

	// A 404 comes back with err == nil from client.Do. getJSON turns it into
	// a *StatusError so callers can't forget to check.
	_, err = getJSON[User](ctx, client, base+"/users/999")
	fmt.Println("    error:", err)
	var se *StatusError
	if errors.As(err, &se) && se.Code == http.StatusNotFound {
		fmt.Println("    -> handle as 'no such user', not as a crash")
	}

	// ---------------------------------------------------------------
	// 3. POST JSON WITH HEADERS
	// ---------------------------------------------------------------
	fmt.Println("\n2. POST JSON with a bearer token")
	order, err := createOrder(ctx, client, base, apiToken, OrderRequest{SKU: "GO-MUG", Quantity: 2})
	fmt.Printf("    %+v err=%v\n", order, err)

	_, err = createOrder(ctx, client, base, "wrong-token", OrderRequest{SKU: "GO-MUG", Quantity: 2})
	fmt.Println("    with a bad token:", err)

	// ---------------------------------------------------------------
	// 4. TIMEOUTS
	// ---------------------------------------------------------------
	fmt.Println("\n3. Timeouts (/slow takes 2s)")

	// Per-call deadline via context. Use this when the CALLER decides, e.g.
	// "this page must render in 300ms, whatever it's waiting on".
	shortCtx, cancel := context.WithTimeout(ctx, 200*time.Millisecond)
	defer cancel()
	start := time.Now()
	_, err = getJSON[map[string]string](shortCtx, client, base+"/slow")
	fmt.Printf("    context timeout after %v: %v\n", since(start), err)
	fmt.Println("    errors.Is DeadlineExceeded:", errors.Is(err, context.DeadlineExceeded))

	// Client-wide timeout. Covers connecting, sending, waiting for headers
	// AND reading the body. A good safety net for every call.
	impatient := &http.Client{Timeout: 100 * time.Millisecond}
	start = time.Now()
	_, err = getJSON[map[string]string](ctx, impatient, base+"/slow")
	fmt.Printf("    client timeout after %v: %v\n", since(start), err)

	// ---------------------------------------------------------------
	// 5. RETRIES WITH BACKOFF
	// ---------------------------------------------------------------
	fmt.Println("\n4. Retrying /flaky (503, 503, then 200)")
	res, err := withRetry(ctx, 4, 50*time.Millisecond, func(ctx context.Context) (map[string]string, error) {
		return getJSON[map[string]string](ctx, client, base+"/flaky")
	})
	fmt.Printf("    result %v err=%v after %d calls\n", res, err, api.flakyCalls.Load())

	fmt.Println("    retrying a 404 (should give up at once):")
	calls := 0
	_, err = withRetry(ctx, 4, 50*time.Millisecond, func(ctx context.Context) (User, error) {
		calls++
		return getJSON[User](ctx, client, base+"/users/999")
	})
	fmt.Printf("    %d call, err=%v\n", calls, err)

	// ---------------------------------------------------------------
	// 6. MANY REQUESTS, LIMITED CONCURRENCY
	// ---------------------------------------------------------------
	fmt.Println("\n5. Fetching 20 items (each takes 50ms)")
	ids := make([]int, 20)
	for i := range ids {
		ids[i] = i + 1
	}
	for _, limit := range []int{1, 5, 20} {
		api.maxInFlight.Store(0)
		start := time.Now()
		items, err := fetchAll(ctx, client, base, ids, limit)
		total := 0
		for _, it := range items {
			total += it.PricePence
		}
		fmt.Printf("    limit %2d: %-6v server saw max %2d at once, total %d pence, err=%v\n",
			limit, since(start), api.maxInFlight.Load(), total, err)
	}
}

// --- Building blocks --------------------------------------------------------

// StatusError is returned for any non-2xx response. It keeps the code and
// the start of the body, which often explains what went wrong.
type StatusError struct {
	Code int
	Body string
}

func (e *StatusError) Error() string {
	return fmt.Sprintf("HTTP %d: %s", e.Code, e.Body)
}

// doJSON sends req, checks the status, and decodes a JSON body into out.
func doJSON(c *http.Client, req *http.Request, out any) error {
	resp, err := c.Do(req)
	if err != nil {
		// Network-level failure: refused, DNS, timeout, cancelled.
		// There's no response, so nothing to close.
		return err
	}
	// ALWAYS close the body when err == nil, even if you don't read it.
	// Otherwise the connection can't be reused and you slowly leak them.
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		// io.LimitReader: never trust a server to send a small body.
		// We only want a snippet for the error message.
		snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return &StatusError{Code: resp.StatusCode, Body: strings.TrimSpace(string(snippet))}
	}

	// Decode straight from the stream (lesson 15), capped at 1 MB.
	return json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(out)
}

// getJSON is GENERIC (lesson 09): you choose the result type at the call
// site, getJSON[User](...), and get a typed value back. No casting from
// a map like PHP's json_decode($body, true).
func getJSON[T any](ctx context.Context, c *http.Client, url string) (T, error) {
	var out T
	// NewRequestWithContext ties the request to ctx: if ctx is cancelled
	// or times out, the request is aborted mid-flight.
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, err
	}
	req.Header.Set("Accept", "application/json")
	err = doJSON(c, req, &out)
	return out, err
}

func createOrder(ctx context.Context, c *http.Client, base, token string, o OrderRequest) (Order, error) {
	body, err := json.Marshal(o)
	if err != nil {
		return Order{}, err
	}
	// The body is an io.Reader (lesson 15). bytes.NewReader wraps a []byte.
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/orders", bytes.NewReader(body))
	if err != nil {
		return Order{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token) // Http::withToken($token)

	var out Order
	// Not `return out, doJSON(...)`: Go doesn't promise to read `out` AFTER
	// doJSON fills it in. Call first, then return.
	err = doJSON(c, req, &out)
	return out, err
}

// withRetry calls fn up to `attempts` times, waiting longer after each
// failure: base, 2×base, 4×base... plus a random extra ("jitter") so that
// many clients retrying at once don't all hit the server together.
//
// Only some failures are worth retrying:
//
//	network errors, 429, 5xx  -> maybe temporary, try again
//	other 4xx                 -> our request is wrong, retrying won't help
//	our own ctx is done       -> the caller has given up, stop now
//
// Retrying a POST is risky: if the first one reached the server, you might
// create two orders. Only retry requests that are safe to repeat (GET,
// PUT, DELETE) unless the API supports idempotency keys.
func withRetry[T any](ctx context.Context, attempts int, base time.Duration, fn func(context.Context) (T, error)) (T, error) {
	var zero T
	for attempt := 1; ; attempt++ {
		v, err := fn(ctx)
		if err == nil {
			return v, nil
		}
		if attempt == attempts || !retryable(err) || ctx.Err() != nil {
			return zero, err
		}

		delay := base << (attempt - 1) // 50ms, 100ms, 200ms... (<< doubles)
		delay += rand.N(delay/2 + 1)   // + up to 50% jitter
		fmt.Printf("    attempt %d failed (%v), retrying in %v\n", attempt, err, delay.Round(time.Millisecond))

		// Sleep, but wake up early if the caller cancels.
		select {
		case <-time.After(delay):
		case <-ctx.Done():
			return zero, ctx.Err()
		}
	}
}

func retryable(err error) bool {
	var se *StatusError
	if errors.As(err, &se) {
		return se.Code == http.StatusTooManyRequests || se.Code >= 500
	}
	return true // network errors
}

// fetchAll fetches every id with at most `limit` requests in flight.
//
// The SEMAPHORE pattern: a buffered channel with `limit` slots. Each
// goroutine puts a token in before its request and takes it out after.
// When all slots are full, the next send blocks until one frees up.
// (Laravel's Http::pool() sends everything at once, with no limit.)
//
// Each goroutine writes only to its OWN index of items and errs, so no
// mutex is needed and results stay in the same order as ids.
func fetchAll(ctx context.Context, c *http.Client, base string, ids []int, limit int) ([]Item, error) {
	items := make([]Item, len(ids))
	errs := make([]error, len(ids))
	sem := make(chan struct{}, limit)

	var wg sync.WaitGroup
	for i, id := range ids {
		wg.Go(func() {
			sem <- struct{}{}        // acquire a slot (blocks when full)
			defer func() { <-sem }() // release it when done
			items[i], errs[i] = getJSON[Item](ctx, c, fmt.Sprintf("%s/items/%d", base, id))
		})
	}
	wg.Wait()

	// errors.Join combines every non-nil error into one, or returns nil
	// if they were all nil.
	return items, errors.Join(errs...)
}

func since(t time.Time) time.Duration {
	return time.Since(t).Round(10 * time.Millisecond)
}

// GOING FURTHER
//   - Calling one host heavily? The default Transport keeps only 2 idle
//     connections per host, so most of your requests open new ones. Raise it:
//
//	client := &http.Client{
//	    Timeout: 10 * time.Second,
//	    Transport: &http.Transport{
//	        MaxIdleConnsPerHost: 20,
//	        IdleConnTimeout:     90 * time.Second,
//	    },
//	}
//
//   - golang.org/x/sync/errgroup gives you WaitGroup + first error +
//     cancellation + SetLimit(n) in one type. Worth a look once the
//     semaphore pattern makes sense.
//
// THINGS TO TRY
//   - Remove `defer resp.Body.Close()` and run `go vet`. (It won't catch it.
//     Linters like bodyclose do, which is why people use them.)
//   - Point getJSON at http://127.0.0.1:1 (nothing listening) and see a
//     network error rather than a StatusError.
//   - In section 5, add a limit of 2 to the list and predict its time
//     before running (20 items × 50ms ÷ 2 at once = ?).
