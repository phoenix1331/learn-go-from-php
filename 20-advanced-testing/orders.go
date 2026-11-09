package shop

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"strings"
	"time"
)

var (
	ErrInvalid     = errors.New("invalid order")
	ErrNotFound    = errors.New("order not found")
	ErrDeclined    = errors.New("card declined")
	ErrUnavailable = errors.New("payment provider unavailable")
)

type Order struct {
	ID          int64     `json:"id"`
	Email       string    `json:"email"`
	Pence       int64     `json:"pence"`
	Total       string    `json:"total"`
	ChargeID    string    `json:"charge_id"`
	ReceiptSent bool      `json:"receipt_sent"`
	CreatedAt   time.Time `json:"created_at"`
}

// THE SEAMS. Everything slow or external sits behind a small interface,
// defined here by the code that USES it. Production passes real ones, tests
// pass fakes. No container or framework is involved: it's just a parameter.

type Store interface {
	Save(ctx context.Context, o Order) (Order, error)
	Find(ctx context.Context, id int64) (Order, error)
}

type Payments interface {
	// Charge takes an idempotency key, so a retry can't charge twice.
	Charge(ctx context.Context, key string, pence int64) (chargeID string, err error)
	Refund(ctx context.Context, chargeID string) error
}

type Mailer interface {
	SendReceipt(ctx context.Context, o Order) error
}

type Service struct {
	Store    Store
	Payments Payments
	Mailer   Mailer
	// Now is a field so tests can freeze the clock, like Carbon::setTestNow.
	Now func() time.Time
}

func NewService(store Store, payments Payments, mailer Mailer) *Service {
	return &Service{Store: store, Payments: payments, Mailer: mailer, Now: time.Now}
}

// Place takes payment, saves the order, then emails a receipt. Each failure
// has a different right answer, and each one has a test.
func (s *Service) Place(ctx context.Context, email, amount string) (Order, error) {
	if !strings.Contains(email, "@") {
		return Order{}, fmt.Errorf("%w: email %q", ErrInvalid, email)
	}
	pence, err := ParseMoney(amount)
	if err != nil || pence <= 0 {
		return Order{}, fmt.Errorf("%w: amount %q", ErrInvalid, amount)
	}

	chargeID, err := s.charge(ctx, rand.Text(), pence)
	if err != nil {
		return Order{}, fmt.Errorf("charge: %w", err)
	}

	order, err := s.Store.Save(ctx, Order{
		Email:     email,
		Pence:     pence,
		Total:     FormatMoney(pence),
		ChargeID:  chargeID,
		CreatedAt: s.Now(),
	})
	if err != nil {
		// We took money but have no order: give it back. WithoutCancel,
		// because this must still happen if the client has hung up.
		refundErr := s.Payments.Refund(context.WithoutCancel(ctx), chargeID)
		return Order{}, errors.Join(fmt.Errorf("save: %w", err), refundErr)
	}

	// The order is paid and saved, so a failed email doesn't undo it.
	order.ReceiptSent = s.Mailer.SendReceipt(ctx, order) == nil
	return order, nil
}

// Find looks up a saved order.
func (s *Service) Find(ctx context.Context, id int64) (Order, error) {
	return s.Store.Find(ctx, id)
}

var retryDelays = []time.Duration{1 * time.Second, 2 * time.Second}

// charge retries outages, but never a decline. The sleeps would make a test
// take 3 seconds, except that synctest makes them instant (service_test.go).
func (s *Service) charge(ctx context.Context, key string, pence int64) (string, error) {
	for attempt := 0; ; attempt++ {
		id, err := s.Payments.Charge(ctx, key, pence)
		if err == nil || !errors.Is(err, ErrUnavailable) || attempt == len(retryDelays) {
			return id, err
		}
		select {
		case <-time.After(retryDelays[attempt]):
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
}
