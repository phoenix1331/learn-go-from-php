//go:build race

package main

// raceEnabled says whether this is a -race build. A build tag on these two
// small files is the standard way for code to find out.
const raceEnabled = true
