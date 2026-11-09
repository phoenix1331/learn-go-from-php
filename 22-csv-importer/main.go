// LESSON 22 — PROJECT: A CSV IMPORTER CALLED FROM LARAVEL
//
// Try it:    go run ./22-csv-importer -demo
// Test with: go test -race -v ./22-csv-importer
//
// This one is a real tool, pulling together streaming (15), the database
// (17), structured logs (18), signals (19) and testing (20).
//
//	go build -o bin/csvimport ./22-csv-importer
//	bin/csvimport -generate=1000000 > customers.csv
//	bin/csvimport -db=app.sqlite -init -max-rejects=0 customers.csv
//
// On one test machine that took 10 seconds and 24 MB of memory for a million
// rows. Flags must come before the filename: Go's flag package stops at the
// first argument that isn't a flag.
//
// The files:
//   - validate.go  header and row rules, like a FormRequest
//   - importer.go  the streaming loop
//   - sink.go      SQLite batches and upserts
//   - main.go      the command line: flags, output and exit codes
//   - laravel/     the Artisan command and migration, to copy into your app
//
// WHY GO FOR THIS? A PHP import of a big file fights memory limits and
// Eloquent's overhead, and usually gets split across queue jobs. A single Go
// binary streams the file at a steady, small memory use, with nothing to
// install on the server.
//
// THE CONTRACT WITH LARAVEL. A CLI tool's real API is its streams and exit code:
//
//	stdout   exactly one JSON document, the Summary. Data, so nothing else.
//	stderr   JSON log lines, including progress. Laravel can show these live.
//
//	exit 0   every row imported
//	exit 1   the import failed (unreadable file, bad header, database error)
//	exit 2   wrong flags: a bug in the calling code
//	exit 3   finished, but some rows were rejected
//	exit 4   stopped early: too many rejects, or cancelled
//
// Exits 1, 3 and 4 still print the Summary, so PHP can always read the details.
//
// SET -timeout LOWER THAN Process::timeout. When Laravel's timeout expires,
// Symfony sends SIGTERM and then SIGKILL about a millisecond later, which is
// no time to clean up. With -timeout the binary stops itself between rows
// and still reports what it got through.
//
// IF IT DIES ANYWAY, just run the same file again. Finished batches are
// updated rather than duplicated, which is the point of using an upsert.
package main

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	exitOK           = 0
	exitFailed       = 1
	exitUsage        = 2
	exitRejects      = 3
	exitStoppedEarly = 4
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// options holds one import's settings.
type options struct {
	db            string
	init          bool
	rejects       string
	batch         int
	maxRejects    int64
	dryRun        bool
	timeout       time.Duration
	progressEvery int64
	file          string
}

// run is main without the globals: arguments, streams and context are all
// passed in, and it returns the exit code. That makes the whole command line
// testable, flags included, without building a binary. (See cli_test.go.)
func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("csvimport", flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.db, "db", "", "SQLite database file (required unless -dry-run)")
	fs.BoolVar(&o.init, "init", false, "create the customers table if it's missing")
	fs.StringVar(&o.rejects, "rejects", "", "write rejected rows to this CSV file")
	fs.IntVar(&o.batch, "batch", 1000, "rows per database transaction")
	fs.Int64Var(&o.maxRejects, "max-rejects", 1000, "stop after this many rejected rows (0 for no limit)")
	fs.BoolVar(&o.dryRun, "dry-run", false, "validate only, write nothing to the database")
	fs.DurationVar(&o.timeout, "timeout", 0, "stop cleanly after this long (0 for no limit)")
	fs.Int64Var(&o.progressEvery, "progress-every", 50_000, "log progress every N rows (0 for never)")
	demo := fs.Bool("demo", false, "run a guided demo on generated data")
	generate := fs.Int("generate", 0, "print a sample CSV with this many rows, then exit")
	fs.Usage = func() {
		fmt.Fprintln(stderr, "usage: csvimport -db=FILE [flags] FILE.csv  (use - for stdin)")
		fs.PrintDefaults()
	}

	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage // the flag package has already explained the problem
	}

	logger := slog.New(slog.NewJSONHandler(stderr, nil))
	switch {
	case *demo:
		return runDemo(ctx, stdout, logger)
	case *generate > 0:
		if err := writeSample(stdout, *generate); err != nil {
			fmt.Fprintln(stderr, "error:", err)
			return exitFailed
		}
		return exitOK
	}

	if fs.NArg() != 1 {
		fmt.Fprintln(stderr, "error: expected exactly one CSV file")
		return exitUsage
	}
	o.file = fs.Arg(0)
	if o.batch < 1 {
		fmt.Fprintln(stderr, "error: -batch must be at least 1")
		return exitUsage
	}
	if o.db == "" && !o.dryRun {
		fmt.Fprintln(stderr, "error: -db is required unless -dry-run is set")
		return exitUsage
	}

	summary := importFile(ctx, o, stdin, logger)

	// Printing to stdout can't usefully fail: if PHP has gone, so has the
	// point. The exit code is still accurate.
	if err := json.NewEncoder(stdout).Encode(summary); err != nil {
		logger.Error("writing summary", "err", err)
	}
	return exitCode(summary.Status)
}

