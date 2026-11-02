// LESSON 15 — FILES & STREAMING: os, bufio, io.Reader / io.Writer, big CSVs
//
// Run with:  go run ./15-files-streaming
// Test with: go test -v ./15-files-streaming
//
// Everything here happens in a temporary folder that's deleted at the end.
//
// THE BIG IDEA. In PHP it's tempting to write
//
//	$rows = array_map('str_getcsv', file('orders.csv'));
//
// which loads the whole file into memory. Fine for 1,000 rows, a fatal
// "Allowed memory size exhausted" for 10 million. Go's file handling is
// built around STREAMING: read a bit, process it, throw it away, repeat.
// Memory stays flat however big the file is.
//
// Streaming rests on two tiny interfaces from the io package:
//
//	type Reader interface { Read(p []byte) (n int, err error) }
//	type Writer interface { Write(p []byte) (n int, err error) }
//
// Files, network connections, HTTP bodies, gzip streams, strings, hashes,
// and os.Stdin / os.Stdout all implement one or both. Write a function
// that takes an io.Reader and it works with ALL of them (lesson 06's
// "accept interfaces" idea, paying off).
package main

import (
	"bufio"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/csv"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math/rand/v2"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"
)

// main is tiny on purpose. All the work happens in run(), which returns an
// error. That way every `defer` in run() (closing files, deleting the temp
// folder) still runs on failure. os.Exit and log.Fatal skip defers, so call
// them only from main.
func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	ctx := context.Background()

	// os.MkdirTemp makes a uniquely named folder (like PHP's sys_get_temp_dir()
	// plus a random name). RemoveAll is `rm -rf`, so be careful where you point it.
	dir, err := os.MkdirTemp("", "lesson15-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	// filepath.Join uses the right separator for the OS (/ or \).
	// Never build paths with string concatenation.
	notesPath := filepath.Join(dir, "notes.txt")
	logPath := filepath.Join(dir, "app.log")
	csvPath := filepath.Join(dir, "orders.csv")
	gzPath := csvPath + ".gz"

	// ---------------------------------------------------------------
	// 1. SMALL FILES IN ONE GO
	// ---------------------------------------------------------------
	fmt.Println("1. Whole-file read and write")

	// file_put_contents(). 0o644 is the Unix permission (rw-r--r--).
	if err := os.WriteFile(notesPath, []byte("Go reads files as bytes.\n"), 0o644); err != nil {
		return err
	}
	// file_get_contents(). Returns []byte; convert to string to print.
	data, err := os.ReadFile(notesPath)
	if err != nil {
		return err
	}
	fmt.Printf("    read %d bytes: %q\n", len(data), data)

	// file_exists(). Missing files are just an error you can check with errors.Is.
	if _, err := os.Stat(filepath.Join(dir, "nope.txt")); errors.Is(err, fs.ErrNotExist) {
		fmt.Println("    nope.txt does not exist")
	}

	// ---------------------------------------------------------------
	// 2. BUFFERED WRITING AND LINE-BY-LINE READING
	// ---------------------------------------------------------------
	fmt.Println("\n2. bufio.Writer and bufio.Scanner")
	if err := writeLog(logPath); err != nil {
		return err
	}
	errorLines, err := grep(logPath, "ERROR")
	if err != nil {
		return err
	}
	for _, l := range errorLines {
		fmt.Println("   ", l)
	}

	// ---------------------------------------------------------------
	// 3. WRITE A BIG CSV, ONE ROW AT A TIME
	// ---------------------------------------------------------------
	fmt.Println("\n3. Generating a CSV")
	const rows = 200_000 // underscores in numbers are just for readability
	start := time.Now()
	if err := writeOrders(csvPath, rows); err != nil {
		return err
	}
	fmt.Printf("    wrote %d rows (%s) in %v\n", rows, fileSize(csvPath), since(start))

	// ---------------------------------------------------------------
	// 4. LOAD IT ALL vs STREAM IT
	// ---------------------------------------------------------------
	fmt.Println("\n4. ReadAll vs streaming")

	// The PHP-style way: every row in memory at once.
	start = time.Now()
	before := liveHeapMB()
	records, err := readAllOrders(csvPath)
	if err != nil {
		return err
	}
	held := liveHeapMB() - before
	// Go is clever: it sees `records` isn't used after this point and would
	// free it DURING liveHeapMB(), making ReadAll look free. KeepAlive marks
	// it as still in use up to here, so we measure what a real program
	// holding the rows would hold.
	runtime.KeepAlive(records)
	fmt.Printf("    ReadAll:   %d records, %.1f MB held in memory, %v\n", len(records), held, since(start))

	// The streaming way: one row in memory at a time.
	start = time.Now()
	before = liveHeapMB()
	summary, err := sumOrdersFile(ctx, csvPath)
	if err != nil {
		return err
	}
	held = max(liveHeapMB()-before, 0) // can dip below 0 as ReadAll's memory is freed
	fmt.Printf("    Streaming: %d rows, %.1f MB held in memory, %v\n", summary.Rows, held, since(start))
	printSummary(summary)

	// ---------------------------------------------------------------
	// 5. COMPOSING READERS AND WRITERS
	// ---------------------------------------------------------------
	fmt.Println("\n5. gzip + sha256 in one pass, then sum the gzipped file")
	hash, err := gzipAndHash(csvPath, gzPath)
	if err != nil {
		return err
	}
	fmt.Printf("    %s -> %s, sha256 %s...\n", fileSize(csvPath), fileSize(gzPath), hash[:16])

	// The SAME sumOrders function reads the compressed file, unchanged,
	// because a gzip.Reader is just another io.Reader.
	gzSummary, err := sumOrdersGzip(ctx, gzPath)
	if err != nil {
		return err
	}
	fmt.Printf("    total from .gz: £%s (matches: %v)\n",
		pounds(gzSummary.TotalPence), gzSummary.TotalPence == summary.TotalPence)

	// ---------------------------------------------------------------
	// 6. STREAMING + CONTEXT (lesson 14)
	// ---------------------------------------------------------------
	fmt.Println("\n6. Cancelling a long read")
	tight, cancel := context.WithTimeout(ctx, 2*time.Millisecond)
	defer cancel()
	partial, err := sumOrdersFile(tight, csvPath)
	fmt.Printf("    got through %d of %d rows: %v\n", partial.Rows, rows, err)

	// ---------------------------------------------------------------
	// 7. LISTING A DIRECTORY
	// ---------------------------------------------------------------
	fmt.Println("\n7. What's in the temp folder (like scandir)")
	entries, err := os.ReadDir(dir) // sorted by name
	if err != nil {
		return err
	}
	for _, e := range entries {
		fmt.Printf("    %-14s %s\n", e.Name(), fileSize(filepath.Join(dir, e.Name())))
	}
	return nil
}

