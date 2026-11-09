// LESSON 19 — GRACEFUL SHUTDOWN: OS signals, finishing in-flight work
//
// Run with:  go run ./19-graceful-shutdown
// Test with: go test -race -v ./19-graceful-shutdown
//
// The tour sends signals to itself, so it needs no input. To press Ctrl+C
// yourself, run the real service (from section 6):
//
//	go run ./19-graceful-shutdown -serve
//
// The files:
//   - worker.go  Queue and Pool: background jobs that stop cleanly
//   - app.go     Serve (the reusable pattern) and RunApp (a whole service)
//   - main.go    this tour
//
// WHY THIS EXISTS. Every deploy restarts your service. PHP mostly hides
// this: each request is its own short-lived run, and PHP-FPM finishes them
// for you on a graceful reload. The place you HAVE met it is a queue worker.
// `php artisan queue:work` catches SIGTERM, finishes the current job, then
// exits, and Supervisor's stopwaitsecs says how long it's allowed to take.
//
// A Go service is one process holding every in-flight request and job. If
// it just dies, all of them die with it: half-finished imports, customers
// seeing connection errors. So you have to do what queue:work does:
//
//	notice the signal -> stop taking new work -> finish what's in hand -> exit
//
// THE SIGNALS
//
//	SIGINT    Ctrl+C in a terminal                       catchable
//	SIGTERM   "please stop": docker stop, systemd,       catchable
//	          Kubernetes, Supervisor, plain `kill`
//	SIGHUP    terminal closed; by convention "reload"    catchable
//	SIGKILL   `kill -9`: the kernel just ends you        CANNOT be caught
//
// A platform sends SIGTERM, waits, then sends SIGKILL. The wait is 10s for
// docker stop, 30s for Kubernetes, 90s for systemd, and 10s for Supervisor
// (Laravel's docs tell you to raise stopwaitsecs). Your grace period has to
// fit inside it, or you get killed mid-cleanup anyway.
//
// Signals need Linux, macOS or WSL. Windows can't send them like this.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"
)

func main() {
	// os.Exit skips deferred calls, so it lives here, above run(), where
	// there are none. All the deferred cleanup is inside run() and runs
	// first. (log.Fatal calls os.Exit too: avoid it below main.)
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	serve := flag.Bool("serve", false, "run the service until Ctrl+C, instead of the tour")
	addr := flag.String("addr", "localhost:8080", "address for -serve")
	// 3 stages x 3s stays under docker stop's 10s.
	grace := flag.Duration("grace", 3*time.Second, "time allowed for each shutdown stage")
	flag.Parse()

	if *serve {
		return runService(*addr, *grace)
	}
	return tour()
}

// runService is what a real main looks like. This is the part to copy.
func runService(addr string, grace time.Duration) error {
	var level slog.LevelVar
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: &level}))

	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Once the first signal arrives, stop() restores Go's default behaviour,
	// so a SECOND Ctrl+C kills the process at once. Without this, nobody
	// can get out of a shutdown that's stuck.
	context.AfterFunc(ctx, func() {
		stop()
		logger.Info("shutting down, press Ctrl+C again to force", "grace", grace)
	})

	// `kill -HUP <pid>` toggles debug logs, with no restart (see section 1).
	hup := make(chan os.Signal, 1)
	signal.Notify(hup, syscall.SIGHUP)
	defer signal.Stop(hup)
	go func() {
		for range hup {
			toggleDebug(&level)
			logger.Info("log level changed", "level", level.Level())
		}
	}()

	if err := RunApp(ctx, ln, logger, grace); err != nil {
		return err
	}
	logger.Info("clean exit")
	return nil
}

