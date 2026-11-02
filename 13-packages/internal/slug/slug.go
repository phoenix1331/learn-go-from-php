// Package slug turns text into URL-friendly slugs (like Str::slug in Laravel).
//
// INTERNAL PACKAGES: any package under a folder called `internal/` can only be
// imported by code rooted at the PARENT of that `internal/` folder.
//
//	13-packages/internal/slug  can be imported by  13-packages/...   ✅
//	                           but NOT by          11-http-server/   ❌
//
// The compiler enforces this ("use of internal package ... not allowed").
// It's how Go libraries keep helpers private without exporting them to the
// world. PHP has no equivalent: anything autoloadable is usable by anyone,
// and you rely on @internal docblocks and good manners.
package slug

import (
	"strings"
	"unicode"
)

// Make lowercases s, keeps letters and digits, and joins words with sep.
//
//	Make("Hello, World!", "-")  // "hello-world"
func Make(s, sep string) string {
	// strings.FieldsFunc splits on every rune where the function returns true,
	// and drops empty pieces, so runs of punctuation collapse for free.
	words := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
	return strings.Join(words, sep)
}
