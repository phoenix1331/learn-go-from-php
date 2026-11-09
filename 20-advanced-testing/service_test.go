package shop

import (
	"context"
	"errors"
	"testing"
	"testing/synctest"
	"time"
)

const validEmail = "ada@example.com"

// These tests are about the rules, so no HTTP is involved.

func TestPlaceChargesSavesAndEmails(t *testing.T) {
	a := newApp()

	order, err := a.svc.Place(t.Context(), validEmail, "12.50")
	if err != nil {
		t.Fatal(err)
	}

	if order.Pence != 1250 || order.Total != "12.50" || order.ChargeID != "ch_1" {
		t.Errorf("unexpected order: %+v", order)
	}
	if !order.CreatedAt.Equal(frozenNow) {
		t.Errorf("CreatedAt = %v, want the frozen time", order.CreatedAt)
	}
	if !order.ReceiptSent {
		t.Error("ReceiptSent = false")
	}
	// The spy: Laravel's Mail::assertSent(), written as a plain if.
	if len(a.mail.sent) != 1 || a.mail.sent[0].ID != order.ID {
		t.Errorf("receipts sent: %+v", a.mail.sent)
	}
}

func TestPlaceRejectsBadInputBeforeCharging(t *testing.T) {
	tests := []struct {
		name, email, amount string
	}{
		{"no @ in email", "ada", "10.00"},
		{"not money", validEmail, "ten"},
		{"zero", validEmail, "0.00"},
		{"negative", validEmail, "-5.00"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := newApp()
			_, err := a.svc.Place(t.Context(), tt.email, tt.amount)
			if !errors.Is(err, ErrInvalid) {
				t.Errorf("err = %v, want ErrInvalid", err)
			}
			if a.pay.calls() != 0 {
				t.Error("charged the customer for an invalid order")
			}
		})
	}
}

func TestMailFailureKeepsOrder(t *testing.T) {
	a := newApp()
	a.mail.err = errors.New("smtp down")

	order, err := a.svc.Place(t.Context(), validEmail, "10.00")
	if err != nil {
		t.Fatalf("a failed email must not fail the order: %v", err)
	}
	if order.ReceiptSent {
		t.Error("ReceiptSent = true, but sending failed")
	}
	if a.store.count() != 1 {
		t.Error("order wasn't saved")
	}
}

func TestDeclinedIsNotRetriedOrSaved(t *testing.T) {
	a := newApp()
	a.pay.errs = []error{ErrDeclined}

	_, err := a.svc.Place(t.Context(), validEmail, "10.00")
	if !errors.Is(err, ErrDeclined) {
		t.Fatalf("err = %v, want ErrDeclined", err)
	}
	if a.pay.calls() != 1 {
		t.Errorf("charged %d times, want 1", a.pay.calls())
	}
	if a.store.count() != 0 {
		t.Error("saved an order that wasn't paid for")
	}
}

// The test that justifies having fakes. Making a real database fail at
// exactly this moment would be very hard. Here it's one line.
func TestRefundsWhenSaveFails(t *testing.T) {
	a := newApp()
	a.store.saveErr = errors.New("database is down")

	_, err := a.svc.Place(t.Context(), validEmail, "10.00")
	if err == nil {
		t.Fatal("expected an error")
	}
	if len(a.pay.refunds) != 1 || a.pay.refunds[0] != "ch_1" {
		t.Errorf("refunds = %v, want [ch_1]", a.pay.refunds)
	}
	if len(a.mail.sent) != 0 {
		t.Error("sent a receipt for a refunded order")
	}
}

// FAKE TIME WITH synctest (Go 1.25+).
//
// Inside synctest.Test, the code runs in a "bubble" with a fake clock. When
// every goroutine in the bubble is blocked, the clock jumps straight to the
// next timer. time.After, time.Sleep and context timeouts all follow it, so
// no production code has to change. Unlike Carbon::setTestNow, this also
// moves timers that are already waiting.
//
// This test sleeps for 3 "seconds" and takes microseconds. The limit:
// goroutines blocked on real network I/O don't count as blocked, so use
// fakes inside a bubble rather than httptest.NewServer.
func TestRetryBacksOff(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newApp()
		a.pay.errs = []error{ErrUnavailable, ErrUnavailable}

		start := time.Now()
		if _, err := a.svc.Place(t.Context(), validEmail, "10.00"); err != nil {
			t.Fatal(err)
		}

		// Exact equality on time: only possible because the clock is fake.
		if got, want := time.Since(start), 3*time.Second; got != want {
			t.Errorf("took %v, want %v", got, want)
		}
		if a.pay.calls() != 3 {
			t.Fatalf("charged %d times, want 3", a.pay.calls())
		}
		// Every retry must reuse one key, or an outage could charge twice.
		if a.pay.keys[0] == "" || a.pay.keys[0] != a.pay.keys[1] || a.pay.keys[1] != a.pay.keys[2] {
			t.Errorf("idempotency keys differ: %q", a.pay.keys)
		}
	})
}

func TestRetryGivesUp(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newApp()
		a.pay.errs = []error{ErrUnavailable, ErrUnavailable, ErrUnavailable}

		_, err := a.svc.Place(t.Context(), validEmail, "10.00")
		if !errors.Is(err, ErrUnavailable) {
			t.Errorf("err = %v, want ErrUnavailable", err)
		}
		if a.pay.calls() != 3 {
			t.Errorf("charged %d times, want 3", a.pay.calls())
		}
	})
}

func TestRetryStopsWhenCancelled(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		a := newApp()
		a.pay.errs = []error{ErrUnavailable}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()

		result := make(chan error, 1)
		go func() {
			_, err := a.svc.Place(ctx, validEmail, "10.00")
			result <- err
		}()

		// Wait returns once every other goroutine in the bubble is blocked:
		// here, sitting in charge()'s retry delay. No sleeps, no guessing.
		synctest.Wait()
		cancel()

		if err := <-result; !errors.Is(err, context.Canceled) {
			t.Errorf("err = %v, want context.Canceled", err)
		}
		if a.pay.calls() != 1 {
			t.Errorf("charged %d times, want 1", a.pay.calls())
		}
	})
}
