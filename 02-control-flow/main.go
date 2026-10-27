// LESSON 02 — CONTROL FLOW: if, for, switch, defer
//
// Run with:  go run ./02-control-flow
//
// Go keeps this small: there's only ONE loop keyword (`for`), no parentheses
// around conditions, and braces are always required.
package main

import "fmt"

func main() {
	// ---------------------------------------------------------------
	// 1. IF / ELSE
	// ---------------------------------------------------------------
	score := 72

	// No parentheses around the condition. Braces are mandatory,
	// and `else` must be on the same line as the closing brace.
	if score >= 90 {
		fmt.Println("A")
	} else if score >= 70 {
		fmt.Println("B")
	} else {
		fmt.Println("C")
	}

	// `if` can run a short statement first. The variable it declares
	// (`half` here) only exists inside the if/else blocks.
	// You'll see this constantly with errors: `if err := doThing(); err != nil {`
	if half := score / 2; half > 30 {
		fmt.Println("half is", half)
	}
	// fmt.Println(half) // would not compile — half is out of scope

	// Conditions must be real booleans. PHP lets you write `if ($count)`,
	// Go does not — you must write `if count > 0`.

	// ---------------------------------------------------------------
	// 2. FOR — the only loop in Go
	// ---------------------------------------------------------------

	// Classic C-style loop (like PHP's for).
	for i := 0; i < 3; i++ {
		fmt.Println("classic loop", i)
	}

	// "While" loop: just a condition.
	n := 1
	for n < 50 {
		n *= 3
	}
	fmt.Println("n ended at", n)

	// Range over an integer (Go 1.22+): loops 0, 1, 2.
	for i := range 3 {
		fmt.Println("range int", i)
	}

	// Range over a slice gives index AND value (like PHP foreach $k => $v).
	fruits := []string{"apple", "banana", "cherry"}
	for index, fruit := range fruits {
		fmt.Println(index, fruit)
	}

	// Don't need the index? Use `_` to ignore it.
	for _, fruit := range fruits {
		fmt.Println("I like", fruit)
	}

	// Infinite loop with break / continue.
	count := 0
	for {
		count++
		if count%2 == 0 {
			continue // skip even numbers
		}
		if count > 7 {
			break // exit the loop
		}
		fmt.Println("odd:", count)
	}

	// ---------------------------------------------------------------
	// 3. SWITCH
	// ---------------------------------------------------------------
	day := "sat"

	// Unlike PHP, cases do NOT fall through — no `break` needed.
	// One case can match several values.
	switch day {
	case "sat", "sun":
		fmt.Println("weekend")
	case "mon":
		fmt.Println("ugh, monday")
	default:
		fmt.Println("weekday")
	}

	// A switch with no value is a neat replacement for long if/else chains.
	// (Similar to PHP's `match (true)`.)
	temp := 18
	switch {
	case temp < 10:
		fmt.Println("cold")
	case temp < 20:
		fmt.Println("mild")
	default:
		fmt.Println("warm")
	}

	// ---------------------------------------------------------------
	// 4. DEFER
	// ---------------------------------------------------------------
	// `defer` schedules a call to run when the surrounding FUNCTION returns.
	// Used for cleanup: closing files, unlocking mutexes, closing DB rows.
	// Multiple defers run in reverse order (last in, first out).
	deferDemo()
}

func deferDemo() {
	fmt.Println("deferDemo: start")
	defer fmt.Println("deferDemo: deferred 1 (runs last)")
	defer fmt.Println("deferDemo: deferred 2 (runs first)")
	fmt.Println("deferDemo: end of function body")

	// Typical real-world use:
	//   f, err := os.Open("data.csv")
	//   if err != nil { return err }
	//   defer f.Close()   // guaranteed to close, however we leave the function
}
