// LESSON 09 — GENERICS: write a function or type once, use it with many types
//
// Run with:  go run ./09-generics
//
// Without generics you'd need SumInts, SumFloats, and so on, or fall back to
// `any` and lose type safety. Type parameters go in square brackets.
package main

import (
	"cmp"
	"fmt"
	"strings"
)

// [T any] declares a type parameter T that can be any type.
// Map applies fn to every element (like PHP's array_map).
func Map[T, U any](items []T, fn func(T) U) []U {
	out := make([]U, 0, len(items))
	for _, item := range items {
		out = append(out, fn(item))
	}
	return out
}

// Filter keeps the elements where keep returns true (array_filter).
func Filter[T any](items []T, keep func(T) bool) []T {
	var out []T
	for _, item := range items {
		if keep(item) {
			out = append(out, item)
		}
	}
	return out
}

// CONSTRAINTS restrict which types are allowed. This interface lists types
// using `|`. The ~ means "this type, or any type whose underlying type is it".
type Number interface {
	~int | ~int64 | ~float64
}

func Sum[T Number](nums []T) T {
	var total T // zero value of whatever T is
	for _, n := range nums {
		total += n // allowed because every Number supports +
	}
	return total
}

// cmp.Ordered is a built-in constraint for anything you can compare with <.
func Max[T cmp.Ordered](a, b T) T {
	if a > b {
		return a
	}
	return b
}

// GENERIC TYPES: a Stack that works for any element type.
type Stack[T any] struct {
	items []T
}

func (s *Stack[T]) Push(v T) {
	s.items = append(s.items, v)
}

// Returns the value and whether there was one (the "comma ok" pattern again).
func (s *Stack[T]) Pop() (T, bool) {
	var zero T
	if len(s.items) == 0 {
		return zero, false
	}
	last := s.items[len(s.items)-1]
	s.items = s.items[:len(s.items)-1]
	return last, true
}

// A generic key/value pair used as a return type.
type Pair[K comparable, V any] struct {
	Key   K
	Value V
}

func main() {
	nums := []int{1, 2, 3, 4, 5, 6}

	// Go usually INFERS T from the arguments, so you don't write Map[int, string].
	labels := Map(nums, func(n int) string { return fmt.Sprintf("#%d", n) })
	fmt.Println("labels:", labels)

	evens := Filter(nums, func(n int) bool { return n%2 == 0 })
	fmt.Println("evens:", evens)

	words := []string{"go", "php", "rust", "javascript"}
	long := Filter(words, func(w string) bool { return len(w) > 3 })
	fmt.Println("long words:", Map(long, strings.ToUpper))

	fmt.Println("sum ints:", Sum(nums))
	fmt.Println("sum floats:", Sum([]float64{1.5, 2.25}))
	// Sum([]string{"a"}) // compile error: string does not satisfy Number

	fmt.Println("max:", Max(3, 9), Max("apple", "banana"), Max(2.5, 1.5))

	var s Stack[string]
	s.Push("first")
	s.Push("second")
	for {
		v, ok := s.Pop()
		if !ok {
			break
		}
		fmt.Println("popped:", v)
	}

	p := Pair[string, int]{Key: "visits", Value: 1200}
	fmt.Printf("%+v\n", p)
}
