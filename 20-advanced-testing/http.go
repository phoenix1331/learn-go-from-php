package shop

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"maps"
	"net/http"
	"strconv"
	"sync"
)

// Metrics counts outcomes. Every request shares one, from many goroutines,
// so it's exactly the kind of code the race detector is for.
type Metrics struct {
	mu     sync.Mutex
	counts map[string]int
}

func (m *Metrics) Inc(name string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.counts == nil {
		m.counts = make(map[string]int)
	}
	m.counts[name]++
}

// Snapshot returns a copy, so callers never touch the map behind the lock.
func (m *Metrics) Snapshot() map[string]int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return maps.Clone(m.counts)
}

// NewHandler returns an http.Handler rather than starting a server. That's
// what makes it testable with no network: anything can call ServeHTTP.
func NewHandler(svc *Service, token string) http.Handler {
	metrics := &Metrics{}
	mux := http.NewServeMux()

	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Email  string `json:"email"`
			Amount string `json:"amount"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&req); err != nil {
			metrics.Inc("invalid")
			writeError(w, http.StatusBadRequest, "invalid JSON")
			return
		}

		order, err := svc.Place(r.Context(), req.Email, req.Amount)
		if err != nil {
			status, label, message := classify(err)
			metrics.Inc(label)
			writeError(w, status, message)
			return
		}
		metrics.Inc("placed")
		writeJSON(w, http.StatusCreated, order)
	})

	mux.HandleFunc("GET /orders/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
		if err != nil {
			writeError(w, http.StatusNotFound, "order not found")
			return
		}
		order, err := svc.Find(r.Context(), id)
		if err != nil {
			status, _, message := classify(err)
			writeError(w, status, message)
			return
		}
		writeJSON(w, http.StatusOK, order)
	})

	mux.HandleFunc("GET /stats", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, metrics.Snapshot())
	})

	return requireToken(token, mux)
}

// classify maps an error to a response. Like Laravel's exception handler, it
// shows the client only what's safe: anything unexpected is a plain 500.
func classify(err error) (status int, label, message string) {
	switch {
	case errors.Is(err, ErrInvalid):
		return http.StatusUnprocessableEntity, "invalid", err.Error()
	case errors.Is(err, ErrDeclined):
		return http.StatusPaymentRequired, "declined", "card declined"
	case errors.Is(err, ErrNotFound):
		return http.StatusNotFound, "not_found", "order not found"
	case errors.Is(err, ErrUnavailable):
		return http.StatusServiceUnavailable, "unavailable", "payment provider unavailable"
	default:
		return http.StatusInternalServerError, "error", "internal error"
	}
}

func requireToken(token string, next http.Handler) http.Handler {
	want := []byte("Bearer " + token)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got := []byte(r.Header.Get("Authorization"))
		if subtle.ConstantTimeCompare(got, want) != 1 {
			writeError(w, http.StatusUnauthorized, "unauthorized")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, message string) {
	writeJSON(w, status, map[string]string{"error": message})
}
