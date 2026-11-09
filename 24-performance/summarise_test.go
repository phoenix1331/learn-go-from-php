package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
)

const sampleLog = `ts=1 user=a path=/orders status=200 ms=40
ts=2 user=b path=/orders status=500 ms=60
ts=3 path=/users ms=30
no useful fields here
path=/missing-ms status=200
ms=5 status=200
path= ms=5
path=/bad-ms ms=12x
path=/negative ms=-5
path=/first path=/second ms=1
path=/windows ms=7` + "\r\n"

var sampleWant = []PathStat{
	{"/orders", 2, 100},
	{"/users", 1, 30},
	{"/windows", 1, 7},
	{"/first", 1, 1},
}

func TestEveryVersionGivesTheSameReport(t *testing.T) {
	for _, v := range versions {
		t.Run(v.name, func(t *testing.T) {
			got, err := v.fn(strings.NewReader(sampleLog))
			if err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got, sampleWant) {
				t.Errorf("got  %+v\nwant %+v", got, sampleWant)
			}
		})
	}
}

func TestTiesSortByPath(t *testing.T) {
	got, err := SummariseStream(strings.NewReader("path=/b ms=1\npath=/a ms=1\n"))
	if err != nil {
		t.Fatal(err)
	}
	if got[0].Path != "/a" || got[1].Path != "/b" {
		t.Errorf("got %+v", got)
	}
}

func TestMsTooLargeIsSkipped(t *testing.T) {
	const line = "path=/big ms=9223372036854775808\npath=/max ms=9223372036854775807\n"
	for _, v := range versions {
		t.Run(v.name, func(t *testing.T) {
			got, err := v.fn(strings.NewReader(line))
			if err != nil {
				t.Fatal(err)
			}
			if len(got) != 1 || got[0].Path != "/max" {
				t.Errorf("got %+v", got)
			}
		})
	}
}

// bufio.Scanner's limit is a real behaviour change from the other versions,
// so it has to be an error rather than silently losing the rest of the file.
func TestStreamReportsOverlongLine(t *testing.T) {
	_, err := SummariseStream(strings.NewReader(strings.Repeat("x", maxLine+1)))
	if !errors.Is(err, bufio.ErrTooLong) {
		t.Errorf("err = %v, want bufio.ErrTooLong", err)
	}
}

func TestStreamAcceptsLongLines(t *testing.T) {
	line := "path=/long ms=1 padding=" + strings.Repeat("x", 100_000)
	got, err := SummariseStream(strings.NewReader(line))
	if err != nil || len(got) != 1 {
		t.Errorf("got %+v, err %v", got, err)
	}
}

// Differential fuzzing: when you have a slow version you trust, the rule
// writes itself. Whatever the input, the fast version must agree with it.
func FuzzVersionsAgree(f *testing.F) {
	f.Add(sampleLog)
	f.Add("path=a ms=1\r\r\n")
	f.Add("ms=x ms=5 path=a path=b\n\n")
	f.Add(" path=a  ms=1 ")
	f.Add("path=\xff ms=007")
	f.Fuzz(func(t *testing.T, input string) {
		want, err := SummariseNaive(strings.NewReader(input))
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range versions[1:] {
			got, err := v.fn(strings.NewReader(input))
			if err != nil {
				t.Fatalf("%s: %v", v.name, err)
			}
			if !reflect.DeepEqual(got, want) {
				t.Fatalf("%s disagrees on %q:\n got %+v\nwant %+v", v.name, input, got, want)
			}
		}
	})
}

// A benchmark tells you the number today. This test stops it getting worse:
// with a fixed set of paths, allocations mustn't grow with the line count.
func TestStreamAllocationsDoNotGrow(t *testing.T) {
	if raceEnabled {
		t.Skip("the race detector changes allocation counts")
	}
	allocs := func(lines int) float64 {
		data := generateLog(lines, 20)
		return testing.AllocsPerRun(20, func() {
			must(SummariseStream(strings.NewReader(data)))
		})
	}
	small, large := allocs(1_000), allocs(20_000)
	if large > small+5 {
		t.Errorf("allocations grew with input: %v for 1,000 lines, %v for 20,000", small, large)
	}
}

func TestPublicMuxDoesNotExposeProfiler(t *testing.T) {
	rec := httptest.NewRecorder()
	newPublicMux("").ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code != http.StatusNotFound {
		t.Errorf("status = %d, want 404", rec.Code)
	}
}

func TestDebugMuxServesProfiler(t *testing.T) {
	rec := httptest.NewRecorder()
	newDebugMux().ServeHTTP(rec, httptest.NewRequest("GET", "/debug/pprof/", nil))
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "goroutine") {
		t.Errorf("status = %d", rec.Code)
	}
}

func TestReportEndpoint(t *testing.T) {
	h := newPublicMux("path=/a ms=2\npath=/a ms=3\n")
	for _, target := range []string{"/report", "/report?version=naive"} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", target, nil))
		var got []PathStat
		if err := json.NewDecoder(rec.Body).Decode(&got); err != nil {
			t.Fatal(err)
		}
		if want := []PathStat{{"/a", 2, 5}}; !reflect.DeepEqual(got, want) {
			t.Errorf("%s: got %+v", target, got)
		}
	}
}
