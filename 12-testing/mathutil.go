// LESSON 12 — TESTING (part 1: the code under test)
//
// This folder is a LIBRARY package (not `main`), so there's nothing to
// `go run`. Instead, run its tests:
//
//	go test ./12-testing          # run tests
//	go test -v ./12-testing       # verbose: show each test
//	go test -cover ./12-testing   # show code coverage %
//	go test -bench=. ./12-testing # run benchmarks too
//	go test ./...                 # test every package in the repo
//
// Testing is built into Go — no PHPUnit/Pest to install.
// Now open mathutil_test.go alongside this file.
package mathutil

import "errors"

var ErrEmpty = errors.New("empty slice")

// Average returns the mean of nums, or ErrEmpty if there are none.
func Average(nums []float64) (float64, error) {
	if len(nums) == 0 {
		return 0, ErrEmpty
	}
	var total float64
	for _, n := range nums {
		total += n
	}
	return total / float64(len(nums)), nil
}

// IsPalindrome reports whether s reads the same backwards.
// It converts to []rune so multi-byte characters (é, emoji) work properly.
func IsPalindrome(s string) bool {
	r := []rune(s)
	for i, j := 0, len(r)-1; i < j; i, j = i+1, j-1 {
		if r[i] != r[j] {
			return false
		}
	}
	return true
}
