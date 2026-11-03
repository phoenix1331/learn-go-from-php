// LESSON 17 — DATABASES (part 1): database/sql with SQLite
//
// Run with:  go run ./17-databases
// Test with: go test -v ./17-databases
//
// Start with store.go, which holds all the SQL. This file only uses it.
//
// database/sql is Go's PDO: one standard API, with a DRIVER per database.
//
//	PHP                                   Go
//	new PDO('sqlite:app.db')              sql.Open("sqlite", "file:app.db")
//	$pdo->prepare($sql)->execute($args)   db.ExecContext(ctx, sql, args...)
//	$stmt->fetch()                        db.QueryRowContext(...).Scan(&a, &b)
//	foreach ($stmt as $row)               for rows.Next() { rows.Scan(...) }
//	DB::transaction(fn () => ...)         tx := db.BeginTx(); ...; tx.Commit()
//
// We use modernc.org/sqlite, a SQLite driver written in pure Go. The
// popular mattn/go-sqlite3 needs a C compiler (cgo), which makes
// cross-compiling (lesson 23) harder. For MySQL use go-sql-driver/mysql;
// for Postgres use jackc/pgx. The database/sql code barely changes.
//
// The database is a temporary file, deleted when the program ends.
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	// Every query takes a ctx (lesson 14). In a web handler you'd pass
	// r.Context(), so a query is cancelled if the user goes away.
	ctx := context.Background()

	dir, err := os.MkdirTemp("", "lesson17-*")
	if err != nil {
		return err
	}
	defer os.RemoveAll(dir)

	db, err := openDB(ctx, filepath.Join(dir, "shop.db"))
	if err != nil {
		return err
	}
	defer db.Close()
	store := &Store{db: db}

	// ---------------------------------------------------------------
	// 1. INSERTING ROWS
	// ---------------------------------------------------------------
	fmt.Println("1. Inserting customers")
	customers := []struct {
		name, email string
		phone       sql.Null[string]
	}{
		{"Ada", "ada@example.com", sql.Null[string]{V: "07700 900001", Valid: true}},
		{"Grace", "grace@example.com", sql.Null[string]{}}, // Valid: false -> NULL
		{"Seán O'Brien", "sean@example.com", sql.Null[string]{}},
	}
	for _, c := range customers {
		id, err := store.CreateCustomer(ctx, c.name, c.email, c.phone)
		if err != nil {
			return err
		}
		fmt.Printf("    #%d %s\n", id, c.name) // the apostrophe in O'Brien is fine
	}

	_, err = store.CreateCustomer(ctx, "Ada again", "ada@example.com", sql.Null[string]{})
	fmt.Println("    duplicate email:", err, "| is ErrDuplicate:", errors.Is(err, ErrDuplicate))

	// ---------------------------------------------------------------
	// 2. READING ROWS
	// ---------------------------------------------------------------
	fmt.Println("\n2. Reading")
	all, err := store.ListCustomers(ctx)
	if err != nil {
		return err
	}
	for _, c := range all {
		phone := "(no phone)"
		if c.Phone.Valid { // NULL check, like `$c->phone ?? '(no phone)'`
			phone = c.Phone.V
		}
		fmt.Printf("    #%d %-13s %-18s %s\n", c.ID, c.Name, c.Email, phone)
	}

	_, err = store.CustomerByEmail(ctx, "nobody@example.com")
	fmt.Println("    missing:", err, "| is ErrNotFound:", errors.Is(err, ErrNotFound))

	// ---------------------------------------------------------------
	// 3. SQL INJECTION
	// ---------------------------------------------------------------
	fmt.Println("\n3. SQL injection")
	evil := `' OR '1'='1`
	fmt.Printf("    input: %s\n", evil)

	leaked, err := store.UnsafeFindByEmail(ctx, evil)
	if err != nil {
		return err
	}
	// The SQL became: ... WHERE email = '' OR '1'='1'  -> every row matches.
	fmt.Printf("    Sprintf query returned %d customers (leaked the table!)\n", len(leaked))

	_, err = store.CustomerByEmail(ctx, evil)
	fmt.Println("    placeholder query:", err)

	// ---------------------------------------------------------------
	// 4. TRANSACTIONS
	// ---------------------------------------------------------------
	fmt.Println("\n4. Transactions")
	if err := store.ImportProducts(ctx, []Product{
		{SKU: "GO-MUG", PricePence: 1200, Stock: 5},
		{SKU: "GO-TEE", PricePence: 2000, Stock: 1},
	}); err != nil {
		return err
	}
	const mug, tee = 1, 2 // product IDs

	o, err := store.PlaceOrder(ctx, 1, mug, 2)
	fmt.Printf("    Ada buys 2 mugs:     %+v err=%v\n", o, err)

	o, err = store.PlaceOrder(ctx, 2, tee, 1)
	fmt.Printf("    Grace buys 1 tee:    %+v err=%v\n", o, err)

	_, err = store.PlaceOrder(ctx, 3, tee, 1)
	fmt.Println("    Seán buys 1 tee:    ", err)

	// Customer 999 doesn't exist, so the INSERT fails AFTER stock was
	// taken. The rollback should put the stock back.
	before, _ := store.Stock(ctx, mug)
	_, err = store.PlaceOrder(ctx, 999, mug, 1)
	after, _ := store.Stock(ctx, mug)
	fmt.Println("    unknown customer:   ", err)
	fmt.Printf("    mug stock before %d, after %d (rolled back)\n", before, after)

	// ---------------------------------------------------------------
	// 5. BULK INSERTS: ONE TRANSACTION vs ONE PER ROW
	// ---------------------------------------------------------------
	fmt.Println("\n5. Inserting 1,000 rows")
	makeProducts := func(prefix string) []Product {
		ps := make([]Product, 1000)
		for i := range ps {
			ps[i] = Product{SKU: fmt.Sprintf("%s-%04d", prefix, i), PricePence: 100, Stock: 10}
		}
		return ps
	}

	start := time.Now()
	// Passing db (not a tx): every INSERT is its own transaction, and
	// SQLite waits for the disk after each one.
	if err := insertProducts(ctx, db, makeProducts("AUTO")); err != nil {
		return err
	}
	fmt.Printf("    one transaction per row: %v\n", since(start))

	start = time.Now()
	if err := store.ImportProducts(ctx, makeProducts("BULK")); err != nil {
		return err
	}
	fmt.Printf("    one transaction for all: %v\n", since(start))

	// ---------------------------------------------------------------
	// 6. REPORTING QUERIES
	// ---------------------------------------------------------------
	fmt.Println("\n6. Sales by SKU (JOIN + GROUP BY)")
	sales, err := store.SalesBySKU(ctx)
	if err != nil {
		return err
	}
	for _, s := range sales {
		fmt.Printf("    %-7s %d orders, %d units, £%d.%02d\n", s.SKU, s.Orders, s.Units, s.TotalPence/100, s.TotalPence%100)
	}

	// db.Stats() shows the connection pool at work.
	st := db.Stats()
	fmt.Printf("\n    pool: %d open connections, %d in use, %d idle\n", st.OpenConnections, st.InUse, st.Idle)
	return nil
}

