// LESSON 04 — COLLECTIONS: arrays, slices, maps
//
// Run with:  go run ./04-collections
//
// PHP has one "array" that does everything. Go splits that into:
//   - arrays: fixed length (rarely used directly)
//   - slices: growable lists       -> like PHP [1, 2, 3]
//   - maps:   key/value lookups    -> like PHP ['name' => 'Darren']
//
// Each holds ONE type of element, e.g. []string or map[string]int.
package main

import (
	"fmt"
	"maps"
	"slices"
	"sort"
)

func main() {
	// ---------------------------------------------------------------
	// 1. ARRAYS — fixed size, the size is part of the type
	// ---------------------------------------------------------------
	var grid [3]int // [0 0 0]
	grid[1] = 5
	colours := [3]string{"red", "green", "blue"}
	fmt.Println(grid, colours, len(colours))
	// [3]int and [4]int are DIFFERENT types. That's why you'll mostly
	// use slices instead.

	// ---------------------------------------------------------------
	// 2. SLICES — the list type you'll use everywhere
	// ---------------------------------------------------------------
	nums := []int{1, 2, 3} // no size in the brackets = slice

	// append returns a NEW slice header; always assign it back.
	nums = append(nums, 4, 5)
	fmt.Println("nums:", nums, "len:", len(nums))

	// Slicing: s[start:end] — start included, end excluded.
	fmt.Println("nums[1:3]:", nums[1:3]) // [2 3]
	fmt.Println("nums[:2]:", nums[:2])   // [1 2]
	fmt.Println("nums[3:]:", nums[3:])   // [4 5]

	// make(type, length, capacity) pre-allocates. If you know roughly
	// how many items you'll add, this avoids repeated re-allocation.
	names := make([]string, 0, 10)
	names = append(names, "Ada", "Linus", "Grace")
	fmt.Println("names:", names, "len:", len(names), "cap:", cap(names))

	// GOTCHA: a sub-slice SHARES memory with the original.
	original := []int{1, 2, 3}
	view := original[:2]
	view[0] = 99
	fmt.Println("original changed too:", original) // [99 2 3]
	// Use slices.Clone (or copy) when you need an independent copy.
	independent := slices.Clone(original)
	independent[0] = 1
	fmt.Println("clone is separate:", original, independent)

	// The `slices` package has the helpers you'd expect from PHP.
	fmt.Println("contains Ada?", slices.Contains(names, "Ada"))  // in_array
	fmt.Println("index of Grace:", slices.Index(names, "Grace")) // array_search
	slices.Sort(names)                                           // sort (in place)
	fmt.Println("sorted:", names)

	// Sorting by a custom rule (like PHP usort).
	people := []string{"Bob", "alexander", "Eve"}
	sort.Slice(people, func(i, j int) bool {
		return len(people[i]) < len(people[j])
	})
	fmt.Println("by length:", people)

	// A nil slice is valid and behaves like an empty one.
	var empty []int
	fmt.Println("nil slice:", empty, len(empty), empty == nil)
	empty = append(empty, 1) // appending to nil is fine
	fmt.Println("after append:", empty)

	// 2D slice (slice of slices).
	matrix := [][]int{
		{1, 2, 3},
		{4, 5, 6},
	}
	fmt.Println("matrix[1][2]:", matrix[1][2]) // 6

	// ---------------------------------------------------------------
	// 3. MAPS — key/value pairs (PHP associative arrays)
	// ---------------------------------------------------------------
	ages := map[string]int{
		"alice": 31,
		"bob":   25, // trailing comma required on multi-line literals
	}
	ages["carol"] = 40  // add / update
	delete(ages, "bob") // unset($ages['bob'])
	fmt.Println("ages:", ages, "len:", len(ages))

	// Missing keys return the ZERO value, not an error/null.
	fmt.Println("dave's age:", ages["dave"]) // 0

	// So how do you tell "missing" from "actually 0"? The "comma ok" idiom
	// (like PHP's array_key_exists / isset).
	if age, ok := ages["alice"]; ok {
		fmt.Println("found alice:", age)
	}
	if _, ok := ages["dave"]; !ok {
		fmt.Println("dave not found")
	}

	// IMPORTANT: map iteration order is RANDOM in Go — on purpose.
	// Run this file a few times and watch the order change.
	for name, age := range ages {
		fmt.Println(name, "is", age)
	}

	// Need a stable order? Sort the keys first.
	keys := slices.Sorted(maps.Keys(ages))
	for _, k := range keys {
		fmt.Println("sorted:", k, ages[k])
	}

	// A map must be initialised before writing. This would PANIC:
	//   var broken map[string]int
	//   broken["x"] = 1   // panic: assignment to entry in nil map
	// Use a literal (as above) or make():
	scores := make(map[string][]int) // map of string -> slice of ints
	scores["maths"] = append(scores["maths"], 90, 85)
	fmt.Println("scores:", scores)

	// A "set" in Go is usually map[T]bool or map[T]struct{}.
	seen := map[string]bool{}
	for _, w := range []string{"go", "php", "go", "rust", "php"} {
		seen[w] = true
	}
	fmt.Println("unique words:", len(seen)) // 3
}
