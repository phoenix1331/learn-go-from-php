// LESSON 11 — HTTP SERVER: a tiny JSON API with only the standard library
//
// This is the "Go microservice that PHP calls" pattern.
//
// Run with:  go run ./11-http-server
// Then in another terminal:
//
//	curl localhost:8080/health
//	curl localhost:8080/tasks
//	curl -X POST localhost:8080/tasks -d '{"title":"Learn Go"}'
//	curl localhost:8080/tasks/1
//	curl localhost:8080/tasks/99
//
// Stop the server with Ctrl+C.
//
// From Laravel:  Http::post('http://localhost:8080/tasks', ['title' => 'Learn Go']);
//
// Unlike PHP-FPM (a fresh process per request), this is ONE long-running
// process. Each request is handled in its own goroutine, which is why the
// shared data below needs a mutex.
package main

import (
	"encoding/json"
	"log"
	"net/http"
	"strconv"
	"sync"
)

type Task struct {
	ID    int    `json:"id"`
	Title string `json:"title"`
	Done  bool   `json:"done"`
}

// An in-memory "database". In real life you'd use database/sql or similar.
type TaskStore struct {
	mu     sync.Mutex
	tasks  map[int]Task
	nextID int
}

func NewTaskStore() *TaskStore {
	return &TaskStore{tasks: make(map[int]Task), nextID: 1}
}

// Helper to send a JSON response, like Laravel's response()->json().
func writeJSON(w http.ResponseWriter, status int, data any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(data)
}

// Handlers are methods on the store, so they can use its data.
// Every handler has the same signature: (ResponseWriter, *Request).
func (s *TaskStore) list(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	defer s.mu.Unlock()

	tasks := make([]Task, 0, len(s.tasks))
	for _, t := range s.tasks {
		tasks = append(tasks, t)
	}
	writeJSON(w, http.StatusOK, tasks)
}

func (s *TaskStore) create(w http.ResponseWriter, r *http.Request) {
	var t Task
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil || t.Title == "" {
		writeJSON(w, http.StatusUnprocessableEntity, map[string]string{
			"error": "body must be JSON with a non-empty title",
		})
		return
	}

	s.mu.Lock()
	t.ID = s.nextID
	s.nextID++
	s.tasks[t.ID] = t
	s.mu.Unlock()

	writeJSON(w, http.StatusCreated, t)
}

func (s *TaskStore) show(w http.ResponseWriter, r *http.Request) {
	// r.PathValue reads the {id} part of the route pattern (Go 1.22+),
	// like a Laravel route parameter.
	id, err := strconv.Atoi(r.PathValue("id"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "id must be a number"})
		return
	}

	s.mu.Lock()
	t, ok := s.tasks[id]
	s.mu.Unlock()

	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "task not found"})
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// MIDDLEWARE: a function that wraps a handler — same idea as Laravel middleware.
func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		log.Println(r.Method, r.URL.Path)
		next.ServeHTTP(w, r)
	})
}

func main() {
	store := NewTaskStore()

	// ServeMux is the router. Patterns can include the HTTP method
	// and {wildcards} (Go 1.22+), so no framework is needed for simple APIs.
	mux := http.NewServeMux()
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})
	mux.HandleFunc("GET /tasks", store.list)
	mux.HandleFunc("POST /tasks", store.create)
	mux.HandleFunc("GET /tasks/{id}", store.show)

	addr := ":8080"
	log.Println("listening on", addr)
	// ListenAndServe blocks forever (until error). log.Fatal prints & exits.
	log.Fatal(http.ListenAndServe(addr, logRequests(mux)))
}