func tour() error {
	// ---------------------------------------------------------------
	// 1. CATCHING A SIGNAL
	// ---------------------------------------------------------------
	fmt.Println("1. signal.Notify delivers signals to a channel")
	// Like pcntl_signal(SIGHUP, $handler), but a channel, not a callback.
	//
	// The buffer of 1 is required: the runtime won't wait for you, so if
	// nobody is receiving at that instant, an unbuffered channel just loses
	// the signal.
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGHUP)
	defer signal.Stop(sigs)

	var level slog.LevelVar
	fmt.Println("    level before:", level.Level())
	if err := sendSelf(syscall.SIGHUP); err != nil {
		return err
	}
	fmt.Println("    received:", <-sigs)
	// Not every signal means stop. This is the "flip it on a signal" idea
	// from lesson 18: turn on debug logs in production without a restart.
	toggleDebug(&level)
	fmt.Println("    level after: ", level.Level())

	// ---------------------------------------------------------------
	// 2. A SIGNAL AS A CONTEXT
	// ---------------------------------------------------------------
	fmt.Println("\n2. signal.NotifyContext turns a signal into a cancelled ctx")
	// For shutdown, this is what you want: lesson 14's ctx, cancelled by the
	// OS, so everything that already respects ctx stops by itself.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	fmt.Println("    before:", ctx.Err())
	if err := sendSelf(syscall.SIGTERM); err != nil {
		return err
	}
	<-ctx.Done()
	fmt.Println("    after: ", ctx.Err())
	// Until stop() is called, Go keeps catching these signals. runService
	// calls it early, to make a second Ctrl+C fatal.
	stop()

	// ---------------------------------------------------------------
	// 3. Close vs Shutdown
	// ---------------------------------------------------------------
	fmt.Println("\n3. A request is in flight when the server stops")
	{
		d, err := newDemo(300 * time.Millisecond)
		if err != nil {
			return err
		}
		go d.srv.Serve(d.ln)
		reply := d.get()
		<-d.entered
		d.srv.Close() // abrupt: every connection is cut right now
		fmt.Println("    srv.Close:    client got:", <-reply)
	}
	{
		d, err := newDemo(300 * time.Millisecond)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, d.srv, d.ln, 2*time.Second) }()
		reply := d.get()
		<-d.entered
		cancel() // stands in for the signal
		err = <-done
		fmt.Println("    srv.Shutdown: client got:", <-reply, "| Serve returned:", err)
	}

	// ---------------------------------------------------------------
	// 4. WHEN THE GRACE PERIOD RUNS OUT
	// ---------------------------------------------------------------
	fmt.Println("\n4. A request that's too slow to wait for")
	{
		d, err := newDemo(time.Minute)
		if err != nil {
			return err
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		done := make(chan error, 1)
		go func() { done <- Serve(ctx, d.srv, d.ln, 100*time.Millisecond) }()
		reply := d.get()
		<-d.entered
		cancel()
		fmt.Println("    Serve returned:", <-done)
		fmt.Println("    client got:    ", <-reply)
		// The handler stopped because it watches r.Context(). Shutdown can't
		// stop a handler that doesn't.
		fmt.Println("    handler:       ", <-d.outcome)
	}

	// ---------------------------------------------------------------
	// 5. BACKGROUND WORKERS
	// ---------------------------------------------------------------
	fmt.Println("\n5. Three ways for workers to stop (2 workers, 6 jobs)")
	finish := func(ctx context.Context, j Job) error {
		select {
		case <-time.After(j.Work):
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	// Signal part way through: what `queue:work` does on SIGTERM. Right when
	// the queue is durable (Redis), because the rest will still be there.
	stats, left := poolDemo(finish, 150*time.Millisecond, time.Second, true)
	fmt.Printf("    signal, finish in-flight:  %+v, not started: %d\n", stats, left)

	// Signal, but the jobs outlast the grace period. Same as --timeout.
	stats, left = poolDemo(finish, time.Minute, 100*time.Millisecond, true)
	fmt.Printf("    signal, grace runs out:    %+v, not started: %d\n", stats, left)

	// No signal to the pool: close the queue and let it run dry. Right for
	// an in-memory queue, because anything left is gone when you exit.
	stats, left = poolDemo(finish, 50*time.Millisecond, time.Second, false)
	fmt.Printf("    queue closed, drain all:   %+v, not started: %d\n", stats, left)

	// A job aborted part way will run again on a real queue, and so will
	// one interrupted by SIGKILL or a power cut. Graceful shutdown makes
	// that rare, not impossible: jobs still need to be safe to run twice.

	// ---------------------------------------------------------------
	// 6. A WHOLE SERVICE
	// ---------------------------------------------------------------
	fmt.Println("\n6. The real thing: SIGTERM with a request and jobs in flight")
	return wholeService()
}

func wholeService() error {
	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{} // no timestamps: easier to read here
			}
			return a
		},
	}))

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	base := "http://" + ln.Addr().String()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	appDone := make(chan error, 1)
	go func() { appDone <- RunApp(ctx, ln, logger, 2*time.Second) }()

	client := oneShotClient()
	for range 4 {
		resp, err := client.Post(base+"/jobs?ms=200", "text/plain", nil)
		if err != nil {
			return err
		}
		resp.Body.Close()
	}
	slow := make(chan string, 1)
	go func() {
		resp, err := client.Get(base + "/slow?ms=300")
		slow <- describe(resp, err)
	}()

	time.Sleep(50 * time.Millisecond) // let that request reach the handler
	fmt.Println("    --- sending SIGTERM ---")
	if err := sendSelf(syscall.SIGTERM); err != nil {
		return err
	}

	err = <-appDone
	fmt.Println("    slow request got:", <-slow)
	fmt.Println("    RunApp returned: ", err)
	_, afterErr := client.Get(base + "/health")
	fmt.Println("    new requests:    ", describe(nil, afterErr))
	return nil
}

// demo is a one-request server for sections 3 and 4.
type demo struct {
	srv     *http.Server
	ln      net.Listener
	url     string
	entered chan struct{} // closed when the handler starts
	outcome chan string   // what the handler ended up doing
}

