// LESSON 10 — A REAL CLI TOOL: flags, JSON, stdin/stdout, exit codes
//
// This is the "Go binary called from PHP" pattern we talked about.
//
// Try it:
//
//	go run ./10-json-cli --name=Darren --count=3
//	go run ./10-json-cli --name=Darren --count=3 --pretty
//	echo '{"items":["b","a","c"]}' | go run ./10-json-cli --stdin
//	go run ./10-json-cli --count=-1 ; echo "exit code: $?"
//	go run ./10-json-cli --help
//
// Build it, then call it from PHP:
//
//	go build -o bin/jsoncli ./10-json-cli
//	$result = Process::run([base_path('bin/jsoncli'), '--name=Darren']);
//	$data = json_decode($result->output(), true);
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"slices"
	"time"
)

// Struct tags tell encoding/json what the keys should be called.
// `omitempty` leaves the key out when the value is empty/zero.
type Output struct {
	Greeting    string    `json:"greeting"`
	Numbers     []int     `json:"numbers"`
	SortedItems []string  `json:"sorted_items,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

// Input we accept on stdin, e.g. {"items":["b","a"]}.
type Input struct {
	Items []string `json:"items"`
}

func main() {
	// flag defines command-line options. Each returns a POINTER, which
	// gets filled in when flag.Parse() runs. `--help` is generated for free.
	name := flag.String("name", "world", "who to greet")
	count := flag.Int("count", 5, "how many numbers to generate")
	pretty := flag.Bool("pretty", false, "pretty-print the JSON")
	useStdin := flag.Bool("stdin", false, "read JSON input from stdin")
	flag.Parse()

	// Validate input. Errors go to STDERR with a non-zero exit code,
	// so the calling PHP code can check $result->successful().
	if *count < 0 {
		fmt.Fprintln(os.Stderr, "error: --count must be 0 or more")
		os.Exit(1)
	}

	out := Output{
		Greeting:    "Hello, " + *name,
		Numbers:     make([]int, 0, *count),
		GeneratedAt: time.Now().UTC(),
	}
	for i := 1; i <= *count; i++ {
		out.Numbers = append(out.Numbers, i*i)
	}

	if *useStdin {
		var in Input
		// Decode JSON from stdin into the struct (like json_decode into an object).
		if err := json.NewDecoder(os.Stdin).Decode(&in); err != nil {
			fmt.Fprintln(os.Stderr, "error: invalid JSON on stdin:", err)
			os.Exit(2)
		}
		slices.Sort(in.Items)
		out.SortedItems = in.Items
	}

	// Encode the struct to JSON on stdout (like echo json_encode($out)).
	enc := json.NewEncoder(os.Stdout)
	if *pretty {
		enc.SetIndent("", "  ")
	}
	if err := enc.Encode(out); err != nil {
		fmt.Fprintln(os.Stderr, "error: encoding output:", err)
		os.Exit(1)
	}
	// Returning from main normally = exit code 0 (success).
}
