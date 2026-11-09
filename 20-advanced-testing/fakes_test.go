package shop

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// Files ending in _test.go aren't compiled into your program, so fakes
// here can never ship to production.
//
// The vocabulary, loosely:
//   - FAKE: a working but simple implementation (fakeStore: a map, not SQL)
//   - STUB: returns canned answers (saveErr, mailErr)
//   - SPY:  records calls so the test can check them later (spyMailer.sent)
//
// These are just structs. One type can be all three, as fakePayments is.
// The mutexes are needed because handlers can run concurrently (race_test.go).

// Same trick as in payments.go: the build fails if a fake drifts out of step
// with its interface.
var (
	_ Store    = (*fakeStore)(nil)
	_ Payments = (*fakePayments)(nil)
	_ Mailer   = (*spyMailer)(nil)
)

type fakeStore struct {
	mu      sync.Mutex
	orders  map[int64]Order
	nextID  int64
	saveErr error // if set, Save fails with it
}

func (s *fakeStore) Save(ctx context.Context, o Order) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.saveErr != nil {
		return Order{}, s.saveErr
	}
	if s.orders == nil {
		s.orders = make(map[int64]Order)
	}
	s.nextID++
	o.ID = s.nextID
	s.orders[o.ID] = o
	return o, nil
}

func (s *fakeStore) Find(ctx context.Context, id int64) (Order, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	o, ok := s.orders[id]
	if !ok {
		return Order{}, fmt.Errorf("order %d: %w", id, ErrNotFound)
	}
	return o, nil
}

func (s *fakeStore) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.orders)
}

// fakePayments returns the scripted errors in errs, one per call, and
// succeeds once they run out. It remembers everything it was asked to do.
type fakePayments struct {
	mu      sync.Mutex
	errs    []error
	keys    []string // the idempotency key from each Charge call
	refunds []string
}

func (p *fakePayments) Charge(ctx context.Context, key string, pence int64) (string, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.keys = append(p.keys, key)
	if len(p.errs) > 0 {
		err := p.errs[0]
		p.errs = p.errs[1:]
		if err != nil {
			return "", err
		}
	}
	return fmt.Sprintf("ch_%d", len(p.keys)), nil
}

func (p *fakePayments) Refund(ctx context.Context, chargeID string) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refunds = append(p.refunds, chargeID)
	return nil
}

func (p *fakePayments) calls() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.keys)
}

type spyMailer struct {
	mu   sync.Mutex
	sent []Order
	err  error
}

func (m *spyMailer) SendReceipt(ctx context.Context, o Order) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return m.err
	}
	m.sent = append(m.sent, o)
	return nil
}

const testToken = "test-token"

var frozenNow = time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)

// app bundles a Service with its fakes, so a test can set up failures and
// then inspect what happened. Like a fresh container in Laravel's setUp().
type app struct {
	store   *fakeStore
	pay     *fakePayments
	mail    *spyMailer
	svc     *Service
	handler http.Handler
}

func newApp() *app {
	a := &app{store: &fakeStore{}, pay: &fakePayments{}, mail: &spyMailer{}}
	a.svc = NewService(a.store, a.pay, a.mail)
	a.svc.Now = func() time.Time { return frozenNow }
	a.handler = NewHandler(a.svc, testToken)
	return a
}
