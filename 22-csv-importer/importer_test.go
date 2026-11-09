package main

import (
	"bytes"
	"context"
	"encoding/csv"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
)

// fakeSink records each batch, copying it because the importer reuses the
// slice. A sink that kept the slice would be a real bug, and this avoids it.
type fakeSink struct {
	batches [][]Customer
	err     error
}

func (f *fakeSink) Upsert(_ context.Context, batch []Customer) error {
	if f.err != nil {
		return f.err
	}
	f.batches = append(f.batches, slices.Clone(batch))
	return nil
}

func (f *fakeSink) rows() []Customer { return slices.Concat(f.batches...) }

const header = "email,name,country,plan,signed_up\n"
const good = "ada@example.com,Ada,GB,pro,2026-03-14\n"

func runImport(t *testing.T, im *Importer, input string) Summary {
	t.Helper()
	s, err := im.Run(t.Context(), strings.NewReader(input))
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return s
}

func newImporter(sink Sink) *Importer {
	return &Importer{Sink: sink, BatchSize: 2}
}

func TestParseHeader(t *testing.T) {
	tests := []struct {
		name    string
		header  []string
		wantErr string
	}{
		{name: "normal", header: []string{"email", "name", "country", "plan", "signed_up"}},
		{name: "any order", header: []string{"signed_up", "country", "name", "email"}},
		{name: "messy case and spaces", header: []string{" Email", "NAME ", "Country", "Signed_Up"}},
		{name: "extra columns", header: []string{"email", "name", "country", "signed_up", "notes"}},
		{name: "reports all missing", header: []string{"email"}, wantErr: "country, name, signed_up"},
		{name: "duplicate", header: []string{"email", "Email", "name", "country", "signed_up"}, wantErr: "twice"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := parseHeader(tt.header)
			if tt.wantErr == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if !errors.Is(err, ErrBadHeader) || !strings.Contains(err.Error(), tt.wantErr) {
				t.Errorf("err = %v, want ErrBadHeader containing %q", err, tt.wantErr)
			}
		})
	}
}

