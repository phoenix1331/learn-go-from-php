package shop

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The other side of httptest. Lesson 16 pointed its client at one shared
// fake API. Here each test writes the provider's behaviour inline, which
// is Http::fake() with a closure, including Http::assertSent().
func newPayments(t *testing.T, h http.HandlerFunc) *HTTPPayments {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &HTTPPayments{BaseURL: srv.URL, APIKey: "sk_test", Client: srv.Client()}
}

func TestChargeSendsExpectedRequest(t *testing.T) {
	// Handlers run on another goroutine, where t.Fatal isn't allowed.
	// So capture what arrived, then check it back on the test's goroutine.
	// The server finishes the handler before the client call returns,
	// which is what makes reading these variables safe.
	var (
		method, path, auth, key string
		body                    map[string]int64
	)
	p := newPayments(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		auth, key = r.Header.Get("Authorization"), r.Header.Get("Idempotency-Key")
		json.NewDecoder(r.Body).Decode(&body)
		w.WriteHeader(http.StatusCreated)
		io.WriteString(w, `{"id":"ch_42"}`)
	})

	id, err := p.Charge(t.Context(), "key-1", 1250)
	if err != nil {
		t.Fatal(err)
	}
	if id != "ch_42" {
		t.Errorf("id = %q", id)
	}
	if method != "POST" || path != "/charges" {
		t.Errorf("request = %s %s", method, path)
	}
	if auth != "Bearer sk_test" || key != "key-1" {
		t.Errorf("headers: auth=%q key=%q", auth, key)
	}
	if body["pence"] != 1250 {
		t.Errorf("body = %v", body)
	}
}

func TestChargeMapsResponses(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
		want   error // nil means "any error that isn't one of ours"
	}{
		{"declined", 402, "", ErrDeclined},
		{"server error", 500, "", ErrUnavailable},
		{"bad gateway", 502, "", ErrUnavailable},
		{"our mistake", 400, "", nil},
		{"garbage on success", 201, "<html>", nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPayments(t, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				io.WriteString(w, tt.body)
			})

			_, err := p.Charge(t.Context(), "k", 100)
			if err == nil {
				t.Fatal("expected an error")
			}
			if tt.want != nil && !errors.Is(err, tt.want) {
				t.Errorf("err = %v, want %v", err, tt.want)
			}
			// A 400 is a bug in our request. Retrying it would never help.
			if tt.want == nil && errors.Is(err, ErrUnavailable) {
				t.Errorf("err = %v must not be retryable", err)
			}
		})
	}
}

func TestChargeServerUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close() // now nothing is listening on that address

	p := &HTTPPayments{BaseURL: url, Client: http.DefaultClient}
	if _, err := p.Charge(t.Context(), "k", 100); !errors.Is(err, ErrUnavailable) {
		t.Errorf("err = %v, want ErrUnavailable", err)
	}
}

func TestChargeHonoursContext(t *testing.T) {
	entered := make(chan struct{})
	p := newPayments(t, func(w http.ResponseWriter, r *http.Request) {
		// Read the body first. The server only starts watching for the client
		// disconnecting once the body is consumed. Skip this and r.Context()
		// never ends, the handler hangs, and so does srv.Close in the cleanup.
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done() // hang until the client gives up
	})

	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	go func() {
		<-entered
		cancel()
	}()

	if _, err := p.Charge(ctx, "k", 100); !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want context.Canceled", err)
	}
}

func TestRefundEscapesChargeID(t *testing.T) {
	var path string
	p := newPayments(t, func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.EscapedPath()
	})

	if err := p.Refund(t.Context(), "ch/../1"); err != nil {
		t.Fatal(err)
	}
	if want := "/charges/ch%2F..%2F1/refund"; path != want {
		t.Errorf("path = %q, want %q", path, want)
	}
}
