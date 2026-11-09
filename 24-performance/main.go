// LESSON 24 — PERFORMANCE: benchmarks, profiling, reducing allocations
//
// Run with:  go run ./24-performance          (from the repo root)
// Benchmark: go test -bench=. -benchmem -run='^$' ./24-performance
// Test with: go test -race -v ./24-performance
//
// The files:
//   - summarise.go  one job, written four ways, each faster than the last
//   - micro.go      the most common sources of allocations
//   - main.go       this tour, plus a service to profile while it's running
//
// THE METHOD, and it matters more than any trick below:
//
//  1. MEASURE    a benchmark gives you a number to beat
//  2. PROFILE    find where the time actually goes (it's rarely where you'd guess)
//  3. CHANGE     one thing
//  4. MEASURE    keep it only if the number moved
//
// In PHP you'd reach for Blackfire or Xdebug. In Go, it's all built in.
//
// WORK OUT WHETHER IT MATTERS. In lesson 22, a million rows took 0.65s to
// validate and 10.2s to validate and save. Making validation infinitely fast
// would save 6%. Making the database batches better is where the time is.
//
// PROFILE FROM THE COMMAND LINE
//
//	go test -bench=Summarise/naive -run='^$' -cpuprofile=cpu.prof ./24-performance
//	go tool pprof -top -cum cpu.prof
//	go tool pprof -http=:8081 cpu.prof     # flame graph in your browser
//
// PROFILE A LIVE SERVICE
//
//	go run ./24-performance -serve
//	go tool pprof 'http://localhost:6060/debug/pprof/profile?seconds=5'
//	                                       # meanwhile, send it some traffic:
//	for i in $(seq 200); do curl -s 'localhost:8080/report?version=naive' >/dev/null; done
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net"
	"net/http"
	"net/http/pprof"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	runtimepprof "runtime/pprof"
	"strings"
	"syscall"
	"testing"
	"time"
)

// pkg is the full import path, so this works from any folder in the module.
const pkg = "github.com/phoenix1331/learn-go-from-php/24-performance"

var versions = []struct {
	name   string
	change string
	fn     Summariser
}{
	{"naive", "the obvious version", SummariseNaive},
	{"compiled-once", "compile the regexps once", SummariseCompiledOnce},
	{"no-regexp", "replace regexps with string functions", SummariseNoRegexp},
	{"stream", "stream, and stop allocating per line", SummariseStream},
}

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stderr)
	stop()
	os.Exit(code)
}

func run(ctx context.Context, args []string, stderr io.Writer) int {
	fs := flag.NewFlagSet("performance", flag.ContinueOnError)
	fs.SetOutput(stderr)
	serve := fs.Bool("serve", false, "run a service to profile, instead of the tour")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}

	var err error
	if *serve {
		err = runService(ctx)
	} else {
		err = tour(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, "error:", err)
		return 1
	}
	return 0
}

// generateLog makes the same lines every time, so results can be compared.
func generateLog(lines, paths int) string {
	rng := rand.New(rand.NewPCG(1, 2))
	var b strings.Builder
	for range lines {
		fmt.Fprintf(&b, "ts=2026-03-14T10:00:00Z user=user%d path=/api/v1/resource%d status=200 ms=%d\n",
			rng.IntN(1000), rng.IntN(paths), rng.IntN(500))
	}
	return b.String()
}

// result is one measurement, per log line.
type result struct {
	nsPerLine     float64
	allocsPerLine float64
	bytesPerLine  float64
}

// measure is what a benchmark does: repeat until enough time has passed for
// the number to mean something, and divide. MemStats counts allocations.
func measure(lines int, fn func()) result {
	const minimum = 300 * time.Millisecond

	runtime.GC()
	var before, after runtime.MemStats
	runtime.ReadMemStats(&before)
	start := time.Now()
	runs := 0
	for time.Since(start) < minimum {
		fn()
		runs++
	}
	elapsed := time.Since(start)
	runtime.ReadMemStats(&after)

	total := float64(runs * lines)
	return result{
		nsPerLine:     float64(elapsed.Nanoseconds()) / total,
		allocsPerLine: float64(after.Mallocs-before.Mallocs) / total,
		bytesPerLine:  float64(after.TotalAlloc-before.TotalAlloc) / total,
	}
}