func since(t time.Time) time.Duration {
	return time.Since(t).Round(time.Millisecond)
}

// GOING FURTHER
//   - MIGRATIONS: pressly/goose or golang-migrate run versioned .sql files,
//     like `php artisan migrate`.
//   - LESS BOILERPLATE: sqlc reads your .sql queries and GENERATES typed Go
//     functions like the ones in store.go. sqlx adds scanning into structs
//     by column name.
//   - POOL SETTINGS: db.SetMaxOpenConns, SetMaxIdleConns and
//     SetConnMaxLifetime. Set MaxOpenConns below your MySQL/Postgres
//     max_connections, especially with many app servers.
//   - ":memory:" GOTCHA: each connection in the pool gets its OWN empty
//     in-memory database. Use a temp file (as here), or SetMaxOpenConns(1).
//
// THINGS TO TRY
//   - In PlaceOrder, delete `defer tx.Rollback()` and run the program.
//     Seán's out-of-stock order returns early WITHOUT ending its
//     transaction, which keeps holding SQLite's write lock. The next write
//     waits 5 seconds (busy_timeout), then fails with "database is locked".
//     In MySQL/Postgres a leaked transaction similarly holds row locks
//     and a pool connection until something kills it.
//   - Remove `foreign_keys(1)` from the DSN in openDB. The order for
//     customer 999 now succeeds, because SQLite ignores REFERENCES by default.
//   - Change the UPDATE in PlaceOrder to drop `AND stock >= ?` and buy 10
//     mugs. The CHECK (stock >= 0) constraint still stops it, with a less
//     friendly error.