func TestValidate(t *testing.T) {
	cols, err := parseHeader(strings.Split(strings.TrimSpace(header), ","))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name   string
		record []string
		want   []string // the expected problems; none means valid
	}{
		{name: "valid", record: []string{"ada@example.com", "Ada", "GB", "pro", "2026-03-14"}},
		{name: "blank plan defaults", record: []string{"a@b.co", "A", "GB", "", "2026-03-14"}},
		{name: "bad email", record: []string{"nope", "A", "GB", "pro", "2026-03-14"}, want: []string{"email is not valid"}},
		{name: "email with display name", record: []string{"Ada <a@b.co>", "A", "GB", "pro", "2026-03-14"}, want: []string{"email is not valid"}},
		{name: "impossible date", record: []string{"a@b.co", "A", "GB", "pro", "2026-02-30"}, want: []string{"signed_up must be a real date as YYYY-MM-DD"}},
		{name: "wrong column count", record: []string{"a@b.co"}, want: []string{"expected 5 columns, got 1"}},
		{name: "bad encoding", record: []string{"a@b.co", "Caf\xe9", "GB", "pro", "2026-03-14"}, want: []string{"not valid UTF-8 (save the file as CSV UTF-8)"}},
		{
			name: "every problem at once", record: []string{"nope", "", "GBR", "gold", "soon"},
			want: []string{
				"email is not valid", "name is required", "country must be a 2-letter code",
				"plan must be free, pro or team", "signed_up must be a real date as YYYY-MM-DD",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, got := validate(cols, tt.record); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("problems = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestValidateNormalises(t *testing.T) {
	cols, _ := parseHeader([]string{"email", "name", "country", "plan", "signed_up"})
	got, problems := validate(cols, []string{" ADA@Example.com ", " Ada ", "gb", " PRO ", "2026-03-14"})
	want := Customer{Email: "ada@example.com", Name: "Ada", Country: "GB", Plan: "pro", SignedUp: "2026-03-14"}
	if problems != nil || got != want {
		t.Errorf("got %+v, %q", got, problems)
	}
}

func TestBatches(t *testing.T) {
	sink := &fakeSink{}
	input := header + strings.Repeat(good, 5)

	s := runImport(t, newImporter(sink), input)

	sizes := []int{}
	for _, b := range sink.batches {
		sizes = append(sizes, len(b))
	}
	if !slices.Equal(sizes, []int{2, 2, 1}) {
		t.Errorf("batch sizes = %v, want [2 2 1]", sizes)
	}
	if s.Status != StatusCompleted || s.Rows != 5 || s.Imported != 5 {
		t.Errorf("summary: %+v", s)
	}
}

func TestColumnsCanBeReordered(t *testing.T) {
	sink := &fakeSink{}
	runImport(t, newImporter(sink), "signed_up,name,email,country\n2026-03-14,Ada,ada@example.com,GB\n")
	if got := sink.rows(); len(got) != 1 || got[0].Email != "ada@example.com" || got[0].Plan != "free" {
		t.Errorf("rows = %+v", got)
	}
}

func TestBadHeaderFailsTheImport(t *testing.T) {
	for name, input := range map[string]string{
		"empty file":      "",
		"missing columns": "a,b\n1,2\n",
		"broken quoting":  "\"email\"x,name\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := newImporter(&fakeSink{}).Run(t.Context(), strings.NewReader(input))
			if !errors.Is(err, ErrBadHeader) {
				t.Errorf("err = %v, want ErrBadHeader", err)
			}
		})
	}
}

func TestRejectsAreReportedAndImportContinues(t *testing.T) {
	sink := &fakeSink{}
	var rejects bytes.Buffer
	im := newImporter(sink)
	im.Rejects = &rejects

	input := header + good +
		"bad,Grace,GB,pro,2026-03-14\n" +
		"grace@example.com,Grace,GB,pro,2026-03-14\n"
	s := runImport(t, im, input)

	if s.Status != StatusWithRejects || s.Valid != 2 || s.Rejected != 1 || s.Imported != 2 {
		t.Errorf("summary: %+v", s)
	}
	if len(s.FirstRejects) != 1 || s.FirstRejects[0].Line != 3 {
		t.Errorf("FirstRejects = %+v, want line 3", s.FirstRejects)
	}

	got, err := csv.NewReader(&rejects).ReadAll()
	if err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"_line", "_errors", "email", "name", "country", "plan", "signed_up"},
		{"3", "email is not valid", "bad", "Grace", "GB", "pro", "2026-03-14"},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("rejects file:\n got %q\nwant %q", got, want)
	}
}

// A quoted field can contain a newline, so "row 3" and "line 4" differ. The
// report has to give the line someone would actually go to.
func TestRejectLineCountsMultilineFields(t *testing.T) {
	s := runImport(t, newImporter(&fakeSink{}), header+
		"ada@example.com,\"Ada\nLovelace\",GB,pro,2026-03-14\n"+
		"bad,Grace,GB,pro,2026-03-14\n")
	if len(s.FirstRejects) != 1 || s.FirstRejects[0].Line != 4 {
		t.Errorf("FirstRejects = %+v, want line 4", s.FirstRejects)
	}
}

func TestBrokenQuotingRejectsOnlyThatRow(t *testing.T) {
	sink := &fakeSink{}
	s := runImport(t, newImporter(sink), header+
		"ada\"x@example.com,Ada,GB,pro,2026-03-14\n"+
		"grace@example.com,Grace,GB,pro,2026-03-14\n")
	if s.Rejected != 1 || s.Imported != 1 || s.FirstRejects[0].Line != 2 {
		t.Errorf("summary: %+v", s)
	}
}

