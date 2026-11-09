package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// These test the tool the way Laravel uses it: arguments in; stdout, stderr
// and exit code out. run() takes all of them as parameters, so no binary
// needs building, and SQLite is a real file in t.TempDir().

type cliResult struct {
	code           int
	stdout, stderr string
}

func cli(t *testing.T, stdin string, args ...string) cliResult {
	t.Helper()
	var out, errOut bytes.Buffer
	code := run(t.Context(), args, strings.NewReader(stdin), &out, &errOut)
	return cliResult{code, out.String(), errOut.String()}
}

// summary also enforces the contract: stdout is one JSON document.
func (r cliResult) summary(t *testing.T) Summary {
	t.Helper()
	dec := json.NewDecoder(strings.NewReader(r.stdout))
	var s Summary
	if err := dec.Decode(&s); err != nil {
		t.Fatalf("stdout isn't JSON: %q: %v", r.stdout, err)
	}
	if dec.More() {
		t.Fatalf("stdout has more than one document: %q", r.stdout)
	}
	return s
}

func dbPath(t *testing.T) string {
	return "-db=" + filepath.Join(t.TempDir(), "app.sqlite")
}

func queryCustomer(t *testing.T, path string) (name, createdAt, updatedAt string) {
	t.Helper()
	sink, err := OpenSQLite(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	err = sink.db.QueryRowContext(t.Context(),
		`SELECT name, CAST(created_at AS TEXT), CAST(updated_at AS TEXT) FROM customers WHERE email = 'ada@example.com'`,
	).Scan(&name, &createdAt, &updatedAt)
	if err != nil {
		t.Fatal(err)
	}
	return name, createdAt, updatedAt
}

func TestCLIImports(t *testing.T) {
	r := cli(t, header+good, dbPath(t), "-init", "-")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	s := r.summary(t)
	if s.Status != StatusCompleted || s.Inserted != 1 || s.Updated != 0 {
		t.Errorf("summary: %+v", s)
	}
	if !strings.Contains(r.stderr, `"msg":"import finished"`) {
		t.Errorf("expected JSON logs on stderr, got %q", r.stderr)
	}
}

func TestCLIExitCodes(t *testing.T) {
	tests := []struct {
		name  string
		stdin string
		args  []string
		code  int
		state string
	}{
		{"rejects", header + good + "bad,,,,\n", []string{"-init"}, exitRejects, StatusWithRejects},
		{"too many rejects", header + "bad,,,,\nbad,,,,\n", []string{"-init", "-max-rejects=1"}, exitStoppedEarly, StatusTooMany},
		{"bad header", "a,b\n1,2\n", []string{"-init"}, exitFailed, StatusFailed},
		{"table missing", header + good, nil, exitFailed, StatusFailed},
		{"already timed out", header + good, []string{"-init", "-timeout=1ns"}, exitStoppedEarly, StatusCancelled},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			args := append([]string{dbPath(t)}, tt.args...)
			args = append(args, "-")
			r := cli(t, tt.stdin, args...)
			if r.code != tt.code {
				t.Errorf("exit %d, want %d; stderr: %s", r.code, tt.code, r.stderr)
			}
			// Even a failure must give PHP a JSON answer.
			if s := r.summary(t); s.Status != tt.state || s.FirstRejects == nil {
				t.Errorf("summary: %+v", s)
			}
		})
	}
}

func TestCLIUsageErrors(t *testing.T) {
	tests := map[string][]string{
		"no file":      {"-db=x"},
		"no database":  {"file.csv"},
		"unknown flag": {"-nope"},
		"bad batch":    {"-db=x", "-batch=0", "file.csv"},
	}
	for name, args := range tests {
		t.Run(name, func(t *testing.T) {
			r := cli(t, "", args...)
			if r.code != exitUsage || r.stdout != "" {
				t.Errorf("exit %d, stdout %q", r.code, r.stdout)
			}
		})
	}
}

func TestCLIMissingFile(t *testing.T) {
	r := cli(t, "", "-dry-run", filepath.Join(t.TempDir(), "nope.csv"))
	if r.code != exitFailed || r.summary(t).Status != StatusFailed {
		t.Errorf("exit %d: %s", r.code, r.stdout)
	}
}