func tour(ctx context.Context) error {
	const lines = 20_000
	data := generateLog(lines, 20)
	fmt.Printf("%d log lines, %.1f MB, for example:\n    %s\n",
		lines, float64(len(data))/(1<<20), data[:strings.IndexByte(data, '\n')])

	// ---------------------------------------------------------------
	// 1. MEASURE
	// ---------------------------------------------------------------
	fmt.Println("\n1. Measure the obvious version")
	first := versions[0]
	r := measure(lines, func() { must(first.fn(strings.NewReader(data))) })
	fmt.Printf("    %.0f ns per line, %.0f allocations per line\n", r.nsPerLine, r.allocsPerLine)
	fmt.Printf("    That's %.0f lines a second. Is it the regexps? The map? Don't guess.\n", 1e9/r.nsPerLine)

	// ---------------------------------------------------------------
	// 2. PROFILE
	// ---------------------------------------------------------------
	fmt.Println("\n2. Profile it: where does the time go?")
	if err := showProfile(ctx, data, first.fn); err != nil {
		return err
	}
	// flat = time in the function itself. cum = including what it calls.
	// Here regexp.Compile is the biggest single cost, at around half.

	// ---------------------------------------------------------------
	// 3. CHANGE ONE THING AT A TIME
	// ---------------------------------------------------------------
	fmt.Println("\n3. One change at a time, measured")
	fmt.Printf("    %-14s %10s %10s %10s   %s\n", "version", "ns/line", "allocs", "bytes", "the change")
	base := 0.0
	for i, v := range versions {
		r := measure(lines, func() { must(v.fn(strings.NewReader(data))) })
		note := v.change
		if i == 0 {
			base = r.nsPerLine
		} else {
			note += fmt.Sprintf(" (%.0fx faster than naive)", base/r.nsPerLine)
		}
		fmt.Printf("    %-14s %10.0f %10.2f %10.0f   %s\n",
			v.name, r.nsPerLine, r.allocsPerLine, r.bytesPerLine, note)
	}

	// ---------------------------------------------------------------
	// 4. THE BOTTLENECK MOVES
	// ---------------------------------------------------------------
	fmt.Println("\n4. Profile the final version: the bottleneck has moved")
	if err := showProfile(ctx, data, SummariseStream); err != nil {
		return err
	}
	// No one thing dominates any more. That's when to stop.

	// ---------------------------------------------------------------
	// 5. COUNTING ALLOCATIONS
	// ---------------------------------------------------------------
	fmt.Println("\n5. Common sources of allocations (per call)")
	parts := strings.Split(strings.Repeat("word ", 100), " ")
	for _, m := range []struct {
		name string
		fn   func()
	}{
		{"strings: += x100", func() { sinkString = ConcatPlus(parts) }},
		{"strings: Builder with Grow", func() { sinkString = ConcatBuilder(parts) }},
		{"slices:  append x1000", func() { sinkInts = AppendGrow(1000) }},
		{"slices:  make with capacity", func() { sinkInts = AppendPresized(1000) }},
		{"values:  stays on the stack", func() { sinkInt = SumOnStack(1, 2) }},
		{"values:  returned by pointer", func() { sinkPoint = NewOnHeap(1, 2) }},
	} {
		fmt.Printf("    %-30s %v\n", m.name, testing.AllocsPerRun(200, m.fn))
	}

	// ---------------------------------------------------------------
	// 6. ASK THE COMPILER
	// ---------------------------------------------------------------
	fmt.Println("\n6. Asking the compiler what escapes to the heap")
	return showEscapes(ctx)
}

// Package-level sinks keep results alive, so the compiler can't remove the
// work being counted. They're typed because storing a value in an `any`
// is itself an allocation, which would distort the counts.
var (
	sinkString string
	sinkInts   []int
	sinkInt    int
	sinkPoint  *Point
)

func must[T any](_ T, err error) {
	if err != nil {
		panic(err)
	}
}

// showProfile records a CPU profile for one second, then asks pprof for the
// most expensive functions. It's what `go test -cpuprofile` does for you.
func showProfile(ctx context.Context, data string, fn Summariser) error {
	dir, err := os.MkdirTemp("", "go-perf-tour")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)
	path := filepath.Join(dir, "cpu.prof")

	f, err := os.Create(path)
	if err != nil {
		return err
	}
	if err := runtimepprof.StartCPUProfile(f); err != nil {
		f.Close()
		return err
	}
	for start := time.Now(); time.Since(start) < time.Second; {
		must(fn(strings.NewReader(data)))
	}
	runtimepprof.StopCPUProfile()
	if err := f.Close(); err != nil {
		return err
	}

	output, err := exec.CommandContext(ctx, "go", "tool", "pprof", "-top", "-cum", "-nodecount=40", path).CombinedOutput()
	if err != nil {
		return fmt.Errorf("go tool pprof: %w\n%s", err, output)
	}

	shown := 0
	for line := range strings.Lines(string(output)) {
		if !strings.Contains(line, "flat%") && !isInteresting(line) {
			continue
		}
		fmt.Print("    ", line)
		if strings.Contains(line, "flat%") {
			continue
		}
		if shown++; shown == 7 {
			break
		}
	}
	return nil
}

