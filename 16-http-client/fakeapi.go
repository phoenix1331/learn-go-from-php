// LESSON 16 — HTTP CLIENT (part 2: the fake API we call)
//
// Calling real internet APIs in a lesson would be slow and flaky, so this
// file builds a small local API with the same tools as lesson 11. main.go
// starts it with httptest.NewServer, which listens on a random free port
// on 127.0.0.1. Lesson 20 looks at httptest properly.
//
// You don't need to study this file. It's the "other side" for main.go
// to talk to. Each endpoint exists to show off one client feature.
package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"
)

// Shared request/response shapes. In real life these would match the
// third-party API's documentation.
type User struct {
	ID    int    `json:"id"`
	Name  string `json:"name"`
	Email string `json:"email"`
}

type OrderRequest struct {
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
}

type Order struct {
	ID       int    `json:"id"`
	SKU      string `json:"sku"`
	Quantity int    `json:"quantity"`
	Status   string `json:"status"`
}

type Item struct {
	ID         int `json:"id"`
	PricePence int `json:"price_pence"`
}

const apiToken = "secret-token"

// fakeAPI keeps a few counters so main.go can show what happened on the
// server side. atomic types are safe to update from many goroutines at
// once without a mutex (the server handles each request in its own goroutine).
type fakeAPI struct {
	flakyCalls  atomic.Int32
	inFlight    atomic.Int32
	maxInFlight atomic.Int32
}

func (a *fakeAPI) handler() http.Handler {
	mux := http.NewServeMux()

	// GET /users/{id} — 200 with JSON, or 404 for unknown users.
	mux.HandleFunc("GET /users/{id}", func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(r.PathValue("id"))
		if err != nil || id < 1 || id > 100 {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "user not found"})
			return
		}
		writeJSON(w, http.StatusOK, User{ID: id, Name: fmt.Sprintf("User %d", id), Email: fmt.Sprintf("user%d@example.com", id)})
	})

	// POST /orders — needs a bearer token and a JSON body.
	mux.HandleFunc("POST /orders", func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+apiToken {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing or bad token"})
			return
		}
		var req OrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.SKU == "" || req.Quantity < 1 {
			writeJSON(w, http.StatusUnprocessableEntity, map[string]string{"error": "sku and quantity are required"})
			return
		}
		writeJSON(w, http.StatusCreated, Order{ID: 1001, SKU: req.SKU, Quantity: req.Quantity, Status: "pending"})
	})

	// GET /slow — takes 2 seconds, unless the client gives up first.
	mux.HandleFunc("GET /slow", func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-time.After(2 * time.Second):
			writeJSON(w, http.StatusOK, map[string]string{"status": "finally"})
		case <-r.Context().Done(): // the client hung up (lesson 14)
		}
	})

	// GET /flaky — fails with 503 twice, succeeds on the third call, repeat.
	mux.HandleFunc("GET /flaky", func(w http.ResponseWriter, r *http.Request) {
		if n := a.flakyCalls.Add(1); n%3 != 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "try again"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// GET /items/{id} — takes 50ms, and records how many requests were
	// being handled at the same moment.
	mux.HandleFunc("GET /items/{id}", func(w http.ResponseWriter, r *http.Request) {
		now := a.inFlight.Add(1)
		defer a.inFlight.Add(-1)
		// Raise maxInFlight to `now` if it's bigger. CompareAndSwap only
		// succeeds if nobody changed the value since we loaded it.
		for {
			m := a.maxInFlight.Load()
			if now <= m || a.maxInFlight.CompareAndSwap(m, now) {
				break
			}
		}

		time.Sleep(50 * time.Millisecond)
		id, _ := strconv.Atoi(r.PathValue("id"))
		writeJSON(w, http.StatusOK, Item{ID: id, PricePence: id * 100})
	})

	return mux
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