func TestOnlyFirstRejectsAreKeptInSummary(t *testing.T) {
	s := runImport(t, newImporter(&fakeSink{}), header+strings.Repeat("bad,,,,\n", 50))
	if s.Rejected != 50 || len(s.FirstRejects) != maxReportedRejects {
		t.Errorf("rejected=%d kept=%d", s.Rejected, len(s.FirstRejects))
	}
}

func TestTooManyRejectsStopsEarly(t *testing.T) {
	im := newImporter(&fakeSink{})
	im.MaxRejects = 2
	s := runImport(t, im, header+strings.Repeat("bad,,,,\n", 100))
	if s.Status != StatusTooMany || s.Rejected != 3 || s.Rows != 3 {
		t.Errorf("summary: %+v", s)
	}
}

func TestCancelStopsAndLeavesPartialBatch(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	sink := &fakeSink{}
	im := newImporter(sink)
	im.ProgressEvery = 3
	im.Progress = func(Summary) { cancel() }

	s, err := im.Run(ctx, strings.NewReader(header+strings.Repeat(good, 100)))
	if err != nil {
		t.Fatal(err)
	}
	// Cancelled after row 3: batch [1,2] was committed, row 3 was only pending.
	if s.Status != StatusCancelled || s.Rows != 3 || s.Imported != 2 || len(sink.rows()) != 2 {
		t.Errorf("summary: %+v, sink rows %d", s, len(sink.rows()))
	}
}

func TestSinkFailureFailsTheImport(t *testing.T) {
	boom := errors.New("disk full")
	_, err := newImporter(&fakeSink{err: boom}).Run(t.Context(), strings.NewReader(header+good))
	if !errors.Is(err, boom) {
		t.Errorf("err = %v, want it to wrap %v", err, boom)
	}
}

func TestDryRunValidatesWithoutASink(t *testing.T) {
	s := runImport(t, &Importer{BatchSize: 2}, header+strings.Repeat(good, 3)+"bad,,,,\n")
	if !s.DryRun || s.Valid != 3 || s.Rejected != 1 || s.Imported != 0 {
		t.Errorf("summary: %+v", s)
	}
}

// Rule: whatever the bytes, it never panics and every row is accounted for.
func FuzzImport(f *testing.F) {
	f.Add(header + good)
	f.Add("\ufeff" + header + "bad,,,,\n")
	f.Add(header + "\"unterminated")
	f.Add("email\n\n\n")
	f.Fuzz(func(t *testing.T, input string) {
		var rejects bytes.Buffer
		im := &Importer{Sink: &fakeSink{}, Rejects: &rejects, BatchSize: 2}
		s, err := im.Run(t.Context(), strings.NewReader(input))
		if err != nil {
			return
		}
		if s.Rows != s.Valid+s.Rejected {
			t.Fatalf("rows %d != valid %d + rejected %d", s.Rows, s.Valid, s.Rejected)
		}
		if s.Imported != s.Valid {
			t.Fatalf("imported %d != valid %d", s.Imported, s.Valid)
		}
	})
}

func TestExcelBOMIsRemoved(t *testing.T) {
	sink := &fakeSink{}
	s := runImport(t, newImporter(sink), "\ufeff"+header+good)
	if s.Imported != 1 {
		t.Errorf("summary: %+v", s)
	}
}

// The promise made by the rejects file: fix it, then upload it again. That
// breaks if the file's own header still has the BOM in it.
func TestRejectsFileCanBeUploadedAgain(t *testing.T) {
	var rejects bytes.Buffer
	im := newImporter(&fakeSink{})
	im.Rejects = &rejects
	runImport(t, im, "\ufeff"+header+"bad,Ada,GB,pro,2026-03-14\n")

	fixed := strings.Replace(rejects.String(), "bad,Ada", "ada@example.com,Ada", 1)
	sink := &fakeSink{}
	s := runImport(t, newImporter(sink), fixed)
	if s.Imported != 1 || s.Rejected != 0 || sink.rows()[0].Email != "ada@example.com" {
		t.Errorf("summary: %+v", s)
	}
}
