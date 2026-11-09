package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
)

// The job: read log lines like this one (lesson 18's format) and add up the
// time spent on each path.
//
//	ts=2026-03-14T10:00:00Z user=ada path=/orders status=200 ms=45
//
// Lines without a non-empty path= and an all-digits ms= are skipped.
//
// There are four versions, each making one change from the last. They must
// all give exactly the same answers, which FuzzVersionsAgree checks. A fast
// wrong answer isn't an optimisation.

// PathStat is one row of the report.
type PathStat struct {
	Path    string `json:"path"`
	Count   int64  `json:"count"`
	TotalMS int64  `json:"total_ms"`
}

// Summariser is the shape all four versions share.
type Summariser func(r io.Reader) ([]PathStat, error)

// maxLine is the longest line that SummariseStream accepts.
const maxLine = 1 << 20

func sorted(stats []PathStat) []PathStat {
	slices.SortFunc(stats, func(a, b PathStat) int {
		if a.TotalMS != b.TotalMS {
			if a.TotalMS > b.TotalMS {
				return -1
			}
			return 1
		}
		return strings.Compare(a.Path, b.Path)
	})
	return stats
}

func fromMap(m map[string]PathStat) []PathStat {
	out := make([]PathStat, 0, len(m))
	for _, s := range m {
		out = append(out, s)
	}
	return sorted(out)
}

// splitLines mirrors what bufio.ScanLines does: split on \n and drop one \r.
func splitLines(data string) []string {
	lines := strings.Split(data, "\n")
	for i, line := range lines {
		lines[i] = strings.TrimSuffix(line, "\r")
	}
	return lines
}

// SummariseNaive is the obvious way to write it, especially coming from PHP.
//
// The trap: PHP caches compiled patterns, so preg_match in a loop is fine.
// Go doesn't. MustCompile parses the pattern from scratch on every call.
func SummariseNaive(r io.Reader) ([]PathStat, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	stats := map[string]PathStat{}
	for _, line := range splitLines(string(data)) {
		pathRe := regexp.MustCompile(`(?:^| )path=([^ ]+)`)
		msRe := regexp.MustCompile(`(?:^| )ms=([0-9]+)(?: |$)`)

		path := pathRe.FindStringSubmatch(line)
		ms := msRe.FindStringSubmatch(line)
		if path == nil || ms == nil {
			continue
		}
		n, err := strconv.ParseInt(ms[1], 10, 64)
		if err != nil {
			continue
		}
		key := fmt.Sprintf("%s", path[1])
		s := stats[key]
		s.Path = key
		s.Count++
		s.TotalMS += n
		stats[key] = s
	}
	return fromMap(stats), nil
}

// Step 1: compile each pattern once. A two-line change.
var (
	pathRe = regexp.MustCompile(`(?:^| )path=([^ ]+)`)
	msRe   = regexp.MustCompile(`(?:^| )ms=([0-9]+)(?: |$)`)
)

func SummariseCompiledOnce(r io.Reader) ([]PathStat, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	stats := map[string]PathStat{}
	for _, line := range splitLines(string(data)) {
		path := pathRe.FindStringSubmatch(line)
		ms := msRe.FindStringSubmatch(line)
		if path == nil || ms == nil {
			continue
		}
		n, err := strconv.ParseInt(ms[1], 10, 64)
		if err != nil {
			continue
		}
		s := stats[path[1]]
		s.Path = path[1]
		s.Count++
		s.TotalMS += n
		stats[path[1]] = s
	}
	return fromMap(stats), nil
}

// Step 2: the format is simple, so regular expressions aren't needed.
func SummariseNoRegexp(r io.Reader) ([]PathStat, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return nil, err
	}
	stats := map[string]PathStat{}
	for _, line := range splitLines(string(data)) {
		var path, msText string
		for _, field := range strings.Split(line, " ") {
			if path == "" && strings.HasPrefix(field, "path=") {
				path = field[len("path="):]
			}
			if msText == "" && strings.HasPrefix(field, "ms=") && isDigits(field[len("ms="):]) {
				msText = field[len("ms="):]
			}
		}
		if path == "" || msText == "" {
			continue
		}
		n, err := strconv.ParseInt(msText, 10, 64)
		if err != nil {
			continue
		}
		s := stats[path]
		s.Path = path
		s.Count++
		s.TotalMS += n
		stats[path] = s
	}
	return fromMap(stats), nil
}

func isDigits[T string | []byte](s T) bool {
	if len(s) == 0 {
		return false
	}
	for i := range len(s) {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// Step 3: stop allocating per line, and stop holding the file in memory.
//
// Three changes do it:
//   - bufio.Scanner reuses one buffer, so memory no longer grows with the file
//   - parseLine works on sub-slices of that buffer, copying nothing
//   - a map of pointers needs one lookup per line, not a lookup and a store
//
// It's also the least readable version. That's the usual trade, and it's why
// you only do this to code that a profile has shown is worth it.
func SummariseStream(r io.Reader) ([]PathStat, error) {
	stats := make(map[string]*PathStat, 64)

	sc := bufio.NewScanner(r)
	// The default limit is 64 KB, and a longer line stops the scan with an error.
	sc.Buffer(make([]byte, 0, 64*1024), maxLine)

	for sc.Scan() {
		path, ms, ok := parseLine(sc.Bytes())
		if !ok {
			continue
		}
		// Converting bytes to a string normally copies them. Here the compiler
		// can see that the key doesn't outlive the lookup, so it doesn't.
		s := stats[string(path)]
		if s == nil {
			// Only here do we copy: sc.Bytes() is overwritten by the next Scan.
			s = &PathStat{Path: string(path)}
			stats[s.Path] = s
		}
		s.Count++
		s.TotalMS += ms
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}

	out := make([]PathStat, 0, len(stats))
	for _, s := range stats {
		out = append(out, *s)
	}
	return sorted(out), nil
}

var (
	space   = []byte(" ")
	pathKey = []byte("path=")
	msKey   = []byte("ms=")
)

func parseLine(line []byte) (path []byte, ms int64, ok bool) {
	var msText []byte
	for more := true; more; {
		var field []byte
		field, line, more = bytes.Cut(line, space)
		if path == nil && bytes.HasPrefix(field, pathKey) && len(field) > len(pathKey) {
			path = field[len(pathKey):]
		}
		if msText == nil && bytes.HasPrefix(field, msKey) && isDigits(field[len(msKey):]) {
			msText = field[len(msKey):]
		}
	}
	if path == nil || msText == nil {
		return nil, 0, false
	}

	for _, c := range msText {
		d := int64(c - '0')
		if ms > (math.MaxInt64-d)/10 {
			return nil, 0, false // too big, the same outcome as ParseInt failing
		}
		ms = ms*10 + d
	}
	return path, ms, true
}
