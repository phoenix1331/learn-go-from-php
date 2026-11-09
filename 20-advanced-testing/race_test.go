package shop

import (
	"encoding/json"
	"sync"
	"testing"
)

// THE RACE DETECTOR. `go test -race` instruments every memory access and
// reports two goroutines touching the same variable, unsynchronised, with
// at least one writing. PHP has no equivalent because requests share nothing.
// In Go, every request handler shares your whole heap.
//
// It only sees races that actually HAPPEN during the run, so it needs a test
// that really does the concurrent thing. That's the job of the test below.
// It slows tests down several times over, which is fine for CI (see
// .github/workflows/ci.yml) but too slow to leave on in production.
//
// Note what this test doesn't do: t.Parallel() alone doesn't exercise a race
// unless the parallel tests share something. Here, one app is shared on
// purpose.
func TestConcurrentOrders(t *testing.T) {
	const orders = 50
	a := newApp()

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		seen = make(map[int64]bool)
	)
	for range orders {
		wg.Go(func() {
			rec := a.serve(newRequest(t, "POST", "/orders", validBody))
			if rec.Code != 201 {
				// t.Errorf is safe from any goroutine. t.Fatalf is not.
				t.Errorf("status = %d: %s", rec.Code, rec.Body)
				return
			}
			var order Order
			if err := json.Unmarshal(rec.Body.Bytes(), &order); err != nil {
				t.Error(err)
				return
			}
			mu.Lock()
			defer mu.Unlock()
			seen[order.ID] = true
		})
		// Read the stats while the writes are happening, or the detector
		// has no read to compare them with.
		wg.Go(func() { a.serve(newRequest(t, "GET", "/stats", "")) })
	}
	wg.Wait()

	if len(seen) != orders {
		t.Errorf("%d unique IDs, want %d", len(seen), orders)
	}
	stats := decodeJSON[map[string]int](t, a.serve(newRequest(t, "GET", "/stats", "")).Body)
	if stats["placed"] != orders {
		t.Errorf("stats placed = %d, want %d", stats["placed"], orders)
	}
}
