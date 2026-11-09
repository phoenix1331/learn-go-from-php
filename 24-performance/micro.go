package main

import "strings"

// Small pairs of functions, showing the most common sources of allocations.
// micro_test.go benchmarks them. Section 5 of the tour counts allocations.

// ConcatPlus copies the whole string so far on every step, because strings
// are immutable, so the work grows with the square of the length. PHP's .=
// usually extends the string in place, which is why this habit is a surprise.
func ConcatPlus(parts []string) string {
	out := ""
	for _, p := range parts {
		out += p
	}
	return out
}

// ConcatBuilder appends to one buffer, sized up front with Grow.
func ConcatBuilder(parts []string) string {
	size := 0
	for _, p := range parts {
		size += len(p)
	}
	var b strings.Builder
	b.Grow(size)
	for _, p := range parts {
		b.WriteString(p)
	}
	return b.String()
}

// AppendGrow makes append reallocate and copy as the slice outgrows itself.
func AppendGrow(n int) []int {
	var out []int
	for i := range n {
		out = append(out, i)
	}
	return out
}

// AppendPresized does one allocation, because the final size is known.
func AppendPresized(n int) []int {
	out := make([]int, 0, n)
	for i := range n {
		out = append(out, i)
	}
	return out
}

// Point is small enough that copying it is cheap.
type Point struct{ X, Y int }

// SumOnStack: p never leaves this function, so it lives on the stack and
// costs the garbage collector nothing.
//
//go:noinline
func SumOnStack(x, y int) int {
	p := Point{x, y}
	return p.X + p.Y
}

// NewOnHeap: the pointer outlives the function, so p "escapes" to the heap.
// PHP developers often reach for pointers to avoid copying. In Go, for small
// values, the copy is usually cheaper than the allocation.
//
//go:noinline
func NewOnHeap(x, y int) *Point {
	p := Point{x, y}
	return &p
}
