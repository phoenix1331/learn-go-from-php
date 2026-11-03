// LESSON 17 — DATABASES (part 2: the Store)
//
// Store wraps *sql.DB with one method per query, a bit like a Laravel
// repository class. Everything SQL lives here, so the rest of the program
// works with plain Go structs and never builds SQL strings.
//
// No ORM: Go code usually writes SQL by hand with database/sql. It's more
// typing than Eloquent but nothing is hidden. You see every query, and
// N+1 problems can't sneak in. (See "GOING FURTHER" in main.go for tools
// that generate this code for you.)
package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	// The SQLite driver. Importing it runs its init() function, which
	// registers the driver under the name "sqlite" for sql.Open to use.
	// If you only need that registration, import it as _ "modernc.org/sqlite".
	// We also use its Error type below, so it's a normal import here.
	"modernc.org/sqlite"
	sqlite3 "modernc.org/sqlite/lib" // error code constants
)

// Sentinel errors (lesson 07). Callers check these with errors.Is, never
// the driver's own errors, so swapping SQLite for Postgres only changes
// this file.
var (
	ErrNotFound   = errors.New("not found")
	ErrDuplicate  = errors.New("already exists")
	ErrOutOfStock = errors.New("not enough stock")
)

type Customer struct {
	ID    int64
	Name  string
	Email string
	// Phone can be NULL. A plain string can't hold NULL (its zero value is
	// ""), so Scan would fail. sql.Null[T] (Go 1.22+) adds a Valid flag:
	// Valid == false means NULL. Like a ?string in PHP.
	Phone sql.Null[string]
}

type Product struct {
	ID         int64
	SKU        string
	PricePence int64
	Stock      int
}

type Order struct {
	ID         int64
	CustomerID int64
	ProductID  int64
	Quantity   int
	TotalPence int64
}

const schema = `
CREATE TABLE IF NOT EXISTS customers (
	id    INTEGER PRIMARY KEY,
	name  TEXT NOT NULL,
	email TEXT NOT NULL UNIQUE,
	phone TEXT
);
CREATE TABLE IF NOT EXISTS products (
	id          INTEGER PRIMARY KEY,
	sku         TEXT NOT NULL UNIQUE,
	price_pence INTEGER NOT NULL,
	stock       INTEGER NOT NULL CHECK (stock >= 0)
);
CREATE TABLE IF NOT EXISTS orders (
	id          INTEGER PRIMARY KEY,
	customer_id INTEGER NOT NULL REFERENCES customers(id),
	product_id  INTEGER NOT NULL REFERENCES products(id),
	quantity    INTEGER NOT NULL,
	total_pence INTEGER NOT NULL
);`

// openDB opens (or creates) a SQLite file and makes sure the schema exists.
func openDB(ctx context.Context, path string) (*sql.DB, error) {
	// _pragma options run on EVERY new connection in the pool:
	//   foreign_keys(1)    SQLite ignores REFERENCES unless you switch this on
	//   busy_timeout(5000) wait up to 5s for a lock instead of failing at once
	// _txlock=immediate makes transactions take SQLite's write lock at BEGIN.
	// Without it, two transactions can both read, then deadlock when both
	// try to write ("database is locked"). MySQL/Postgres don't need this.
	dsn := "file:" + path + "?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)&_txlock=immediate"

	// *sql.DB is NOT one connection. It's a POOL that opens connections as
	// needed and is safe to share between goroutines. Open it once at
	// startup and pass it around, like the http.Client in lesson 16.
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	// sql.Open doesn't connect; it only checks the arguments. Ping makes
	// a real connection, so a bad path fails here, not on the first query.
	if err := db.PingContext(ctx); err != nil {
		db.Close()
		return nil, err
	}
	// ExecContext is for statements that return no rows. A real app would
	// use migrations (see GOING FURTHER), but this is enough for a lesson.
	if _, err := db.ExecContext(ctx, schema); err != nil {
		db.Close()
		return nil, fmt.Errorf("creating schema: %w", err)
	}
	return db, nil
}

