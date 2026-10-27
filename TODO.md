# TODO LIST

Where I'm up to, what's next, and notes to pick up from later.

**To resume:** check the status tables below, find the first unticked lesson, and carry on.

---

## Part 1: Foundations (built)

| # | Lesson | Status | Notes |
|---|---|---|---|
| 01 | Basics | [ ] | |
| 02 | Control flow | [ ] | |
| 03 | Functions | [ ] | |
| 04 | Collections | [ ] | |
| 05 | Structs & methods | [ ] | |
| 06 | Interfaces | [ ] | |
| 07 | Errors | [ ] | |
| 08 | Concurrency | [ ] | |
| 09 | Generics | [ ] | |
| 10 | JSON CLI tool | [ ] | |
| 11 | HTTP server | [ ] | |
| 12 | Testing | [ ] | |

## Part 2: Real-world Go (planned, not yet built)

These build towards using Go for heavy lifting alongside PHP / Laravel.

| # | Lesson | What it would cover | Status |
|---|---|---|---|
| 13 | Packages & modules | Splitting code into packages, `internal/`, `go get` third-party modules, `go.sum` | [ ] |
| 14 | Context | `context.Context` for timeouts and cancellation, passing it through calls | [ ] |
| 15 | Files & streaming | `os`, `bufio`, `io.Reader`/`Writer`, streaming a large CSV without loading it all | [ ] |
| 16 | HTTP client | Calling APIs, timeouts, retries, fetching many URLs concurrently with a limit | [ ] |
| 17 | Databases | `database/sql` with SQLite, queries, transactions, avoiding SQL injection | [ ] |
| 18 | Config & logging | Env vars, `log/slog` structured logging, sensible defaults | [ ] |
| 19 | Graceful shutdown | OS signals, finishing in-flight work before exiting | [ ] |
| 20 | Advanced testing | `httptest`, fakes via interfaces, fuzz tests, race detector in tests | [ ] |
| 21 | Laravel ↔ Go queue worker | Laravel pushes jobs to Redis, a Go worker processes them and reports back | [ ] |
| 22 | Project: CSV importer | Validate and import a big CSV, called from Laravel as a CLI binary | [ ] |
| 23 | Deployment | Cross-compiling, Docker multi-stage builds, running as a service | [ ] |
| 24 | Performance | Benchmarks, `pprof` profiling, reducing allocations | [ ] |

Optional extras to slot in: iterators (`range` over functions), `embed` for bundling files into a binary, `time` and scheduling, WebSockets.