// --- Section 2 --------------------------------------------------------------

// writeLog writes a few lines through a bufio.Writer.
//
// Every f.Write is a system call, which is slow if you do millions of tiny
// ones. bufio.Writer collects writes in memory (4 KB by default) and passes
// them to the file in big chunks. The catch: you MUST call Flush() at the
// end, or the last chunk is silently lost.
//
// Note the NAMED RETURN `err` (lesson 03). The deferred func can change
// it, so an error from Close() isn't thrown away. For files you WRITE,
// Close can fail (disk full, network drive), and that means data loss.
func writeLog(path string) (err error) {
	f, err := os.Create(path) // fopen($path, 'w'): create or truncate
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	w := bufio.NewWriter(f)
	lines := []string{
		"INFO  app started",
		"INFO  user 7 logged in",
		"ERROR payment 1001 declined",
		"INFO  user 7 logged out",
		"ERROR disk 91% full",
	}
	for _, l := range lines {
		// fmt.Fprintln writes to ANY io.Writer, not just the terminal.
		if _, err := fmt.Fprintln(w, l); err != nil {
			return err
		}
	}
	return w.Flush() // comment this out and grep() finds nothing
}

// grep returns lines containing substr, with line numbers.
//
// bufio.Scanner is Go's `while (($line = fgets($fh)) !== false)`. It reads
// a chunk at a time and hands you one line (without the \n) per Scan().
// Lines longer than 64 KB make it fail with bufio.ErrTooLong; raise the
// limit with scanner.Buffer() if you need to.
func grep(path, substr string) ([]string, error) {
	f, err := os.Open(path) // read-only. Closing a read-only file can't lose data,
	if err != nil {         // so a plain defer is fine here.
		return nil, err
	}
	defer f.Close()

	var matches []string
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		if strings.Contains(sc.Text(), substr) {
			matches = append(matches, fmt.Sprintf("line %d: %s", n, sc.Text()))
		}
	}
	// Scan() returns false at EOF *or* on an error. Err() tells them apart
	// (it returns nil at a normal EOF).
	return matches, sc.Err()
}