func exitCode(status string) int {
	switch status {
	case StatusCompleted:
		return exitOK
	case StatusWithRejects:
		return exitRejects
	case StatusTooMany, StatusCancelled:
		return exitStoppedEarly
	default:
		return exitFailed
	}
}

// importFile does the work. Any failure is recorded in the Summary, because
// Laravel should always get a JSON answer.
func importFile(ctx context.Context, o options, stdin io.Reader, logger *slog.Logger) Summary {
	start := time.Now()

	s, err := doImport(ctx, o, stdin, logger)
	if err != nil {
		s.Status = StatusFailed
		s.Error = err.Error()
		if s.FirstRejects == nil {
			s.FirstRejects = []Reject{} // JSON [] rather than null, for PHP
		}
	}
	s.DurationMS = time.Since(start).Milliseconds()
	logger.Info("import finished", "status", s.Status, "rows", s.Rows, "imported", s.Imported, "rejected", s.Rejected)
	return s
}

func doImport(ctx context.Context, o options, stdin io.Reader, logger *slog.Logger) (Summary, error) {
	src := stdin
	if o.file != "-" {
		f, err := os.Open(o.file)
		if err != nil {
			return Summary{DryRun: o.dryRun}, err
		}
		defer f.Close()
		src = f
	}

	im := &Importer{
		BatchSize:     o.batch,
		MaxRejects:    o.maxRejects,
		ProgressEvery: o.progressEvery,
		Progress: func(s Summary) {
			logger.Info("progress", "rows", s.Rows, "imported", s.Imported, "rejected", s.Rejected)
		},
	}

	var sink *SQLiteSink
	if !o.dryRun {
		var err error
		if sink, err = OpenSQLite(ctx, o.db); err != nil {
			return Summary{}, fmt.Errorf("opening database: %w", err)
		}
		defer sink.Close()
		if o.init {
			if err := sink.Init(ctx); err != nil {
				return Summary{}, fmt.Errorf("creating table: %w", err)
			}
		}
		im.Sink = sink // set only here: a nil *SQLiteSink in an interface isn't nil
	}

	var before int64
	if sink != nil {
		var err error
		if before, err = sink.Count(ctx); err != nil {
			return Summary{}, fmt.Errorf("reading table (was the migration run?): %w", err)
		}
	}

	var rejects *os.File
	if o.rejects != "" {
		var err error
		if rejects, err = os.Create(o.rejects); err != nil {
			return Summary{DryRun: o.dryRun}, fmt.Errorf("creating rejects file: %w", err)
		}
		defer rejects.Close()
		im.Rejects = rejects // same nil-interface reason as above
	}

	// The clock starts here, so -timeout covers only the import itself.
	runCtx := ctx
	if o.timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, o.timeout)
		defer cancel()
	}
	s, err := im.Run(runCtx, src)

	if sink != nil {
		// WithoutCancel: a cancelled import should still report its numbers.
		after, countErr := sink.Count(context.WithoutCancel(ctx))
		if countErr != nil && err == nil {
			err = fmt.Errorf("counting rows: %w", countErr)
		}
		if countErr == nil {
			s.Inserted = after - before
			s.Updated = s.Imported - s.Inserted
		}
	}

	if rejects != nil {
		if s.Rejected > 0 {
			s.RejectsFile = o.rejects
		} else {
			rejects.Close()
			os.Remove(o.rejects) // no problems, so no empty file to confuse anyone
		}
	}
	return s, err
}

