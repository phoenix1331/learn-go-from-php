// LESSON 05 — STRUCTS & METHODS: Go's answer to classes
//
// Run with:  go run ./05-structs-methods
//
// Go has no classes, no `extends`, no constructors. Instead:
//   - a struct holds data (like a class's properties)
//   - methods are functions attached to a type
//   - embedding gives you composition instead of inheritance
package main

import (
	"fmt"
	"strings"
)

// VISIBILITY: Go uses capitalisation instead of public/private.
//
//	Uppercase name (User, Email)  -> exported, visible to other packages
//	lowercase name (password)     -> unexported, private to this package
type User struct {
	Name     string
	Email    string
	Age      int
	password string
}

// "Constructor" — just a normal function, named New<Type> by convention.
// It returns a pointer (*User) so callers share one User instead of copies.
func NewUser(name, email string, age int) *User {
	return &User{
		Name:  name,
		Email: strings.ToLower(email),
		Age:   age,
	}
}

// METHOD with a VALUE receiver `(u User)`.
// The method gets a COPY of the struct — fine for reading.
// `u` plays the role of PHP's $this.
func (u User) Greeting() string {
	return "Hi, I'm " + u.Name
}

// METHOD with a POINTER receiver `(u *User)`.
// Needed whenever the method MODIFIES the struct (otherwise you'd
// modify a copy and the change would vanish). Also avoids copying big structs.
// Rule of thumb: if any method needs a pointer receiver, use pointers for all.
func (u *User) Birthday() {
	u.Age++
}

func (u *User) SetPassword(p string) {
	u.password = p // private field, only settable through this method
}

// ---------------------------------------------------------------
// EMBEDDING — composition instead of inheritance
// ---------------------------------------------------------------

type Timestamps struct {
	CreatedAt string
	UpdatedAt string
}

func (t Timestamps) Describe() string {
	return "created " + t.CreatedAt
}

// Admin embeds User and Timestamps (no field name, just the type).
// All their fields and methods are "promoted", so admin.Name and
// admin.Greeting() work directly. A bit like a PHP trait.
type Admin struct {
	*User
	Timestamps
	Permissions []string
}

// Admin can define its own Greeting, which "shadows" User's.
func (a Admin) Greeting() string {
	return "Hello, I'm admin " + a.Name
}

// STRUCT TAGS: metadata in backticks, read by other packages.
// The json package uses them to map field names (see lesson 10).
type Product struct {
	ID    int     `json:"id"`
	Title string  `json:"title"`
	Price float64 `json:"price"`
}

func main() {
	// Creating structs.
	u1 := User{Name: "Ada", Email: "ada@example.com", Age: 36} // value
	u2 := NewUser("Linus", "LINUS@Example.com", 54)            // pointer

	fmt.Println(u1.Greeting())
	fmt.Println(u2.Greeting(), "-", u2.Email)

	// Go automatically takes &u1 when calling a pointer method on a
	// variable, so this works even though u1 isn't a pointer.
	u1.Birthday()
	fmt.Println("Ada is now", u1.Age)

	// %+v prints field names; very handy when debugging (like var_dump).
	u2.SetPassword("secret")
	fmt.Printf("%+v\n", *u2)

	// VALUE vs POINTER semantics.
	copyOfU1 := u1 // structs are COPIED on assignment
	copyOfU1.Name = "Changed"
	fmt.Println("u1 unaffected:", u1.Name)

	samePerson := u2 // copying a POINTER = both point to the same User
	samePerson.Name = "Linus T."
	fmt.Println("u2 affected:", u2.Name)

	// Embedding in action.
	admin := Admin{
		User:        NewUser("Grace", "grace@example.com", 45),
		Timestamps:  Timestamps{CreatedAt: "2026-01-01"},
		Permissions: []string{"users.edit", "billing.view"},
	}
	fmt.Println(admin.Name)            // promoted field from User
	fmt.Println(admin.Greeting())      // Admin's own version
	fmt.Println(admin.User.Greeting()) // explicitly call the embedded one
	fmt.Println(admin.Describe())      // promoted from Timestamps
	admin.Birthday()                   // promoted pointer method
	fmt.Println("admin age:", admin.Age)

	// Anonymous struct — a one-off shape without declaring a type.
	// Common in tests and for quick JSON payloads.
	point := struct {
		X, Y int
	}{X: 3, Y: 4}
	fmt.Println("point:", point)

	p := Product{ID: 1, Title: "Keyboard", Price: 49.99}
	fmt.Printf("%+v\n", p)
}
