// Package greet is a small library package used by lesson 13.
//
// A PACKAGE is a folder of .go files that all start with the same
// `package <name>` line. That's the whole unit of code organisation in Go:
// no namespaces, no `use` statements per class, no autoloader.
//
//	PHP:  namespace App\Greet;  ...  use App\Greet\Greeter;
//	Go:   package greet         ...  import ".../13-packages/greet"
//
// The comment directly above `package` is the package's documentation.
// Try:  go doc ./13-packages/greet
package greet

import (
	"fmt"
	"strings"
)

// VISIBILITY IS DECIDED BY THE FIRST LETTER — there are no public/private
// keywords.
//
//	Uppercase first letter  -> EXPORTED (like `public`), usable from other packages
//	lowercase first letter  -> unexported (like `private`, but per PACKAGE, not per class)
//
// This applies to everything: functions, types, struct fields, constants, variables.

// DefaultGreeting is exported, so main can read it as greet.DefaultGreeting.
const DefaultGreeting = "Hello"

// callCount is unexported: only code inside package greet can touch it.
// Any file in this folder can use it, though: "private" is package-wide.
var callCount int

// Greeter builds greetings. The type and the Greeting field are exported;
// the shout field is not, so other packages can't set it directly.
type Greeter struct {
	Greeting string
	shout    bool
}

// New is the conventional constructor name when a package has one main type.
// Callers write greet.New(...), which reads nicely. (Compare: new Greeter(...).)
func New(greeting string, shout bool) *Greeter {
	if greeting == "" {
		greeting = DefaultGreeting
	}
	return &Greeter{Greeting: greeting, shout: shout}
}

// Greet is an exported method.
func (g *Greeter) Greet(name string) string {
	callCount++
	msg := fmt.Sprintf("%s, %s!", g.Greeting, clean(name))
	if g.shout {
		msg = strings.ToUpper(msg)
	}
	return msg
}

// Calls exposes the unexported counter read-only, like a PHP getter.
func Calls() int {
	return callCount
}

// clean is an unexported helper. main cannot call greet.clean(...):
// the compiler reports "undefined: greet.clean".
func clean(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "stranger"
	}
	return name
}
