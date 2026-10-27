// LESSON 06 — INTERFACES: behaviour without inheritance
//
// Run with:  go run ./06-interfaces
//
// The big difference from PHP: there is no `implements` keyword.
// If a type has all the methods an interface asks for, it satisfies that
// interface automatically ("implicit implementation", or duck typing that
// the compiler checks for you).
package main

import (
	"fmt"
	"math"
)

// An interface is a list of method signatures.
// Convention: small interfaces, often one method, named "<verb>er".
type Shape interface {
	Area() float64
	Perimeter() float64
}

type Rectangle struct {
	Width, Height float64
}

func (r Rectangle) Area() float64      { return r.Width * r.Height }
func (r Rectangle) Perimeter() float64 { return 2 * (r.Width + r.Height) }

type Circle struct {
	Radius float64
}

func (c Circle) Area() float64      { return math.Pi * c.Radius * c.Radius }
func (c Circle) Perimeter() float64 { return 2 * math.Pi * c.Radius }

// Neither Rectangle nor Circle mentions Shape anywhere, yet both ARE Shapes,
// because both have Area() and Perimeter().

// This function accepts ANY Shape — that's polymorphism in Go.
func describe(s Shape) {
	fmt.Printf("%T: area=%.2f perimeter=%.2f\n", s, s.Area(), s.Perimeter())
}

// ---------------------------------------------------------------
// fmt.Stringer — a built-in interface you'll meet constantly
// ---------------------------------------------------------------
// The fmt package checks for this interface:
//
//	type Stringer interface { String() string }
//
// Implement it and fmt.Println uses it (like PHP's __toString).
func (c Circle) String() string {
	return fmt.Sprintf("Circle(r=%.1f)", c.Radius)
}

// ---------------------------------------------------------------
// Interfaces for swappable dependencies (very common in real apps)
// ---------------------------------------------------------------
type Notifier interface {
	Send(to, msg string) error
}

type EmailNotifier struct{}

func (EmailNotifier) Send(to, msg string) error {
	fmt.Println("[email] to", to+":", msg)
	return nil
}

type SMSNotifier struct{ From string }

func (s SMSNotifier) Send(to, msg string) error {
	fmt.Println("[sms from", s.From+"] to", to+":", msg)
	return nil
}

// Depends on the interface, not a concrete type, so it's easy to swap
// implementations or pass a fake one in tests (like Laravel contracts).
func notifyAll(n Notifier, users []string) {
	for _, u := range users {
		n.Send(u, "Your report is ready")
	}
}

func main() {
	shapes := []Shape{
		Rectangle{Width: 3, Height: 4},
		Circle{Radius: 2},
	}
	for _, s := range shapes {
		describe(s)
	}

	fmt.Println(Circle{Radius: 1.5}) // uses our String() method

	notifyAll(EmailNotifier{}, []string{"ada@example.com"})
	notifyAll(SMSNotifier{From: "MyApp"}, []string{"+447700900000"})

	// ---------------------------------------------------------------
	// TYPE ASSERTIONS — get the concrete type back out of an interface
	// ---------------------------------------------------------------
	var s Shape = Circle{Radius: 3}

	// "comma ok" form: safe, ok is false if it's not a Circle.
	if c, ok := s.(Circle); ok {
		fmt.Println("it's a circle with radius", c.Radius)
	}
	// Without ok, a wrong assertion PANICS:  r := s.(Rectangle)

	// ---------------------------------------------------------------
	// THE EMPTY INTERFACE: `any`
	// ---------------------------------------------------------------
	// `any` (alias for interface{}) has no methods, so EVERY type satisfies
	// it. It's the closest thing to PHP's "mixed". Use sparingly — you lose
	// compile-time type safety.
	things := []any{42, "hello", 3.14, true, Rectangle{1, 2}, nil}

	// A TYPE SWITCH branches on the concrete type.
	for _, t := range things {
		switch v := t.(type) {
		case int:
			fmt.Println("int, doubled:", v*2)
		case string:
			fmt.Println("string of length", len(v))
		case Shape:
			fmt.Println("a shape with area", v.Area())
		case nil:
			fmt.Println("nil value")
		default:
			fmt.Printf("something else: %T\n", v)
		}
	}
}
