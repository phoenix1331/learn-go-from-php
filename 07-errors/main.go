// LESSON 07 — ERRORS: values, wrapping, custom errors, panic/recover
//
// Run with:  go run ./07-errors
//
// Go has no try/catch for everyday failures. A function that can fail
// returns an `error` as its last value, and the caller checks it.
// `error` is just a built-in interface:
//
//	type error interface { Error() string }
package main

import (
	"errors"
	"fmt"
	"os"
	"strconv"
)

// SENTINEL ERRORS: predefined error values callers can compare against.
// Named Err<Something> by convention.
var ErrNotFound = errors.New("not found")
var ErrUnauthorised = errors.New("unauthorised")

// CUSTOM ERROR TYPE: any type with an Error() string method is an error.
// Use one when you need to carry extra data (like a PHP exception subclass).
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string {
	return e.Field + ": " + e.Reason
}

func findUser(id int) (string, error) {
	if id == 1 {
		return "Ada", nil
	}
	return "", ErrNotFound
}

func validateAge(input string) (int, error) {
	age, err := strconv.Atoi(input) // string -> int; returns an error if invalid
	if err != nil {
		return 0, &ValidationError{Field: "age", Reason: "must be a number"}
	}
	if age < 0 {
		return 0, &ValidationError{Field: "age", Reason: "cannot be negative"}
	}
	return age, nil
}

// WRAPPING: add context as an error travels up, using fmt.Errorf with %w.
// The original error is kept inside, so callers can still inspect it.
func loadProfile(id int) (string, error) {
	name, err := findUser(id)
	if err != nil {
		return "", fmt.Errorf("loadProfile(%d): %w", id, err)
	}
	return name + "'s profile", nil
}

// PANIC is for truly unexpected, unrecoverable bugs (like an index out of
// range). Don't use it for normal errors. RECOVER, called inside a deferred
// function, can catch a panic — this is how web servers avoid crashing on
// a single bad request.
func safeDivide(a, b int) (result int, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("recovered from panic: %v", r)
		}
	}()
	return a / b, nil // dividing ints by zero panics
}

func main() {
	// 1. The standard pattern.
	name, err := findUser(1)
	if err != nil {
		fmt.Println("error:", err)
		return
	}
	fmt.Println("found:", name)

	// 2. Wrapped errors read like a stack of context.
	_, err = loadProfile(42)
	fmt.Println("error:", err) // loadProfile(42): not found

	// errors.Is looks through the wrapping chain for a specific value.
	// Never compare with == when errors may be wrapped.
	if errors.Is(err, ErrNotFound) {
		fmt.Println("-> treat as a 404")
	}

	// 3. errors.As finds a specific error TYPE in the chain and gives you
	//    access to its fields (like `catch (ValidationException $e)`).
	_, err = validateAge("abc")
	var vErr *ValidationError
	if errors.As(err, &vErr) {
		fmt.Println("validation failed on field:", vErr.Field, "-", vErr.Reason)
	}

	if age, err := validateAge("30"); err == nil {
		fmt.Println("valid age:", age)
	}

	// 4. Real-world example: reading a file that doesn't exist.
	_, err = os.ReadFile("does-not-exist.txt")
	if errors.Is(err, os.ErrNotExist) {
		fmt.Println("file missing:", err)
	}

	// 5. Panic + recover.
	_, err = safeDivide(10, 0)
	fmt.Println("safeDivide:", err)

	// 6. Combine several errors into one (useful for validating many fields).
	err = errors.Join(
		&ValidationError{Field: "email", Reason: "required"},
		&ValidationError{Field: "name", Reason: "too short"},
	)
	fmt.Println("joined errors:\n" + err.Error())
}
