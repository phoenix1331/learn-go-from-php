package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Because sumOrders takes an io.Reader, tests can feed it a string.
// No fixture files, no temp folders. This is the payoff of accepting
// interfaces.

func TestSumOrders(t *testing.T) {
	input := `id,customer,amount_pence,country
1,Ada,1000,GB
2,"Smith, Jones & Co",250,FR
3,Grace,oops,GB
4,Linus,500
5,"Dave ""The Wave""",50,GB
`
	s, err := sumOrders(t.Context(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if s.Rows != 5 {
		t.Errorf("Rows = %d, want 5", s.Rows)
	}
	if s.TotalPence != 1300 {
		t.Errorf("TotalPence = %d, want 1300", s.TotalPence)
	}
	if s.ByCountry["GB"] != 1050 || s.ByCountry["FR"] != 250 {
		t.Errorf("ByCountry = %v", s.ByCountry)
	}
	if s.BadRows != 2 {
		t.Errorf("BadRows = %d, want 2 (problems: %v)", s.BadRows, s.Problems)
	}
}

func TestSumOrdersBadHeader(t *testing.T) {
	_, err := sumOrders(t.Context(), strings.NewReader("name,total\nAda,10\n"))
	if err == nil || !strings.Contains(err.Error(), "unexpected header") {
		t.Fatalf("want header error, got %v", err)
	}
}

func TestSumOrdersEmptyInput(t *testing.T) {
	_, err := sumOrders(t.Context(), strings.NewReader(""))
	if err == nil {
		t.Fatal("want an error for empty input, got nil")
	}
}

func TestSumOrdersCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	_, err := sumOrders(ctx, strings.NewReader("id,customer,amount_pence,country\n1,Ada,1,GB\n"))
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want context.Canceled, got %v", err)
	}
}

// When you do need real files, t.TempDir() gives a folder that's deleted
// automatically after the test.
func TestWriteThenGzipRoundTrip(t *testing.T) {
	dir := t.TempDir()
	csvPath := filepath.Join(dir, "orders.csv")
	gzPath := csvPath + ".gz"

	if err := writeOrders(csvPath, 500); err != nil {
		t.Fatal(err)
	}
	plain, err := sumOrdersFile(t.Context(), csvPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := gzipAndHash(csvPath, gzPath); err != nil {
		t.Fatal(err)
	}
	zipped, err := sumOrdersGzip(t.Context(), gzPath)
	if err != nil {
		t.Fatal(err)
	}

	if plain.Rows != 500 || zipped.Rows != 500 {
		t.Errorf("rows: plain %d, gzipped %d, want 500", plain.Rows, zipped.Rows)
	}
	if plain.TotalPence != zipped.TotalPence {
		t.Errorf("totals differ: plain %d, gzipped %d", plain.TotalPence, zipped.TotalPence)
	}
}

func TestGrep(t *testing.T) {
	path := filepath.Join(t.TempDir(), "app.log")
	if err := writeLog(path); err != nil {
		t.Fatal(err)
	}
	got, err := grep(path, "ERROR")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || !strings.HasPrefix(got[0], "line 3:") {
		t.Errorf("grep = %q", got)
	}

	if _, err := grep(filepath.Join(t.TempDir(), "missing.log"), "x"); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("want ErrNotExist for missing file, got %v", err)
	}
}
