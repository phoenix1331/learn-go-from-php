// LESSON 03 — FUNCTIONS: params, multiple returns, variadics, closures, pointers
//
// Run with:  go run ./03-functions
package main

import (
	"errors"
	"fmt"
)

// Basic function. Types come AFTER names: `a int`, and the return type
// goes after the parentheses.
func add(a int, b int) int {
	return a + b
}

// Consecutive params of the same type can share it.
func multiply(a, b int) int {
	return a * b
}

// MULTIPLE RETURN VALUES — one of Go's signature features.
// The idiomatic pattern is (result, error). Go has no exceptions for normal
// failures: errors are just values you return and check.
func divide(a, b float64) (float64, error) {
	if b == 0 {
		return 0, errors.New("cannot divide by zero")
	}
	return a / b, nil // nil = "no error"
}

// NAMED RETURN VALUES. They start at their zero value, and a bare
// `return` sends back whatever they currently hold. Handy for short
// functions; avoid in long ones as it gets hard to follow.
func minMax(nums []int) (min, max int) {
	min, max = nums[0], nums[0]
	for _, n := range nums {
		if n < min {
			min = n
		}
		if n > max {
			max = n
		}
	}
	return
}

// VARIADIC function: `...int` accepts any number of ints and
// receives them as a slice (like PHP's `...$nums`).
func sum(nums ...int) int {
	total := 0
	for _, n := range nums {
		total += n
	}
	return total
}

// Functions are values: they can be passed as arguments...
func apply(nums []int, fn func(int) int) []int {
	result := make([]int, 0, len(nums))
	for _, n := range nums {
		result = append(result, fn(n))
	}
	return result
}

// ...and returned from other functions. This returns a CLOSURE: the inner
// function "captures" the variable `count` and keeps it alive between calls.
// (PHP needs `use (&$count)` for this; Go captures automatically.)
func makeCounter() func() int {
	count := 0
	return func() int {
		count++
		return count
	}
}

// POINTERS. Go passes everything BY VALUE (a copy) by default.
// To let a function modify the caller's variable, pass a pointer.
//
//	*int  = "pointer to an int" (the type)
//	&x    = "address of x"     (get a pointer)
//	*p    = "value at p"       (follow the pointer)
func doubleCopy(n int) {
	n *= 2 // only changes the local copy
}

func doublePointer(n *int) {
	*n *= 2 // changes the original
}

func main() {
	fmt.Println("add:", add(2, 3))
	fmt.Println("multiply:", multiply(4, 5))

	// Receive multiple returns. ALWAYS check the error first.
	// You'll write `if err != nil` a lot — that's normal Go.
	result, err := divide(10, 4)
	if err != nil {
		fmt.Println("error:", err)
	} else {
		fmt.Println("divide:", result)
	}

	_, err = divide(1, 0) // ignore the result with _, keep the error
	if err != nil {
		fmt.Println("error:", err)
	}

	lo, hi := minMax([]int{7, 2, 9, 4})
	fmt.Println("min/max:", lo, hi)

	fmt.Println("sum:", sum(1, 2, 3, 4))
	nums := []int{10, 20, 30}
	fmt.Println("sum of slice:", sum(nums...)) // spread a slice with ...

	// Anonymous function passed inline (like a PHP closure / arrow fn).
	squared := apply([]int{1, 2, 3}, func(n int) int { return n * n })
	fmt.Println("squared:", squared)

	counter := makeCounter()
	fmt.Println("counter:", counter(), counter(), counter()) // 1 2 3

	x := 5
	doubleCopy(x)
	fmt.Println("after doubleCopy:", x) // still 5
	doublePointer(&x)
	fmt.Println("after doublePointer:", x) // 10
}
