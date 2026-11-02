package main

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// newTestAPI starts a fresh fake API for one test. t.Cleanup shuts it down
// when the test ends (like a tearDown() that you register inline).
// Each test gets its own server, so counters never leak between tests.
func newTestAPI(t *testing.T) (*fakeAPI, string) {
	t.Helper() // failures report the caller's line, not this one
	api := &fakeAPI{}
	srv := httptest.NewServer(api.handler())
	t.Cleanup(srv.Close)
	return api, srv.URL
}

var testClient = &http.Client{Timeout: 2 * time.Second}

func TestGetJSON(t *testing.T) {
	_, base := newTestAPI(t)

	u, err := getJSON[User](t.Context(), testClient, base+"/users/3")
	if err != nil {
		t.Fatal(err)
	}
	if u.ID != 3 || u.Email != "user3@example.com" {
		t.Errorf("got %+v", u)
	}
}

func TestGetJSONNotFound(t *testing.T) {
	_, base := newTestAPI(t)

	_, err := getJSON[User](t.Context(), testClient, base+"/users/999")
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusNotFound {
		t.Fatalf("want 404 StatusError, got %v", err)
	}
}

func TestCreateOrder(t *testing.T) {
	_, base := newTestAPI(t)

	o, err := createOrder(t.Context(), testClient, base, apiToken, OrderRequest{SKU: "X", Quantity: 3})
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == 0 || o.SKU != "X" || o.Quantity != 3 {
		t.Errorf("got %+v", o)
	}
}

func TestCreateOrderErrors(t *testing.T) {
	_, base := newTestAPI(t)

	tests := []struct {
		name  string
		token string
		req   OrderRequest
		want  int
	}{
		{"bad token", "nope", OrderRequest{SKU: "X", Quantity: 1}, http.StatusUnauthorized},
		{"missing sku", apiToken, OrderRequest{Quantity: 1}, http.StatusUnprocessableEntity},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := createOrder(t.Context(), testClient, base, tt.token, tt.req)
			var se *StatusError
			if !errors.As(err, &se) || se.Code != tt.want {
				t.Errorf("want HTTP %d, got %v", tt.want, err)
			}
		})
	}
}

func TestContextTimeout(t *testing.T) {
	_, base := newTestAPI(t)
	ctx, cancel := context.WithTimeout(t.Context(), 20*time.Millisecond)
	defer cancel()

	_, err := getJSON[map[string]string](ctx, testClient, base+"/slow")
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("want DeadlineExceeded, got %v", err)
	}
}

func TestWithRetryRecovers(t *testing.T) {
	api, base := newTestAPI(t)

	_, err := withRetry(t.Context(), 5, time.Millisecond, func(ctx context.Context) (map[string]string, error) {
		return getJSON[map[string]string](ctx, testClient, base+"/flaky")
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := api.flakyCalls.Load(); got != 3 {
		t.Errorf("server saw %d calls, want 3", got)
	}
}

func TestWithRetryGivesUp(t *testing.T) {
	api, base := newTestAPI(t)

	_, err := withRetry(t.Context(), 2, time.Millisecond, func(ctx context.Context) (map[string]string, error) {
		return getJSON[map[string]string](ctx, testClient, base+"/flaky")
	})
	var se *StatusError
	if !errors.As(err, &se) || se.Code != http.StatusServiceUnavailable {
		t.Fatalf("want 503 after giving up, got %v", err)
	}
	if got := api.flakyCalls.Load(); got != 2 {
		t.Errorf("server saw %d calls, want 2", got)
	}
}

// A fake fn (no server at all) is enough to test the retry rules.
func TestWithRetrySkips4xx(t *testing.T) {
	calls := 0
	_, err := withRetry(t.Context(), 5, time.Millisecond, func(context.Context) (int, error) {
		calls++
		return 0, &StatusError{Code: http.StatusBadRequest}
	})
	if err == nil || calls != 1 {
		t.Errorf("calls = %d, err = %v; want 1 call and an error", calls, err)
	}
}

func TestWithRetryStopsWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	calls := 0
	_, err := withRetry(ctx, 5, time.Hour, func(context.Context) (int, error) {
		calls++
		cancel() // the caller gives up during the first attempt
		return 0, &StatusError{Code: http.StatusServiceUnavailable}
	})
	if calls != 1 || err == nil {
		t.Errorf("calls = %d, err = %v; want 1 call and an error (no 1-hour wait)", calls, err)
	}
}

func TestFetchAllRespectsLimit(t *testing.T) {
	api, base := newTestAPI(t)
	ids := []int{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}

	items, err := fetchAll(t.Context(), testClient, base, ids, 3)
	if err != nil {
		t.Fatal(err)
	}
	if got := api.maxInFlight.Load(); got > 3 {
		t.Errorf("server saw %d requests at once, limit was 3", got)
	}
	for i, it := range items {
		if it.ID != ids[i] {
			t.Errorf("items[%d].ID = %d, want %d (order not kept)", i, it.ID, ids[i])
		}
	}
}
