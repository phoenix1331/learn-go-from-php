// LESSON 20 — ADVANCED TESTING: httptest, fakes, fuzzing, races, fake time
//
// Test with: go test -race -v ./20-advanced-testing
// Fuzz with: go test -fuzz=FuzzMoneyRoundTrip -fuzztime=10s ./20-advanced-testing
//
// There's no main here. Like lesson 12, the tests are the lesson. The code
// under test is a small order API: it takes a payment, saves the order and
// emails a receipt. Read it first, then the tests in this order:
//
//   - money.go, orders.go, http.go, payments.go   the code under test
//   - fakes_test.go      hand-written stand-ins for the interfaces
//   - service_test.go    business rules, using fakes; fake time with synctest
//   - handler_test.go    our HTTP API, with no network involved
//   - payments_test.go   our HTTP CLIENT, against a throwaway real server
//   - fuzz_test.go       letting Go invent the inputs
//   - race_test.go       proving the concurrent paths are safe
//
// IF YOU KNOW LARAVEL'S TESTING HELPERS
//
//	$this->postJson('/orders', [...])   httptest.NewRecorder + handler.ServeHTTP
//	Http::fake([...])                   httptest.NewServer with a test handler
//	Mail::fake(), Mail::assertSent()    a spy struct implementing Mailer
//	$this->mock(Gateway::class)         a fake struct implementing Payments
//	Carbon::setTestNow(), $this->travel  a Now field, or testing/synctest
//
// The big difference: Laravel can swap anything through the container or a
// facade at runtime. Go can't, and doesn't want to. You can only replace
// what the code receives as an INTERFACE. So testable Go comes from how the
// code is written (see Service in orders.go), not from a mocking library.
//
// GOING FURTHER
//   - GOLDEN FILES: for big outputs, compare against testdata/x.golden and
//     add a -update flag that rewrites it. Like Pest snapshot testing.
//   - SLOW TESTS: call t.Skip when testing.Short() is true, then run
//     `go test -short` locally and the full set in CI.
//   - INTEGRATION TESTS: put `//go:build integration` at the top of a file
//     and it only runs with `go test -tags=integration`.
//   - MORE FLAGS: -shuffle=on finds tests that depend on each other's order.
//     -count=1 skips the test result cache.
//   - MOCK GENERATORS: uber-go/mock and testify/mock exist, but hand-written
//     fakes stay readable while your interfaces stay small.
//
// THINGS TO TRY
//   - Replace FormatMoney's body with
//     `return fmt.Sprintf("%d.%02d", pence/100, pence%100)` (and import fmt),
//     then run the fuzz command above. Plain `go test` still passes, but the
//     fuzzer soon finds the bug and saves the input under testdata/. Delete
//     that folder afterwards.
//   - Delete the two m.mu lines from Metrics.Inc in http.go, then run
//     `go test -race -run Concurrent ./20-advanced-testing` and read the
//     report: it names both goroutines and the exact lines.
//   - In Service.Place, replace the Refund call with `var refundErr error`,
//     and watch TestRefundsWhenSaveFails catch the customer being charged
//     for nothing.
//   - Change the 2 in retryDelays to 20 and see TestRetryBacksOff report a
//     21 second total, still instantly.
package shop