// --- Section 3 --------------------------------------------------------------

var orderHeader = []string{"id", "customer", "amount_pence", "country"}

// writeOrders generates a CSV with a few deliberately broken rows.
func writeOrders(path string, n int) (err error) {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if cerr := f.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	// A fixed seed gives the same "random" data every run.
	rng := rand.New(rand.NewPCG(15, 15))
	customers := []string{"Ada", "Grace", "Linus", `Dave "The Wave"`, "Smith, Jones & Co"}
	countries := []string{"GB", "IE", "FR", "DE"}

	// csv.Writer handles quoting (commas, quotes in names) and buffers
	// internally, so there's no need to wrap it in bufio. Like fputcsv().
	w := csv.NewWriter(f)
	w.Write(orderHeader)
	for i := 1; i <= n; i++ {
		rec := []string{
			strconv.Itoa(i),
			customers[rng.IntN(len(customers))],
			strconv.Itoa(rng.IntN(10_000)), // up to £99.99
			countries[rng.IntN(len(countries))],
		}
		switch {
		case i%40_000 == 0:
			rec[2] = "oops" // bad amount
		case i%75_000 == 0:
			rec = rec[:3] // missing column
		}
		// Write errors are sticky: we can skip checking each one and check
		// w.Error() once at the end, after Flush.
		w.Write(rec)
	}
	w.Flush()
	return w.Error()
}

// --- Section 4 --------------------------------------------------------------

// readAllOrders is the "load everything" approach, for comparison only.
func readAllOrders(path string) ([][]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	r := csv.NewReader(f)
	r.FieldsPerRecord = -1 // allow rows with the wrong number of columns
	return r.ReadAll()
}

// Summary is what we keep while streaming: a few numbers, not the rows.
type Summary struct {
	Rows       int
	TotalPence int64
	ByCountry  map[string]int64
	BadRows    int
	Problems   []string // the first few, for display
}

func (s *Summary) problem(format string, args ...any) {
	s.BadRows++
	if len(s.Problems) < 3 {
		s.Problems = append(s.Problems, fmt.Sprintf(format, args...))
	}
}

// sumOrders streams orders from ANY io.Reader: a file, a gzip stream,
// os.Stdin, an HTTP upload, or a strings.Reader in a test.
func sumOrders(ctx context.Context, r io.Reader) (Summary, error) {
	s := Summary{ByCountry: map[string]int64{}}

	cr := csv.NewReader(r)
	// ReuseRecord lets Read() reuse the same []string for every row instead
	// of allocating a new one. Faster, but don't keep a reference to a row
	// after the next Read() — copy what you need.
	cr.ReuseRecord = true

	header, err := cr.Read()
	if err != nil {
		return s, fmt.Errorf("reading header: %w", err)
	}
	if !slices.Equal(header, orderHeader) {
		return s, fmt.Errorf("unexpected header %q, want %q", header, orderHeader)
	}

	for {
		// Checking ctx on every row is wasted effort; every 1,000 is plenty.
		if s.Rows%1000 == 0 {
			if err := ctx.Err(); err != nil {
				return s, err
			}
		}

		rec, err := cr.Read()
		if errors.Is(err, io.EOF) {
			return s, nil // finished
		}

		// A malformed row is a *csv.ParseError (lesson 07's errors.As).
		// Record it and carry on: one bad line shouldn't abort a big import.
		// Anything else (a disk or network error) is fatal.
		var pe *csv.ParseError
		if errors.As(err, &pe) {
			s.Rows++
			s.problem("line %d: %v", pe.Line, pe.Err)
			continue
		}
		if err != nil {
			return s, err
		}
		s.Rows++

		amount, err := strconv.ParseInt(rec[2], 10, 64)
		if err != nil {
			line, _ := cr.FieldPos(2)
			s.problem("line %d: bad amount %q", line, rec[2])
			continue
		}
		s.TotalPence += amount
		s.ByCountry[rec[3]] += amount
	}
}

