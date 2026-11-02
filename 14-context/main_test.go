package main

import (
	"context"
	"errors"
	"testing"
	"time"
)

// Tests can live alongside package main too. Go 1.24 added t.Context():
// a context that is cancelled automatically when the test finishes, so
// it's the right root for anything a test starts.

func TestSlowOperationFinishesInTime(t *testing.T) {
	res, err := slowOperation(t.Context(), "q", time.Millisecond)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "q ok" {
		t.Errorf("got %q, want %q", res, "q ok")
	}
}

func TestSlowOperationTimesOut(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()

	start := time.Now()
	_, err := slowOperation(ctx, "q", time.Second)

	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
	// The whole point: we returned quickly instead of waiting a full second.
	if elapsed := time.Since(start); elapsed > 500*time.Millisecond {
		t.Errorf("took %v, should have stopped at the deadline", elapsed)
	}
}

func TestSlowOperationAlreadyCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel() // cancelled before we even start

	_, err := slowOperation(ctx, "q", time.Second)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want Canceled, got %v", err)
	}
}

func TestFirstResultPicksFastest(t *testing.T) {
	res, err := firstResult(t.Context(), map[string]time.Duration{
		"slow": time.Second,
		"fast": time.Millisecond,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "fast ok" {
		t.Errorf("got %q, want %q", res, "fast ok")
	}
}

func TestProcessBatchStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	done, err := processBatch(ctx, make([]string, 10))
	if done != 0 {
		t.Errorf("processed %d rows, want 0", done)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("want Canceled, got %v", err)
	}
}

func TestProcessBatchCompletes(t *testing.T) {
	done, err := processBatch(t.Context(), make([]string, 3))
	if err != nil || done != 3 {
		t.Errorf("got (%d, %v), want (3, nil)", done, err)
	}
}