func TestCLIDryRunCreatesNothing(t *testing.T) {
	dir := t.TempDir()
	db := filepath.Join(dir, "app.sqlite")
	r := cli(t, header+good, "-db="+db, "-dry-run", "-")
	if r.code != exitOK {
		t.Fatalf("exit %d: %s", r.code, r.stderr)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Errorf("dry run touched the database: %v", err)
	}
}

func TestCLIRejectsFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "rejects.csv")

	r := cli(t, header+"bad,,,,\n", "-dry-run", "-rejects="+path, "-")
	if got := r.summary(t).RejectsFile; got != path {
		t.Errorf("RejectsFile = %q", got)
	}
	if _, err := os.Stat(path); err != nil {
		t.Error(err)
	}

	// With nothing rejected, there's no file left to confuse anyone.
	r = cli(t, header+good, "-dry-run", "-rejects="+path, "-")
	if got := r.summary(t).RejectsFile; got != "" {
		t.Errorf("RejectsFile = %q", got)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("empty rejects file left behind: %v", err)
	}
}

// Running a file twice must be safe: this is the recovery plan after a crash.
func TestUpsertMakesReRunsSafe(t *testing.T) {
	db := filepath.Join(t.TempDir(), "app.sqlite")

	first := cli(t, header+good, "-db="+db, "-init", "-").summary(t)
	_, created, _ := queryCustomer(t, db)

	changed := "ada@example.com,Countess,GB,pro,2026-03-14\n"
	second := cli(t, header+changed, "-db="+db, "-").summary(t)

	if first.Inserted != 1 || second.Inserted != 0 || second.Updated != 1 {
		t.Errorf("first %+v, second %+v", first, second)
	}
	name, createdAgain, _ := queryCustomer(t, db)
	if name != "Countess" {
		t.Errorf("name = %q, want the new value", name)
	}
	if createdAgain != created {
		t.Errorf("created_at changed from %q to %q", created, createdAgain)
	}
}

func TestTimestampsUseLaravelFormat(t *testing.T) {
	db := filepath.Join(t.TempDir(), "app.sqlite")
	sink, err := OpenSQLite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err := sink.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	sink.Now = func() time.Time { return time.Date(2026, 3, 14, 15, 9, 26, 0, time.UTC) }

	err = sink.Upsert(t.Context(), []Customer{{Email: "ada@example.com", Name: "A", Country: "GB", Plan: "pro", SignedUp: "2026-03-14"}})
	if err != nil {
		t.Fatal(err)
	}
	if _, created, updated := queryCustomer(t, db); created != "2026-03-14 15:09:26" || updated != created {
		t.Errorf("created_at %q, updated_at %q", created, updated)
	}
}

func TestSinkRollsBackWholeBatch(t *testing.T) {
	db := filepath.Join(t.TempDir(), "app.sqlite")
	sink, err := OpenSQLite(t.Context(), db)
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err := sink.Init(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := sink.db.ExecContext(t.Context(),
		`CREATE TRIGGER refuse BEFORE INSERT ON customers WHEN NEW.email = 'bad@example.com'
		 BEGIN SELECT RAISE(ABORT, 'refused'); END`); err != nil {
		t.Fatal(err)
	}

	err = sink.Upsert(t.Context(), []Customer{{Email: "ok@example.com"}, {Email: "bad@example.com"}})
	if err == nil {
		t.Fatal("expected an error")
	}
	if n, err := sink.Count(context.Background()); err != nil || n != 0 {
		t.Errorf("count = %d, err %v, want the whole batch rolled back", n, err)
	}
}

func TestGenerateProducesImportableFile(t *testing.T) {
	sample := cli(t, "", "-generate=1000")
	if sample.code != exitOK {
		t.Fatalf("exit %d", sample.code)
	}
	r := cli(t, sample.stdout, "-dry-run", "-")
	s := r.summary(t)
	if s.Rows != 1000 || s.Rejected != 2 {
		t.Errorf("summary: %+v", s)
	}
}
