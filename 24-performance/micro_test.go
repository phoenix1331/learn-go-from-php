package main

import (
	"fmt"
	"strconv"
	"strings"
	"testing"
)

// READING THE OUTPUT
//
//	BenchmarkSummarise/stream-8   150   7800000 ns/op   41.2 MB/s   9000 B/op   31 allocs/op
//	           name         GOMAXPROCS  runs  time per run  throughput  memory   allocations
//
// b.Loop (Go 1.24) replaced the old `for i := 0; i < b.N; i++` style. It only
// times the loop body, so setup above it isn't counted, and it stops the
// compiler optimising away a result you never use, which would otherwise
// report an impossibly fast 0.2 ns/op.

func BenchmarkSummarise(b *testing.B) {
	data := generateLog(20_000, 20)
	for _, v := range versions {
		b.Run(v.name, func(b *testing.B) {
			b.ReportAllocs()
			b.SetBytes(int64(len(data))) // adds an MB/s column
			for b.Loop() {
				if _, err := v.fn(strings.NewReader(data)); err != nil {
					b.Fatal(err)
				}
			}
		})
	}
}

func BenchmarkParseLine(b *testing.B) {
	line := []byte("ts=2026-03-14T10:00:00Z user=ada path=/orders status=200 ms=45")
	b.ReportAllocs()
	for b.Loop() {
		if _, _, ok := parseLine(line); !ok {
			b.Fatal("should parse")
		}
	}
}

func BenchmarkConcat(b *testing.B) {
	parts := strings.Split(strings.Repeat("word ", 1000), " ")
	b.Run("plus", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ConcatPlus(parts)
		}
	})
	b.Run("builder", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			ConcatBuilder(parts)
		}
	})
}

func BenchmarkAppend(b *testing.B) {
	b.Run("grow", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			AppendGrow(10_000)
		}
	})
	b.Run("presized", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			AppendPresized(10_000)
		}
	})
}

func BenchmarkFormatInt(b *testing.B) {
	b.Run("Sprintf", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = fmt.Sprintf("%d", 123456)
		}
	})
	b.Run("strconv", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			_ = strconv.Itoa(123456)
		}
	})
}

func BenchmarkStackVsHeap(b *testing.B) {
	b.Run("stack", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			SumOnStack(1, 2)
		}
	})
	b.Run("heap", func(b *testing.B) {
		b.ReportAllocs()
		for b.Loop() {
			NewOnHeap(1, 2)
		}
	})
}
