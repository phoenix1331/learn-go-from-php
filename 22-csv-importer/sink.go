package main

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	_ "modernc.org/sqlite"
)

// laravelTime is the format Laravel writes to created_at and updated_at.
const laravelTime = "2006-01-02 15:04:05"

// SQLiteSink writes to the same table that Laravel's migration creates.
type SQLiteSink struct {
	db  *sql.DB
	Now func() time.Time
}

func OpenSQLite(ctx context.Context, path string) (*SQLiteSink, error) {
	// The same options as lesson 17. busy_timeout matters more here, because
	// the Laravel app may be using this database during the import.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	return &SQLiteSink{db: db, Now: time.Now}, nil
}

func (s *SQLiteSink) Close() error { return s.db.Close() }

// Init creates the table for trying this without Laravel. In a real app
// the migration owns the schema, and you don't pass -init.
func (s *SQLiteSink) Init(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `CREATE TABLE IF NOT EXISTS customers (
		id INTEGER PRIMARY KEY AUTOINCREMENT,
		email VARCHAR NOT NULL UNIQUE,
		name VARCHAR NOT NULL,
		country VARCHAR NOT NULL,
		plan VARCHAR NOT NULL,
		signed_up DATE NOT NULL,
		created_at DATETIME,
		updated_at DATETIME
	)`)
	return err
}

func (s *SQLiteSink) Count(ctx context.Context) (int64, error) {
	var n int64
	err := s.db.QueryRowContext(ctx, `SELECT COUNT(*) FROM customers`).Scan(&n)
	return n, err
}

// Upsert writes one batch in one transaction.
//
// Why batches? SQLite syncs to disk on every commit, so one transaction per
// row is hundreds of times slower. One transaction for the whole file would
// lock out your Laravel app for the entire import. Batches sit in between.
//
// Why an upsert? It's what makes a re-run safe. If an import is killed part
// way, run the same file again: finished rows get updated, not duplicated.
// Same idea as Model::upsert().
func (s *SQLiteSink) Upsert(ctx context.Context, batch []Customer) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback() // does nothing once Commit has succeeded

	stmt, err := tx.PrepareContext(ctx, `
		INSERT INTO customers (email, name, country, plan, signed_up, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(email) DO UPDATE SET
			name = excluded.name,
			country = excluded.country,
			plan = excluded.plan,
			signed_up = excluded.signed_up,
			updated_at = excluded.updated_at`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	now := s.Now().UTC().Format(laravelTime)
	for _, c := range batch {
		if _, err := stmt.ExecContext(ctx, c.Email, c.Name, c.Country, c.Plan, c.SignedUp, now, now); err != nil {
			return fmt.Errorf("upserting %s: %w", c.Email, err)
		}
	}
	return tx.Commit()
}
