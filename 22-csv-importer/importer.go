package main

import (
	"context"
	"encoding/csv"
	"errors"
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
)

// Every outcome Laravel might need to tell apart.
const (
	StatusCompleted   = "completed"
	StatusWithRejects = "completed_with_rejects"
	StatusTooMany     = "too_many_rejects"
	StatusCancelled   = "cancelled"
	StatusFailed      = "failed"
)

const maxReportedRejects = 10

// Reject explains why a row was refused. Line is the line in the file, so it
// matches what someone sees in a text editor.
type Reject struct {
	Line   int      `json:"line"`
	Errors []string `json:"errors"`
}

// Summary is the one JSON document printed on stdout for Laravel.
type Summary struct {
	Status       string   `json:"status"`
	Error        string   `json:"error,omitempty"`
	DryRun       bool     `json:"dry_run"`
	Rows         int64    `json:"rows"`     // data rows read
	Valid        int64    `json:"valid"`    // rows that passed validation
	Rejected     int64    `json:"rejected"` // rows that didn't
	Imported     int64    `json:"imported"` // rows committed to the database
	Inserted     int64    `json:"inserted"`
	Updated      int64    `json:"updated"`
	RejectsFile  string   `json:"rejects_file,omitempty"`
	FirstRejects []Reject `json:"first_rejects"`
	DurationMS   int64    `json:"duration_ms"`
}

// Sink is wherever valid rows end up. SQLite here (sink.go), but MySQL or
// Postgres need only another implementation. Tests use a fake.
type Sink interface {
	Upsert(ctx context.Context, batch []Customer) error
}

// Importer reads a CSV from start to finish, keeping only one batch in
// memory, so a 10 GB file needs no more memory than a 10 KB one.
//
// There are no goroutines, and that's deliberate. The database write is the
// slow part and SQLite allows one writer, so parallel validation would add
// complexity and gain almost nothing. Use concurrency when a measurement
// says so (lesson 24), not because it's Go.
type Importer struct {
	Sink          Sink // nil means dry run: validate only
	Rejects       io.Writer
	BatchSize     int
	MaxRejects    int64 // 0 means no limit
	ProgressEvery int64 // 0 means no progress reports
	Progress      func(Summary)
}

// Run imports src. It returns an error only if the whole import failed.
// Stopping early (too many rejects, cancellation) is a Status, not an error.
func (im *Importer) Run(ctx context.Context, src io.Reader) (Summary, error) {
	s := Summary{DryRun: im.Sink == nil, FirstRejects: []Reject{}}

	cr := csv.NewReader(src)
	cr.ReuseRecord = true   // don't allocate a new slice per row (lesson 15)
	cr.FieldsPerRecord = -1 // we check the count, to report it nicely

	first, err := cr.Read()
	if errors.Is(err, io.EOF) {
		return s, fmt.Errorf("%w: the file is empty", ErrBadHeader)
	}
	if err != nil {
		return s, fmt.Errorf("%w: %w", ErrBadHeader, err)
	}
	header := slices.Clone(first) // ReuseRecord would overwrite it
	// Excel's "CSV UTF-8" starts the file with invisible bytes called a BOM.
	// Left in, the first column's name looks right in every log message but
	// never matches. Strip it here, before anything else sees the header.
	header[0] = strings.TrimPrefix(header[0], "\ufeff")
	cols, err := parseHeader(header)
	if err != nil {
		return s, err
	}

	var rejects *csv.Writer
	if im.Rejects != nil {
		rejects = csv.NewWriter(im.Rejects)
		// The original columns are kept, so someone can fix this file and
		// upload it again. The importer ignores the two extra columns.
		if err := rejects.Write(append([]string{"_line", "_errors"}, header...)); err != nil {
			return s, fmt.Errorf("writing rejects: %w", err)
		}
	}

	reject := func(line int, record []string, problems []string) error {
		s.Rejected++
		if len(s.FirstRejects) < maxReportedRejects {
			s.FirstRejects = append(s.FirstRejects, Reject{Line: line, Errors: slices.Clone(problems)})
		}
		if rejects == nil {
			return nil
		}
		row := append([]string{strconv.Itoa(line), strings.Join(problems, "; ")}, record...)
		if err := rejects.Write(row); err != nil {
			return fmt.Errorf("writing rejects: %w", err)
		}
		return nil
	}

	batch := make([]Customer, 0, im.BatchSize)
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		if im.Sink != nil {
			// Once a batch has started, let it finish. The cancellation is
			// noticed at the top of the loop, between rows.
			if err := im.Sink.Upsert(context.WithoutCancel(ctx), batch); err != nil {
				return fmt.Errorf("writing batch: %w", err)
			}
			s.Imported += int64(len(batch))
		}
		batch = batch[:0]
		return nil
	}

	s.Status = StatusCompleted
loop:
	for {
		if ctx.Err() != nil {
			s.Status = StatusCancelled
			s.Error = ctx.Err().Error()
			break
		}

		record, err := cr.Read()
		if errors.Is(err, io.EOF) {
			break
		}

		var rejectErr error
		var parseErr *csv.ParseError
		switch {
		case errors.As(err, &parseErr):
			// Broken quoting in this row. The reader can carry on after it.
			s.Rows++
			rejectErr = reject(parseErr.StartLine, nil, []string{parseErr.Err.Error()})
		case err != nil:
			return s, fmt.Errorf("reading: %w", err)
		default:
			s.Rows++
			line, _ := cr.FieldPos(0)
			customer, problems := validate(cols, record)
			if problems != nil {
				rejectErr = reject(line, record, problems)
				break
			}
			s.Valid++
			batch = append(batch, customer)
			if len(batch) == im.BatchSize {
				if err := flush(); err != nil {
					return s, err
				}
			}
		}
		if rejectErr != nil {
			return s, rejectErr
		}

		if im.MaxRejects > 0 && s.Rejected > im.MaxRejects {
			// Probably the wrong file. Stop rather than churn through it.
			s.Status = StatusTooMany
			s.Error = fmt.Sprintf("more than %d rows rejected", im.MaxRejects)
			break loop
		}
		if im.ProgressEvery > 0 && s.Rows%im.ProgressEvery == 0 && im.Progress != nil {
			im.Progress(s)
		}
	}

	// A stopped import leaves its partial batch out: nobody asked for it.
	if s.Status == StatusCompleted {
		if err := flush(); err != nil {
			return s, err
		}
		if s.Rejected > 0 {
			s.Status = StatusWithRejects
		}
	}

	if rejects != nil {
		rejects.Flush() // csv.Writer buffers: forgetting this loses rows
		if err := rejects.Error(); err != nil {
			return s, fmt.Errorf("writing rejects: %w", err)
		}
	}
	return s, nil
}