func sumOrdersFile(ctx context.Context, path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()
	return sumOrders(ctx, f)
}

// --- Section 5 --------------------------------------------------------------

// gzipAndHash compresses src into dst and returns src's SHA-256, reading
// the file only once.
//
//	file --io.Copy--> MultiWriter --+--> gzip.Writer --> dst file
//	                                +--> sha256 hash
//
// io.Copy moves data from a Reader to a Writer in 32 KB chunks.
// io.MultiWriter is a Writer that copies everything to several Writers,
// like the Unix `tee` command.
func gzipAndHash(src, dst string) (hash string, err error) {
	in, err := os.Open(src)
	if err != nil {
		return "", err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return "", err
	}
	defer func() {
		if cerr := out.Close(); cerr != nil && err == nil {
			err = cerr
		}
	}()

	zw := gzip.NewWriter(out)
	h := sha256.New() // a hash is an io.Writer too: hash_file('sha256', ...)

	if _, err := io.Copy(io.MultiWriter(zw, h), in); err != nil {
		return "", err
	}
	// gzip.Writer.Close writes the gzip footer. It's like Flush: skip it
	// and the .gz file is corrupt.
	if err := zw.Close(); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func sumOrdersGzip(ctx context.Context, path string) (Summary, error) {
	f, err := os.Open(path)
	if err != nil {
		return Summary{}, err
	}
	defer f.Close()

	// Readers wrap readers: file -> gzip decompressor -> CSV parser.
	// The data is decompressed a chunk at a time as the CSV reader asks.
	zr, err := gzip.NewReader(f)
	if err != nil {
		return Summary{}, err
	}
	defer zr.Close()
	return sumOrders(ctx, zr)
}

// --- Helpers ----------------------------------------------------------------

func printSummary(s Summary) {
	fmt.Printf("    total £%s, %d bad rows\n", pounds(s.TotalPence), s.BadRows)
	// Map order is random (lesson 04), so sort the keys for stable output.
	for _, c := range slices.Sorted(maps.Keys(s.ByCountry)) {
		fmt.Printf("      %s £%s\n", c, pounds(s.ByCountry[c]))
	}
	for _, p := range s.Problems {
		fmt.Println("      problem:", p)
	}
}

// pounds formats pence as pounds. Money is stored as whole pence (int64),
// never float64, to avoid rounding errors. Same rule as in PHP.
func pounds(pence int64) string {
	return fmt.Sprintf("%d.%02d", pence/100, pence%100)
}

func fileSize(path string) string {
	info, err := os.Stat(path)
	if err != nil {
		return "?"
	}
	if info.Size() < 1e6 {
		return fmt.Sprintf("%d bytes", info.Size())
	}
	return fmt.Sprintf("%.1f MB", float64(info.Size())/1e6)
}

// liveHeapMB forces a garbage collection, then reports how much memory is
// still in use. Good enough for a rough before/after comparison.
func liveHeapMB() float64 {
	runtime.GC()
	var m runtime.MemStats
	runtime.ReadMemStats(&m)
	return float64(m.HeapAlloc) / 1e6
}

func since(t time.Time) time.Duration {
	return time.Since(t).Round(time.Millisecond)
}

// THINGS TO TRY
//   - Comment out `return w.Flush()` in writeLog (return nil instead) and
//     see section 2 print nothing.
//   - Change rows to 2_000_000. The ReadAll figure grows to hundreds of
//     MB; the streaming figure barely moves.
//   - Read from stdin instead of a file: sumOrders(ctx, os.Stdin) works
//     because os.Stdin is an io.Reader. (Lesson 22 builds on this.)
