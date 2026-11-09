package shop

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const validBody = `{"email":"ada@example.com","amount":"12.50"}`

// newRequest builds a request with a valid token. httptest.NewRequest is for
// feeding a handler directly, with no network involved.
func newRequest(t *testing.T, method, target, body string) *http.Request {
	r := httptest.NewRequestWithContext(t.Context(), method, target, strings.NewReader(body))
	r.Header.Set("Authorization", "Bearer "+testToken)
	return r
}

// serve is this lesson's $this->postJson(). A ResponseRecorder is an
// http.ResponseWriter that just remembers what was written to it.
func (a *app) serve(r *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	a.handler.ServeHTTP(rec, r)
	return rec
}

// decodeJSON is generic (lesson 9) so tests get a typed value back.
func decodeJSON[T any](t *testing.T, r io.Reader) T {
	t.Helper() // failures point at the caller's line, not this one
	var v T
	if err := json.NewDecoder(r).Decode(&v); err != nil {
		t.Fatalf("decoding JSON: %v", err)
	}
	return v
}

func TestPlaceOrderStatusCodes(t *testing.T) {
	tests := []struct {
		name   string
		body   string
		setup  func(*app)
		status int
	}{
		{name: "ok", body: validBody, status: 201},
		{name: "broken JSON", body: `{"email":`, status: 400},
		{name: "wrong JSON type", body: `{"amount":12.5}`, status: 400},
		{name: "invalid amount", body: `{"email":"a@b.c","amount":"free"}`, status: 422},
		{
			name: "declined", body: validBody, status: 402,
			setup: func(a *app) { a.pay.errs = []error{ErrDeclined} },
		},
		{
			name: "database down", body: validBody, status: 500,
			setup: func(a *app) { a.store.saveErr = errors.New("boom") },
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Nothing here is shared, so subtests can run side by side.
			t.Parallel()
			a := newApp()
			if tt.setup != nil {
				tt.setup(a)
			}
			rec := a.serve(newRequest(t, "POST", "/orders", tt.body))
			if rec.Code != tt.status {
				t.Errorf("status = %d, want %d; body: %s", rec.Code, tt.status, rec.Body)
			}
			if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
				t.Errorf("Content-Type = %q", ct)
			}
		})
	}
}

func TestPlaceOrderResponseBody(t *testing.T) {
	a := newApp()
	rec := a.serve(newRequest(t, "POST", "/orders", validBody))

	got := decodeJSON[Order](t, rec.Body)
	want := Order{
		ID: 1, Email: "ada@example.com", Pence: 1250, Total: "12.50",
		ChargeID: "ch_1", ReceiptSent: true, CreatedAt: frozenNow,
	}
	// == works on a struct if all its fields are comparable. Use reflect.DeepEqual
	// or go-cmp for slices and maps. (And time.Time.Equal when zones can differ.)
	if got != want {
		t.Errorf("got  %+v\nwant %+v", got, want)
	}
}

func TestAuth(t *testing.T) {
	tests := []struct{ name, header string }{
		{"missing", ""},
		{"wrong token", "Bearer nope"},
		{"wrong scheme", "Basic " + testToken},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newApp()
			r := newRequest(t, "POST", "/orders", validBody)
			r.Header.Set("Authorization", tt.header)

			if rec := a.serve(r); rec.Code != http.StatusUnauthorized {
				t.Errorf("status = %d, want 401", rec.Code)
			}
			if a.pay.calls() != 0 {
				t.Error("an unauthorised request reached the payment code")
			}
		})
	}
}

func TestGetOrder(t *testing.T) {
	a := newApp()
	if _, err := a.svc.Place(t.Context(), validEmail, "1.00"); err != nil {
		t.Fatal(err)
	}

	rec := a.serve(newRequest(t, "GET", "/orders/1", ""))
	if rec.Code != 200 || decodeJSON[Order](t, rec.Body).ID != 1 {
		t.Errorf("GET /orders/1: %d %s", rec.Code, rec.Body)
	}
	for _, target := range []string{"/orders/2", "/orders/abc"} {
		if rec := a.serve(newRequest(t, "GET", target, "")); rec.Code != 404 {
			t.Errorf("GET %s: %d, want 404", target, rec.Code)
		}
	}
}

func TestInternalErrorsAreNotShownToClients(t *testing.T) {
	a := newApp()
	a.store.saveErr = errors.New("connect to 10.0.0.5 as root failed")

	rec := a.serve(newRequest(t, "POST", "/orders", validBody))
	if strings.Contains(rec.Body.String(), "10.0.0.5") {
		t.Errorf("leaked internals: %s", rec.Body)
	}
}

// A recorder skips the network, so it's the default. Use a real server when
// the network IS what you're testing, or to drive the API with a real client.
// Everything else (routing, middleware, JSON) is the same in both.
func TestThroughARealServer(t *testing.T) {
	a := newApp()
	srv := httptest.NewServer(a.handler)
	t.Cleanup(srv.Close)

	req, err := http.NewRequestWithContext(t.Context(), "POST", srv.URL+"/orders", strings.NewReader(validBody))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+testToken)

	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Errorf("status = %d, want 201", resp.StatusCode)
	}
}