type Store struct {
	db *sql.DB
}

// --- Writing ----------------------------------------------------------------

func (s *Store) CreateCustomer(ctx context.Context, name, email string, phone sql.Null[string]) (int64, error) {
	// PLACEHOLDERS (?) keep data separate from SQL. The driver sends the
	// values on their own, so a name like  O'Brien  or  '; DROP TABLE x; --
	// is stored as text and can't change the query. Same as PDO's
	// prepare() + execute([$name, $email]). Postgres uses $1, $2 instead of ?.
	res, err := s.db.ExecContext(ctx,
		`INSERT INTO customers (name, email, phone) VALUES (?, ?, ?)`,
		name, email, phone)
	if isUniqueViolation(err) {
		return 0, fmt.Errorf("customer %q: %w", email, ErrDuplicate)
	}
	if err != nil {
		return 0, err
	}
	// Postgres doesn't support LastInsertId; there you'd add
	// `RETURNING id` and use QueryRowContext(...).Scan(&id).
	return res.LastInsertId()
}

// PlaceOrder takes stock and creates an order in ONE TRANSACTION: either
// both happen or neither does. Laravel's DB::transaction(fn () => ...).
func (s *Store) PlaceOrder(ctx context.Context, customerID, productID int64, qty int) (Order, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Order{}, err
	}
	// THE TRANSACTION IDIOM: defer Rollback straight away. If we return
	// early with an error, everything is undone. If Commit succeeds first,
	// Rollback does nothing (it returns sql.ErrTxDone, which we ignore).
	defer tx.Rollback()

	// Inside a transaction, use tx, not s.db. Queries on s.db would run on
	// a different connection, outside the transaction.
	var price int64
	err = tx.QueryRowContext(ctx, `SELECT price_pence FROM products WHERE id = ?`, productID).Scan(&price)
	if errors.Is(err, sql.ErrNoRows) {
		return Order{}, fmt.Errorf("product %d: %w", productID, ErrNotFound)
	}
	if err != nil {
		return Order{}, err
	}

	// Check and update stock in ONE statement. Reading the stock, checking
	// it in Go, then writing would let two buyers both see "1 left" and
	// both buy it. The WHERE clause makes the database do the check.
	res, err := tx.ExecContext(ctx,
		`UPDATE products SET stock = stock - ? WHERE id = ? AND stock >= ?`,
		qty, productID, qty)
	if err != nil {
		return Order{}, err
	}
	if n, err := res.RowsAffected(); err != nil {
		return Order{}, err
	} else if n == 0 {
		return Order{}, fmt.Errorf("product %d, qty %d: %w", productID, qty, ErrOutOfStock)
	}

	o := Order{CustomerID: customerID, ProductID: productID, Quantity: qty, TotalPence: price * int64(qty)}
	res, err = tx.ExecContext(ctx,
		`INSERT INTO orders (customer_id, product_id, quantity, total_pence) VALUES (?, ?, ?, ?)`,
		o.CustomerID, o.ProductID, o.Quantity, o.TotalPence)
	if err != nil {
		// e.g. a foreign key failure for an unknown customer. The stock
		// UPDATE above is undone by the deferred Rollback.
		return Order{}, fmt.Errorf("inserting order: %w", err)
	}
	if o.ID, err = res.LastInsertId(); err != nil {
		return Order{}, err
	}

	if err := tx.Commit(); err != nil {
		return Order{}, err
	}
	return o, nil
}

// preparer is satisfied by BOTH *sql.DB and *sql.Tx (interfaces are
// implicit, lesson 06), so insertProducts works with or without a transaction.
type preparer interface {
	PrepareContext(ctx context.Context, query string) (*sql.Stmt, error)
}