func newDemo(work time.Duration) (*demo, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	d := &demo{
		ln:      ln,
		url:     "http://" + ln.Addr().String(),
		entered: make(chan struct{}),
		outcome: make(chan string, 1),
	}
	d.srv = &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(d.entered)
		select {
		case <-time.After(work):
			io.WriteString(w, "finished")
			d.outcome <- "completed"
		case <-r.Context().Done():
			d.outcome <- "stopped early: " + r.Context().Err().Error()
		}
	})}
	return d, nil
}

// get makes the request in the background and delivers the result later.
func (d *demo) get() <-chan string {
	reply := make(chan string, 1)
	go func() {
		resp, err := http.Get(d.url)
		reply <- describe(resp, err)
	}()
	return reply
}

// oneShotClient uses a new connection per request. A pooling client can
// leave a spare connection open, which delays Shutdown (see Serve in app.go).
func oneShotClient() *http.Client {
	return &http.Client{
		Timeout:   5 * time.Second,
		Transport: &http.Transport{DisableKeepAlives: true},
	}
}

// describe sums up a client result. It reads and closes the body.
func describe(resp *http.Response, err error) string {
	if err != nil {
		// A *url.Error includes the URL, which has a random port here.
		// Unwrap it to get just the reason.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			err = opErr.Err // "connection refused", without the addresses
		}
		return "ERROR: " + err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return fmt.Sprintf("%d %s", resp.StatusCode, strings.TrimSpace(string(body)))
}

// poolDemo runs 2 workers over 6 jobs. With signal=true it cancels the pool
// as soon as both workers are busy. With signal=false it closes the queue.
func poolDemo(handle func(context.Context, Job) error, work, grace time.Duration, signalled bool) (Stats, int) {
	queue := NewQueue(6)
	for id := range 6 {
		queue.Push(Job{ID: id, Work: work})
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	started := make(chan struct{}, 6)
	pool := &Pool{Workers: 2, Grace: grace, Handle: func(ctx context.Context, j Job) error {
		started <- struct{}{}
		return handle(ctx, j)
	}}

	if signalled {
		go func() {
			<-started
			<-started
			cancel()
		}()
	} else {
		queue.Close()
	}

	stats := pool.Run(ctx, queue.Jobs())
	queue.Close()
	return stats, len(queue.Leftover())
}

func toggleDebug(level *slog.LevelVar) {
	if level.Level() == slog.LevelDebug {
		level.Set(slog.LevelInfo)
	} else {
		level.Set(slog.LevelDebug)
	}
}

// sendSelf sends a signal to this process, like `kill -TERM <our pid>`.
func sendSelf(sig os.Signal) error {
	p, err := os.FindProcess(os.Getpid())
	if err != nil {
		return err
	}
	if err := p.Signal(sig); err != nil {
		return fmt.Errorf("sending %v (this lesson needs Linux, macOS or WSL): %w", sig, err)
	}
	return nil
}

// GOING FURTHER
//   - LOAD BALANCERS: there's a moment after SIGTERM when traffic is still
//     being routed to you. Kubernetes setups often make /health return 503
//     first, sleep a few seconds, THEN call Shutdown.
//   - WEBSOCKETS: Shutdown doesn't wait for hijacked connections. Use
//     srv.RegisterOnShutdown to tell them to close.
//   - STOPPING HANDLERS SOONER: http.Server.BaseContext sets the parent of
//     every r.Context(), so handlers can be told as soon as a signal lands.
//   - DOCKER: use the exec form, CMD ["/app"]. With the shell form,
//     CMD /app, the shell is PID 1 and doesn't pass SIGTERM on (lesson 23).
//   - `go run` DOESN'T FORWARD `kill`: Ctrl+C reaches both, but kill only
//     reaches the go tool. To test with kill, use `go build` and run that.
//   - ERRGROUP: golang.org/x/sync/errgroup tidies up "run these goroutines,
//     stop them all if one fails".
//
// THINGS TO TRY
//   - In -serve mode, run `curl 'localhost:8080/slow?ms=2000'` in a second
//     terminal and press Ctrl+C on the server while it waits. curl still
//     gets its answer.
//   - Try ms=20000 instead: the grace period runs out. Then try again and
//     press Ctrl+C twice.
//   - Queue some jobs with `curl -X POST 'localhost:8080/jobs?ms=2000'`
//     and watch the "workers stopped" line.
//   - In Serve, change context.WithoutCancel(ctx) to plain ctx and run the
//     tests. TestServeFinishesInFlightRequest catches the classic mistake.
//   - In Pool.Run, change context.WithoutCancel(ctx) to plain ctx. Ctrl+C
//     now aborts every job, and TestPoolFinishesInFlightJobs fails.
//   - Delete the first select in the worker loop and run
//     `go test -count=50 -run TestPoolStopsTakingJobs ./19-graceful-shutdown`.
