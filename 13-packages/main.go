// LESSON 13 — PACKAGES & MODULES: splitting code up, internal/, third-party code
//
// Run with:  go run ./13-packages
//
// Until now every lesson was one `package main` folder. Real projects split
// code into packages. This lesson's layout:
//
//	13-packages/
//	├── main.go              package main   (this file: the program)
//	├── greet/greet.go       package greet  (a reusable library package)
//	└── internal/slug/       package slug   (private to 13-packages/...)
//
// MODULE vs PACKAGE — the two words PHP devs mix up:
//
//	PACKAGE = one folder of .go files            ~ a PHP namespace
//	MODULE  = a tree of packages with a go.mod   ~ a Composer project
//
// This whole repo is ONE module. Look at go.mod in the repo root:
//
//	module github.com/phoenix1331/learn-go-from-php
//
// That line is the module path. Every package's import path is the module
// path + its folder, which is why the imports below look like URLs.
// (Compare composer.json's "autoload": {"psr-4": {"App\\": "app/"}}.)
package main

import (
	// Standard library packages: short paths, no download needed.
	"fmt"

	// THIRD-PARTY MODULE. Added with:
	//
	//	go get github.com/google/uuid      (like: composer require ramsey/uuid)
	//
	// That command did two things:
	//   - added a `require` line to go.mod          (like composer.json)
	//   - recorded checksums in go.sum              (like composer.lock, sort of)
	// go.sum holds hashes, not just versions. Go checks every download against
	// them, and against the public checksum database, so a tampered module
	// fails to build. Commit both files.
	//
	// There's no vendor/ folder by default: modules are cached once per machine
	// in $(go env GOMODCACHE) and shared by every project.
	"github.com/google/uuid"

	// OUR OWN PACKAGES: module path + folder.
	"github.com/phoenix1331/learn-go-from-php/13-packages/greet"
	"github.com/phoenix1331/learn-go-from-php/13-packages/internal/slug"
)

func main() {
	// --- Using another package -------------------------------------------
	// You always refer to things as packagename.Thing. The package name is the
	// last part of the import path (greet), not the folder above it.
	g := greet.New("", false) // empty greeting falls back to greet.DefaultGreeting
	fmt.Println(g.Greet("Darren"))

	loud := greet.New("Hiya", true)
	fmt.Println(loud.Greet("  ")) // blank names become "stranger"

	// Exported fields are readable and writable from here...
	loud.Greeting = "Oi"
	fmt.Println(loud.Greet("PHP"))

	// ...but unexported ones aren't. Uncomment either line to see the error:
	// loud.shout = false        // loud.shout undefined (cannot refer to unexported field shout)
	// greet.clean("x")          // undefined: greet.clean

	fmt.Println("greet.Greet has been called", greet.Calls(), "times")

	// --- Internal package -------------------------------------------------
	// Allowed because this file lives under 13-packages/, the parent of internal/.
	title := "Learn Go from PHP: Lesson 13!"
	fmt.Printf("slug of %q = %q\n", title, slug.Make(title, "-"))

	// --- Third-party package ----------------------------------------------
	id := uuid.New() // a random (version 4) UUID, like Str::uuid()
	fmt.Println("new job id:", id)

	parsed, err := uuid.Parse("not-a-uuid")
	if err != nil {
		fmt.Println("parse failed as expected:", err)
	} else {
		fmt.Println("parsed:", parsed)
	}

	// --- Import aliases ---------------------------------------------------
	// If two packages share a name, rename one at import time:
	//
	//	import (
	//	    mrand "math/rand/v2"
	//	    "crypto/rand"
	//	)
	//
	// Like PHP's `use Foo\Bar as Baz;`. Use it only when you need to.
}

// USEFUL MODULE COMMANDS (run from the repo root):
//
//	go get github.com/x/y@v1.2.3   add or change a dependency version
//	go get github.com/x/y@none     remove a dependency
//	go mod tidy                    add missing / drop unused requirements
//	                               (run it after deleting imports)
//	go list -m all                 list every module in the build
//	go mod why github.com/x/y      explain why a module is needed
//	go doc ./13-packages/greet     read a package's docs
//
// THINGS TO TRY:
//   - Copy the slug import into 11-http-server/main.go and run `go build ./...`
//     to see the "use of internal package ... not allowed" error. Undo it after.
//   - Delete the uuid lines above, run `go mod tidy`, and watch go.mod drop
//     the requirement. Put them back and run `go mod tidy` again.
//   - Add a second file to greet/ (say farewell.go, also `package greet`).
//     It can use callCount and clean() directly: unexported means
//     package-private, not file-private.
