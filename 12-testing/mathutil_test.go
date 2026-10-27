// LESSON 12 — TESTING (part 2: the tests)
//
// Rules Go uses to find tests:
//   - the file name ends in _test.go (excluded from normal builds)
//   - test functions are named TestXxx and take (t *testing.T)
//   - benchmarks are named BenchmarkXxx and take (b *testing.B)
package mathutil

import (
	"errors"
	"testing"
)

// A simple test. There are no assert functions in the standard library:
// you write a plain `if` and call t.Errorf (marks the test failed but keeps
// going) or t.Fatalf (fails and stops this test immediately).
func TestAverage(t *testing.T) {
	got, err := Average([]float64{2, 4, 6})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != 4 {
		t.Errorf("Average() = %v, want 4", got)
	}
}

func TestAverageEmpty(t *testing.T) {
	_, err := Average(nil)
	if !errors.Is(err, ErrEmpty) {
		t.Errorf("expected ErrEmpty, got %v", err)
	}
}

// TABLE-DRIVEN TESTS — the most common Go testing style.
// Define cases as a slice of anonymous structs, then loop over them.
// (Like Pest datasets or PHPUnit data providers.)
func TestIsPalindrome(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  bool
	}{
		{"empty string", "", true},
		{"single letter", "a", true},
		{"simple palindrome", "racecar", true},
		{"not a palindrome", "golang", false},
		{"unicode", "été", true},
	}

	for _, tt := range tests {
		// t.Run creates a named subtest, so failures show which case broke.
		// Run one case with:  go test -v -run 'TestIsPalindrome/unicode' ./12-testing
		t.Run(tt.name, func(t *testing.T) {
			if got := IsPalindrome(tt.input); got != tt.want {
				t.Errorf("IsPalindrome(%q) = %v, want %v", tt.input, got, tt.want)
			}
		})
	}
}

// A BENCHMARK measures speed. b.Loop() runs the body enough times to
// get a reliable timing (Go 1.24+).
func BenchmarkIsPalindrome(b *testing.B) {
	for b.Loop() {
		IsPalindrome("abcdefghijjihgfedcba")
	}
}
