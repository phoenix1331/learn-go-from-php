package main

import (
	"database/sql"
	"errors"
	"path/filepath"
	"sync"
	"testing"
)

// Tests use a REAL SQLite database in a temp folder: no mocks, and each
// test starts empty. Like Laravel's RefreshDatabase with SQLite, but
// even simpler, because every test gets its own file.
func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := openDB(t.Context(), filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return &Store{db: db}
}

// seed adds one customer and the given products, returning their IDs.
func seed(t *testing.T, s *Store, products ...Product) (customerID int64) {
	t.Helper()
	id, err := s.CreateCustomer(t.Context(), "Ada", "ada@example.com", sql.Null[string]{})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.ImportProducts(t.Context(), products); err != nil {
		t.Fatal(err)
	}
	return id
}

func TestCreateAndFindCustomer(t *testing.T) {
	s := newTestStore(t)
	ctx := t.Context()

	phone := sql.Null[string]{V: "07700 900001", Valid: true}
	id, err := s.CreateCustomer(ctx, "Ada", "ada@example.com", phone)
	if err != nil {
		t.Fatal(err)
	}

	got, err := s.CustomerByEmail(ctx, "ada@example.com")
	if err != nil {
		t.Fatal(err)
	}
	want := Customer{ID: id, Name: "Ada", Email: "ada@example.com", Phone: phone}
	if got != want { // structs of comparable fields can be compared with ==
		t.Errorf("got %+v, want %+v", got, want)
	}
}

func TestNullPhone(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateCustomer(t.Context(), "Grace", "g@example.com", sql.Null[string]{}); err != nil {
		t.Fatal(err)
	}
	c, err := s.CustomerByEmail(t.Context(), "g@example.com")
	if err != nil {
		t.Fatal(err)
	}
	if c.Phone.Valid {
		t.Errorf("phone should be NULL, got %q", c.Phone.V)
	}
}

func TestDuplicateEmail(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)

	_, err := s.CreateCustomer(t.Context(), "Other", "ada@example.com", sql.Null[string]{})
	if !errors.Is(err, ErrDuplicate) {
		t.Fatalf("want ErrDuplicate, got %v", err)
	}
}

func TestCustomerNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.CustomerByEmail(t.Context(), "nobody@example.com")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("want ErrNotFound, got %v", err)
	}
}

func TestPlaceholdersStopInjection(t *testing.T) {
	s := newTestStore(t)
	seed(t, s)
	evil := `' OR '1'='1`

	// The unsafe version leaks every row...
	leaked, err := s.UnsafeFindByEmail(t.Context(), evil)
	if err != nil {
		t.Fatal(err)
	}
	if len(leaked) != 1 {
		t.Fatalf("expected the unsafe query to leak the 1 row, got %d", len(leaked))
	}

	// ...the safe version treats the input as plain text.
	if _, err := s.CustomerByEmail(t.Context(), evil); !errors.Is(err, ErrNotFound) {
		t.Errorf("want ErrNotFound, got %v", err)
	}

	// And nasty input is STORED as plain text too.
	nasty := `Robert'); DROP TABLE customers; --`
	if _, err := s.CreateCustomer(t.Context(), nasty, "bobby@example.com", sql.Null[string]{}); err != nil {
		t.Fatal(err)
	}
	c, err := s.CustomerByEmail(t.Context(), "bobby@example.com")
	if err != nil || c.Name != nasty {
		t.Errorf("got %+v, %v", c, err)
	}
}

func TestPlaceOrder(t *testing.T) {
	s := newTestStore(t)
	cust := seed(t, s, Product{SKU: "MUG", PricePence: 1200, Stock: 5})

	o, err := s.PlaceOrder(t.Context(), cust, 1, 2)
	if err != nil {
		t.Fatal(err)
	}
	if o.ID == 0 || o.TotalPence != 2400 {
		t.Errorf("got %+v", o)
	}
	if stock, _ := s.Stock(t.Context(), 1); stock != 3 {
		t.Errorf("stock = %d, want 3", stock)
	}
}

func TestPlaceOrderErrors(t *testing.T) {
	tests := []struct {
		name       string
		customerID int64
		productID  int64
		qty        int
		wantErr    error // nil means "any error"
	}{
		{"out of stock", 1, 1, 6, ErrOutOfStock},
		{"unknown product", 1, 42, 1, ErrNotFound},
		{"unknown customer", 999, 1, 1, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newTestStore(t)
			seed(t, s, Product{SKU: "MUG", PricePence: 1200, Stock: 5})

			_, err := s.PlaceOrder(t.Context(), tt.customerID, tt.productID, tt.qty)
			if err == nil || (tt.wantErr != nil && !errors.Is(err, tt.wantErr)) {
				t.Fatalf("want %v, got %v", tt.wantErr, err)
			}
			// Whatever failed, the transaction must leave stock untouched.
			if stock, _ := s.Stock(t.Context(), 1); stock != 5 {
				t.Errorf("stock = %d after failed order, want 5 (not rolled back?)", stock)
			}
		})
	}
}

// Ten buyers race for three T-shirts. The single UPDATE ... WHERE stock >= ?
// means exactly three succeed, never more, however the goroutines interleave.
func TestPlaceOrderConcurrent(t *testing.T) {
	s := newTestStore(t)
	cust := seed(t, s, Product{SKU: "TEE", PricePence: 2000, Stock: 3})

	var (
		wg               sync.WaitGroup
		mu               sync.Mutex
		sold, outOfStock int
	)
	for range 10 {
		wg.Go(func() {
			_, err := s.PlaceOrder(t.Context(), cust, 1, 1)
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				sold++
			case errors.Is(err, ErrOutOfStock):
				outOfStock++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		})
	}
	wg.Wait()

	if sold != 3 || outOfStock != 7 {
		t.Errorf("sold %d, out of stock %d; want 3 and 7", sold, outOfStock)
	}
	if stock, _ := s.Stock(t.Context(), 1); stock != 0 {
		t.Errorf("stock = %d, want 0", stock)
	}
}

func TestSalesBySKU(t *testing.T) {
	s := newTestStore(t)
	cust := seed(t, s,
		Product{SKU: "MUG", PricePence: 1200, Stock: 10},
		Product{SKU: "TEE", PricePence: 2000, Stock: 10},
		Product{SKU: "CAP", PricePence: 900, Stock: 10}, // never sold
	)
	for _, buy := range []struct {
		product int64
		qty     int
	}{{1, 1}, {1, 2}, {2, 1}} {
		if _, err := s.PlaceOrder(t.Context(), cust, buy.product, buy.qty); err != nil {
			t.Fatal(err)
		}
	}

	got, err := s.SalesBySKU(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	want := []SkuSales{
		{SKU: "MUG", Orders: 2, Units: 3, TotalPence: 3600},
		{SKU: "TEE", Orders: 1, Units: 1, TotalPence: 2000},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("row %d: got %+v, want %+v", i, got[i], want[i])
		}
	}
}