// isInteresting hides the runtime's own startup functions from the output.
func isInteresting(line string) bool {
	if !strings.Contains(line, "%") {
		return false
	}
	for _, hide := range []string{"runtime.main", "main.main", "main.run", "main.tour", "main.showProfile", "runtime.goexit"} {
		if strings.HasSuffix(strings.TrimSpace(line), hide) {
			return false
		}
	}
	return true
}

// showEscapes runs the compiler's -m report, for micro.go only.
func showEscapes(ctx context.Context) error {
	cmd := exec.CommandContext(ctx, "go", "build", "-gcflags=-m", "-o", os.DevNull, pkg)
	output, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("go build: %w\n%s", err, output)
	}
	for line := range strings.Lines(string(output)) {
		if strings.Contains(line, "micro.go") && strings.Contains(line, "moved to heap: p") {
			fmt.Print("    ", line[strings.Index(line, "micro.go"):])
		}
	}
	fmt.Println("    (SumOnStack's p isn't listed: it stays on the stack)")
	return nil
}

// ---------------------------------------------------------------
// A SERVICE TO PROFILE
// ---------------------------------------------------------------

// newPublicMux is the API that the outside world can reach.
func newPublicMux(data string) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /report", func(w http.ResponseWriter, r *http.Request) {
		fn := Summariser(SummariseStream)
		if r.URL.Query().Get("version") == "naive" {
			fn = SummariseNaive
		}
		stats, err := fn(strings.NewReader(data))
		if err != nil {
			http.Error(w, "internal error", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(stats)
	})
	return mux
}

// newDebugMux exposes the profiler, on a separate mux and port.
//
// Tutorials often show `import _ "net/http/pprof"`. That adds the profiler to
// http.DefaultServeMux, so a service using the default mux publishes it to
// the world: memory contents, your command line, and a free way to burn CPU.
// Registering the handlers by hand keeps them on a localhost-only port.
func newDebugMux() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	return mux
}

func runService(ctx context.Context) error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))
	data := generateLog(2_000, 20)

	public := &http.Server{Handler: newPublicMux(data), ReadHeaderTimeout: 5 * time.Second}
	debug := &http.Server{Handler: newDebugMux(), ReadHeaderTimeout: 5 * time.Second}

	publicLn, err := net.Listen("tcp", "localhost:8080")
	if err != nil {
		return err
	}
	debugLn, err := net.Listen("tcp", "localhost:6060") // never on a public address
	if err != nil {
		publicLn.Close()
		return err
	}
	go public.Serve(publicLn)
	go debug.Serve(debugLn)
	logger.Info("listening", "api", "localhost:8080", "pprof", "localhost:6060")

	<-ctx.Done()
	logger.Info("shutting down")
	graceCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	return errors.Join(public.Shutdown(graceCtx), debug.Shutdown(graceCtx))
}

// GOING FURTHER
//   - BENCHSTAT: benchmarks are noisy. Run each with -count=10 and compare
//     with golang.org/x/perf/cmd/benchstat, which says if a change is real.
//   - PGO: put a production CPU profile at default.pgo beside main.go and
//     `go build` uses it to optimise hot paths, typically a few percent.
//   - GOGC / GOMEMLIMIT: these tune the garbage collector for you, and are
//     worth trying before you rewrite any code to allocate less.
//   - sync.Pool: reuses buffers across goroutines. Add it when a memory
//     profile shows that one allocation dominating, and not before.
//   - MEMORY PROFILES: -memprofile=mem.prof, then
//     `go tool pprof -sample_index=alloc_space -top mem.prof`.
//   - TRACING: `go tool trace` shows what every goroutine was doing and
//     when, which is what you need for latency problems.
//
// THINGS TO TRY
//   - In SummariseStream, change sc.Bytes() to []byte(sc.Text()), one small
//     copy per line. TestStreamAllocationsDoNotGrow fails.
//   - Break isDigits so it accepts "-", then run
//     `go test -fuzz=FuzzVersionsAgree -fuzztime=20s ./24-performance`.
//   - Move the regexp.MustCompile back inside the loop of the compiled-once
//     version, and run the benchmark.
