// LESSON 08 — CONCURRENCY: goroutines, channels, WaitGroup, select, mutex
//
// Run with:  go run ./08-concurrency
//
// This is where Go really shines compared with PHP. A GOROUTINE is a very
// lightweight thread managed by Go. You can run thousands at once (each
// starts with only a few KB of memory). Start one with the `go` keyword.
//
// Go's motto: "Don't communicate by sharing memory; share memory by
// communicating." Goroutines pass data to each other through CHANNELS.
package main

import (
	"fmt"
	"math/rand"
	"sync"
	"time"
)

// Simulates slow work, like an HTTP call or image resize.
func fetch(url string) string {
	delay := time.Duration(100+rand.Intn(300)) * time.Millisecond
	time.Sleep(delay)
	return fmt.Sprintf("%s (took %v)", url, delay)
}

func main() {
	urls := []string{"site-a.com", "site-b.com", "site-c.com", "site-d.com"}

	// ---------------------------------------------------------------
	// 1. SEQUENTIAL — what PHP does by default
	// ---------------------------------------------------------------
	start := time.Now()
	for _, u := range urls {
		fetch(u)
	}
	fmt.Println("sequential took:", time.Since(start).Round(time.Millisecond))

	// ---------------------------------------------------------------
	// 2. GOROUTINES + WAITGROUP — run all at once, wait for all to finish
	// ---------------------------------------------------------------
	start = time.Now()
	var wg sync.WaitGroup
	for _, u := range urls {
		// wg.Go (Go 1.25+) starts a goroutine and tracks it. Older code does
		// wg.Add(1); go func() { defer wg.Done(); ... }()
		wg.Go(func() {
			fmt.Println("  done:", fetch(u))
		})
	}
	wg.Wait() // block until every goroutine has finished
	fmt.Println("concurrent took:", time.Since(start).Round(time.Millisecond))
	// Total time ≈ the slowest single fetch, not the sum of all of them.

	// ---------------------------------------------------------------
	// 3. CHANNELS — send results back to the caller
	// ---------------------------------------------------------------
	// A channel is a typed pipe. `ch <- v` sends, `v := <-ch` receives.
	// Both block until the other side is ready, which synchronises them.
	results := make(chan string)
	for _, u := range urls {
		go func() {
			results <- fetch(u)
		}()
	}
	// Receive exactly as many results as we started (arrive in finish order).
	for range urls {
		fmt.Println("  result:", <-results)
	}

	// ---------------------------------------------------------------
	// 4. WORKER POOL — limit how many run at once
	// ---------------------------------------------------------------
	// Great for "process 10,000 images, but only 3 at a time".
	// A BUFFERED channel (capacity given) can hold values without a
	// receiver ready, up to its capacity.
	jobs := make(chan int, 10)
	done := make(chan string, 10)

	worker := func(id int) {
		// `range` over a channel keeps receiving until it's closed.
		for job := range jobs {
			time.Sleep(50 * time.Millisecond)
			done <- fmt.Sprintf("worker %d finished job %d", id, job)
		}
	}

	var pool sync.WaitGroup
	for id := 1; id <= 3; id++ {
		pool.Go(func() { worker(id) })
	}

	for j := 1; j <= 6; j++ {
		jobs <- j
	}
	close(jobs) // tells the workers no more jobs are coming

	pool.Wait()
	close(done)
	for msg := range done {
		fmt.Println(" ", msg)
	}

	// ---------------------------------------------------------------
	// 5. SELECT — wait on several channels; add timeouts
	// ---------------------------------------------------------------
	slow := make(chan string)
	go func() {
		time.Sleep(2 * time.Second)
		slow <- "finally!"
	}()

	select {
	case msg := <-slow:
		fmt.Println("got:", msg)
	case <-time.After(500 * time.Millisecond):
		fmt.Println("timed out waiting for slow operation")
	}

	// ---------------------------------------------------------------
	// 6. MUTEX — when goroutines must share a variable
	// ---------------------------------------------------------------
	// Without the lock, counter++ from 1000 goroutines would race and lose
	// updates. Try deleting the Lock/Unlock lines and running with:
	//   go run -race ./08-concurrency
	var (
		mu      sync.Mutex
		counter int
		group   sync.WaitGroup
	)
	for range 1000 {
		group.Go(func() {
			mu.Lock()
			defer mu.Unlock()
			counter++
		})
	}
	group.Wait()
	fmt.Println("counter:", counter) // always 1000
}
