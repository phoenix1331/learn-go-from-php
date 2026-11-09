package shop

import (
	"errors"
	"math"
	"strings"
	"testing"
)

// FUZZING. You write a rule that must ALWAYS hold, and Go invents inputs,
// using code coverage to steer towards new paths. It's a way of finding the
// cases you'd never think to put in a table.
//
//   go test ./20-advanced-testing
//     Runs each fuzz test once per seed, as an ordinary fast test. CI does this.
//
//   go test -fuzz=FuzzMoneyRoundTrip -fuzztime=10s ./20-advanced-testing
//     Actually generates inputs. Needs exactly one target and one package.
//     Without -fuzztime it runs until it finds a failure or you press Ctrl+C.
//
// When it finds a failure, it shrinks the input and saves it to
// testdata/fuzz/<FuzzName>/. Commit that file: from then on, plain `go test`
// replays it, so the bug stays fixed.
//
// The hard part is thinking of a rule, since you don't know the input.
// Good ones: "never panics", "A then undo-A gives back the original",
// "the output is always valid".

// Rule: formatting any amount and parsing it back gives the same amount.
//
// The seeds are deliberately all positive, so the fuzzer has to discover
// negatives by itself. See "THINGS TO TRY" in doc.go.
func FuzzMoneyRoundTrip(f *testing.F) {
	for _, seed := range []int64{0, 1, 99, 100, 1250, math.MaxInt64} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, pence int64) {
		s := FormatMoney(pence)
		got, err := ParseMoney(s)
		if err != nil {
			t.Fatalf("ParseMoney(FormatMoney(%d)) = %q: %v", pence, s, err)
		}
		if got != pence {
			t.Fatalf("round trip: %d -> %q -> %d", pence, s, got)
		}
	})
}

// Rule: whatever the string, ParseMoney never panics, reports failure the
// documented way, and anything it accepts survives a round trip.
func FuzzParseMoney(f *testing.F) {
	for _, seed := range []string{"12.50", "0.00", "-1.00", "1", "", ".", "1.2", "1e5", "٣.٠٠"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, s string) {
		n, err := ParseMoney(s)
		if err != nil {
			if !errors.Is(err, ErrBadMoney) {
				t.Fatalf("ParseMoney(%q): unexpected error type: %v", s, err)
			}
			return
		}
		again, err := ParseMoney(FormatMoney(n))
		if err != nil || again != n {
			t.Fatalf("ParseMoney(%q) = %d, but round trip gave %d, %v", s, n, again, err)
		}
	})
}

// Fuzzing a whole endpoint. Rule: no request body, however strange, can
// cause a 5xx, and a 201 always means a real, positive order.
func FuzzPlaceOrderBody(f *testing.F) {
	for _, seed := range []string{
		validBody,
		`{}`,
		`null`,
		`[1,2,3]`,
		`{"email":"@","amount":"0.01"}`,
		`{"email":"a@b","amount":"99999999999999999.99"}`,
	} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, body string) {
		a := newApp() // fresh fakes per input: runs can't affect each other
		rec := a.serve(newRequest(t, "POST", "/orders", body))

		if rec.Code >= 500 {
			t.Fatalf("status %d for body %q: %s", rec.Code, body, rec.Body)
		}
		if rec.Code != 201 {
			if a.pay.calls() != 0 {
				t.Fatalf("status %d but the customer was charged, body %q", rec.Code, body)
			}
			return
		}
		order := decodeJSON[Order](t, strings.NewReader(rec.Body.String()))
		if order.Pence <= 0 || order.ID == 0 {
			t.Fatalf("201 with a bad order %+v, body %q", order, body)
		}
	})
}