// runDemo shows the normal workflow on generated data.
func runDemo(ctx context.Context, out io.Writer, logger *slog.Logger) int {
	dir, err := os.MkdirTemp("", "csvimport-demo")
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return exitFailed
	}
	defer os.RemoveAll(dir)

	const rows = 20_000
	csvPath := filepath.Join(dir, "customers.csv")
	f, err := os.Create(csvPath)
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return exitFailed
	}
	err = writeSample(f, rows)
	if closeErr := f.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return exitFailed
	}

	base := options{
		db: filepath.Join(dir, "app.sqlite"), init: true,
		rejects: filepath.Join(dir, "rejects.csv"),
		batch:   1000, file: csvPath,
	}
	show := func(title string, o options) Summary {
		s := importFile(ctx, o, nil, slog.New(slog.DiscardHandler))
		fmt.Fprintf(out, "\n%s\n    status=%s rows=%d valid=%d rejected=%d imported=%d inserted=%d updated=%d exit=%d (%dms)\n",
			title, s.Status, s.Rows, s.Valid, s.Rejected, s.Imported, s.Inserted, s.Updated, exitCode(s.Status), s.DurationMS)
		return s
	}

	fmt.Fprintf(out, "Generated %d rows, some of them deliberately bad.\n", rows)

	dry := base
	dry.dryRun = true
	show("1. Dry run: find the problems, write nothing", dry)

	first := show("2. Real import: batches of 1000, bad rows set aside", base)
	for _, r := range first.FirstRejects[:min(4, len(first.FirstRejects))] {
		fmt.Fprintf(out, "    line %d: %s\n", r.Line, strings.Join(r.Errors, "; "))
	}

	show("3. Same file again: updates, no duplicates", base)

	tooStrict := base
	tooStrict.maxRejects = 5
	show("4. With -max-rejects=5: stops early, as if it were the wrong file", tooStrict)

	raw, err := os.ReadFile(base.rejects)
	if err != nil {
		fmt.Fprintln(out, "error:", err)
		return exitFailed
	}
	lines := strings.SplitN(string(raw), "\n", 3)
	fmt.Fprintln(out, "\n5. The rejects file, ready to fix and upload again")
	for _, line := range lines[:min(2, len(lines))] {
		fmt.Fprintln(out, "    "+line)
	}
	return exitOK
}

// writeSample creates a realistic file: a BOM like Excel adds, names needing
// quotes, some repeated emails and a small share of bad rows.
func writeSample(out io.Writer, rows int) error {
	if _, err := io.WriteString(out, "\ufeff"); err != nil {
		return err
	}
	w := csv.NewWriter(out)
	if err := w.Write([]string{"email", "name", "country", "plan", "signed_up"}); err != nil {
		return err
	}

	plans := []string{"free", "pro", "team"}
	for i := 1; i <= rows; i++ {
		email := "user" + strconv.Itoa(i) + "@example.com"
		if i%701 == 0 {
			email = "user1@example.com" // a repeat, which becomes an update
		}
		row := []string{email, "Lovelace, Ada " + strconv.Itoa(i), "GB", plans[i%3], "2026-03-14"}
		if i%500 == 0 {
			switch (i / 500) % 5 {
			case 0:
				row[0] = "not-an-email"
			case 1:
				row[1] = ""
			case 2:
				row[2], row[3] = "Britain", "gold"
			case 3:
				row[4] = "14/03/2026"
			case 4:
				row = row[:3]
			}
		}
		if err := w.Write(row); err != nil {
			return err
		}
	}
	w.Flush()
	return w.Error()
}

// GOING FURTHER
//   - MYSQL / POSTGRES: write another Sink. Upserts are spelled differently
//     (ON DUPLICATE KEY UPDATE on MySQL), and multi-row INSERTs help a lot.
//   - HUGE QUOTED FIELDS: a stray opening quote makes the CSV reader treat
//     the rest of the file as one field, so memory can grow with file size.
//     Use io.LimitReader or a size check on files you don't trust.
//   - CSV INJECTION: the rejects file repeats what users uploaded. A value
//     like =HYPERLINK(...) runs as a formula if someone opens it in Excel.
//     Be careful about giving these files to staff.
//   - RUNNING IT QUEUED: call this from a Laravel job, not a web request,
//     and match the job's $timeout to Process::timeout.
//
// THINGS TO TRY
//   - Run the 1,000,000 row commands at the top of this file with
//     `/usr/bin/time -v` and look at "Maximum resident set size".
//   - Remove the BOM TrimPrefix in Importer.Run and run the demo.
//   - Set -batch=1 on 20,000 rows and compare the time.
//   - Add a -delimiter flag for European Excel's semicolons (cr.Comma).
//   - Add gzip support with lesson 15's compress/gzip.