// insertProducts uses a PREPARED STATEMENT: the database parses the SQL
// once, then each Exec only sends new values. Worth it for bulk inserts.
func insertProducts(ctx context.Context, p preparer, products []Product) error {
	stmt, err := p.PrepareContext(ctx, `INSERT INTO products (sku, price_pence, stock) VALUES (?, ?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()

	for _, pr := range products {
		if _, err := stmt.ExecContext(ctx, pr.SKU, pr.PricePence, pr.Stock); err != nil {
			return fmt.Errorf("inserting %s: %w", pr.SKU, err)
		}
	}
	return nil
}

// ImportProducts inserts all products in one transaction: all or nothing,
// and much faster, because the database commits to disk once, not per row.
func (s *Store) ImportProducts(ctx context.Context, products []Product) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if err := insertProducts(ctx, tx, products); err != nil {
		return err
	}
	return tx.Commit()
}

// --- Reading ----------------------------------------------------------------

// CustomerByEmail returns one row. QueryRowContext + Scan is the Go
// version of ->first(). Scan copies each column into the matching pointer,
// in order, and converts types (INTEGER -> int64, TEXT -> string).
func (s *Store) CustomerByEmail(ctx context.Context, email string) (Customer, error) {
	var c Customer
	err := s.db.QueryRowContext(ctx,
		`SELECT id, name, email, phone FROM customers WHERE email = ?`, email,
	).Scan(&c.ID, &c.Name, &c.Email, &c.Phone)

	// No row is reported as an error at Scan time, like firstOrFail().
	// Translate it to our own sentinel.
	if errors.Is(err, sql.ErrNoRows) {
		return Customer{}, fmt.Errorf("customer %q: %w", email, ErrNotFound)
	}
	return c, err
}

func (s *Store) ListCustomers(ctx context.Context) ([]Customer, error) {
	return s.queryCustomers(ctx, `SELECT id, name, email, phone FROM customers ORDER BY id`)
}

// UnsafeFindByEmail is here ONLY to show SQL injection. Never do this.
// fmt.Sprintf pastes the input INTO the SQL, so the input can rewrite it.
func (s *Store) UnsafeFindByEmail(ctx context.Context, email string) ([]Customer, error) {
	query := fmt.Sprintf(`SELECT id, name, email, phone FROM customers WHERE email = '%s'`, email)
	return s.queryCustomers(ctx, query)
}

// queryCustomers runs a query returning many rows. THE ROWS LOOP:
//
//  1. QueryContext                  run the query
//  2. defer rows.Close()            return the connection to the pool
//  3. for rows.Next() { Scan }      one row at a time (streaming, lesson 15)
//  4. rows.Err()                    did the loop stop because of an error?
//
// Forgetting Close leaks a connection; forgetting Err hides failures.
func (s *Store) queryCustomers(ctx context.Context, query string, args ...any) ([]Customer, error) {
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []Customer
	for rows.Next() {
		var c Customer
		if err := rows.Scan(&c.ID, &c.Name, &c.Email, &c.Phone); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func (s *Store) Stock(ctx context.Context, productID int64) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `SELECT stock FROM products WHERE id = ?`, productID).Scan(&n)
	return n, err
}

type SkuSales struct {
	SKU        string
	Orders     int
	Units      int
	TotalPence int64
}

// SalesBySKU shows that any SQL works: JOINs, GROUP BY, aggregates.
// You just Scan the columns you selected.
func (s *Store) SalesBySKU(ctx context.Context) ([]SkuSales, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT p.sku, COUNT(o.id), COALESCE(SUM(o.quantity), 0), COALESCE(SUM(o.total_pence), 0)
		FROM products p
		LEFT JOIN orders o ON o.product_id = p.id
		GROUP BY p.id
		HAVING COUNT(o.id) > 0
		ORDER BY 4 DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []SkuSales
	for rows.Next() {
		var r SkuSales
		if err := rows.Scan(&r.SKU, &r.Orders, &r.Units, &r.TotalPence); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// isUniqueViolation is the one place that knows about SQLite's error codes.
// With Postgres (pgx) you'd check for *pgconn.PgError with Code "23505".
func isUniqueViolation(err error) bool {
	var se *sqlite.Error
	return errors.As(err, &se) && se.Code() == sqlite3.SQLITE_CONSTRAINT_UNIQUE
}
