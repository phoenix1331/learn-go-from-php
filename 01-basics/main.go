// LESSON 01 — BASICS: packages, imports, variables, types, constants
//
// Run with:  go run ./01-basics
//
// Every Go file starts with a `package` line. A package called `main` that
// contains a `func main()` is special: it compiles into a runnable program.
// (Any other package name = a library that other code imports.)
package main

// Imports. Unused imports are a COMPILE ERROR in Go — the compiler keeps
// your code tidy for you. Your editor (gopls) usually adds/removes these.
import (
	"fmt"     // formatted printing (like PHP's echo / printf / sprintf)
	"strings" // string helpers (like PHP's str_* functions)
)

// Constants are fixed at compile time. Untyped constants like this adapt to
// whatever numeric type they're used with.
const Pi = 3.14159

// A group of constants. `iota` counts up from 0 inside a const block —
// Go's lightweight way of doing enums.
const (
	Small  = iota // 0
	Medium        // 1  (the "= iota" is implied for following lines)
	Large         // 2
)

func main() {
	// ---------------------------------------------------------------
	// 1. DECLARING VARIABLES
	// ---------------------------------------------------------------

	// Long form: var name type = value
	var name string = "Darren"

	// Type can be inferred from the value.
	var age = 40

	// Short form `:=` — declares AND assigns, type inferred.
	// This is what you'll use 90% of the time. Only works inside functions.
	city := "London"

	// Variables are STATICALLY typed. Unlike PHP, this would not compile:
	//   age = "forty"   // error: cannot use "forty" (string) as int

	fmt.Println(name, age, city)

	// ---------------------------------------------------------------
	// 2. ZERO VALUES
	// ---------------------------------------------------------------
	// Declaring without a value gives you the type's "zero value".
	// There is no `undefined`, and no null for basic types.
	var i int     // 0
	var f float64 // 0
	var s string  // "" (empty string, NOT null)
	var b bool    // false
	fmt.Printf("zero values: %d %v %q %v\n", i, f, s, b)

	// ---------------------------------------------------------------
	// 3. BASIC TYPES
	// ---------------------------------------------------------------
	var count int = 42       // size depends on platform (64-bit on yours)
	var big int64 = 1 << 40  // explicitly 64-bit; also int8, int16, int32
	var price float64 = 9.99 // decimals (float32 also exists)
	var letter rune = 'G'    // a single Unicode character (single quotes!)
	var raw byte = 'A'       // a single byte (alias for uint8)
	var ok bool = true
	fmt.Println(count, big, price, letter, string(letter), raw, ok)

	// NO automatic type conversion — you must convert explicitly.
	// PHP would happily do 42 + 9.99; Go makes you say what you mean:
	total := float64(count) + price
	fmt.Println("total:", total)

	// ---------------------------------------------------------------
	// 4. STRINGS
	// ---------------------------------------------------------------
	greeting := "Hello, " + name // concatenate with + (PHP uses .)

	// Backticks = raw string: no escapes, can span multiple lines.
	multi := `line one
line two \n (not a newline here)`
	fmt.Println(greeting)
	fmt.Println(multi)

	// Strings are immutable byte sequences. len() counts BYTES, not chars:
	fmt.Println(len("café")) // 5, because é takes 2 bytes in UTF-8

	// The strings package covers what PHP's str_* functions do.
	fmt.Println(strings.ToUpper("go is fun"))          // strtoupper
	fmt.Println(strings.Contains("golang", "lang"))    // str_contains
	fmt.Println(strings.Split("a,b,c", ","))           // explode
	fmt.Println(strings.Join([]string{"x", "y"}, "-")) // implode
	fmt.Println(strings.Replace("aaa", "a", "b", 2))   // str_replace (limit 2)
	fmt.Println(strings.TrimSpace("   padded   "))     // trim

	// ---------------------------------------------------------------
	// 5. FORMATTED OUTPUT (Printf / Sprintf)
	// ---------------------------------------------------------------
	// %v  = default format for any value (your go-to)
	// %+v = like %v but includes struct field names
	// %T  = the TYPE of the value (great for learning!)
	// %d  = integer   %s = string   %q = quoted string
	// %f  = float     %.2f = float with 2 decimals
	fmt.Printf("%v is type %T\n", price, price)
	fmt.Printf("%s is %d years old\n", name, age)
	fmt.Printf("price: £%.2f\n", price)

	// Sprintf returns the string instead of printing it (same as PHP).
	label := fmt.Sprintf("%s from %s", name, city)
	fmt.Println(label)

	// ---------------------------------------------------------------
	// 6. CONSTANTS & IOTA
	// ---------------------------------------------------------------
	fmt.Println("Pi:", Pi)
	fmt.Println("sizes:", Small, Medium, Large)

	// Unused LOCAL variables are also a compile error. If you genuinely
	// need to ignore something, assign it to the blank identifier `_`.
	_ = "this value is deliberately ignored"
}
