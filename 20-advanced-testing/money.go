package shop

import (
	"errors"
	"strconv"
	"strings"
)

var ErrBadMoney = errors.New("bad money value")

// ParseMoney turns "12.50" into 1250 pence. Money is an integer in the
// smallest unit, never a float. It's a pure function that takes untrusted
// strings, which makes it the perfect thing to fuzz (fuzz_test.go).
func ParseMoney(s string) (int64, error) {
	digits, negative := strings.CutPrefix(s, "-")
	whole, frac, hasDot := strings.Cut(digits, ".")
	if !hasDot {
		frac = "00"
	}
	if !allDigits(whole) || len(frac) != 2 || !allDigits(frac) {
		return 0, ErrBadMoney
	}

	joined := whole + frac
	if negative {
		joined = "-" + joined
	}
	n, err := strconv.ParseInt(joined, 10, 64)
	if err != nil {
		return 0, ErrBadMoney // too big for an int64
	}
	return n, nil
}

// FormatMoney turns 1250 into "12.50". It works on the digits as text
// because the arithmetic version is a trap. See "THINGS TO TRY" in doc.go.
func FormatMoney(pence int64) string {
	s := strconv.FormatInt(pence, 10)
	digits, negative := strings.CutPrefix(s, "-")
	for len(digits) < 3 {
		digits = "0" + digits
	}
	cut := len(digits) - 2
	out := digits[:cut] + "." + digits[cut:]
	if negative {
		out = "-" + out
	}
	return out
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}
