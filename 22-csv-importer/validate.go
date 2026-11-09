package main

import (
	"errors"
	"fmt"
	"net/mail"
	"slices"
	"strings"
	"time"
	"unicode/utf8"
)

// ErrBadHeader means the file itself is wrong, as opposed to some rows in it.
var ErrBadHeader = errors.New("bad header")

// Customer is a row that passed validation, ready for the database.
type Customer struct {
	Email    string
	Name     string
	Country  string
	Plan     string
	SignedUp string
}

// columns says where each field is in this particular file. People reorder
// spreadsheet columns, so never rely on position.
type columns struct {
	email, name, country, plan, signedUp int
	count                                int
}

// parseHeader finds the columns by name and ignores any it doesn't know.
func parseHeader(header []string) (columns, error) {
	found := make(map[string]int, len(header))
	for i, name := range header {
		name = strings.ToLower(strings.TrimSpace(name))
		if _, dup := found[name]; dup {
			return columns{}, fmt.Errorf("%w: column %q appears twice", ErrBadHeader, name)
		}
		found[name] = i
	}

	// Report every missing column at once (the idea from lesson 18).
	var problems []string
	for _, name := range []string{"email", "name", "country", "signed_up"} {
		if _, ok := found[name]; !ok {
			problems = append(problems, name)
		}
	}
	if len(problems) > 0 {
		slices.Sort(problems)
		return columns{}, fmt.Errorf("%w: missing column(s): %s", ErrBadHeader, strings.Join(problems, ", "))
	}

	plan, ok := found["plan"]
	if !ok {
		plan = -1 // optional
	}
	return columns{
		email: found["email"], name: found["name"], country: found["country"],
		signedUp: found["signed_up"], plan: plan, count: len(header),
	}, nil
}

// validate checks one row and returns every problem with it, like a Laravel
// FormRequest. A row with one mistake reported at a time is a row that
// someone has to fix and upload several times over.
func validate(c columns, record []string) (Customer, []string) {
	if len(record) != c.count {
		return Customer{}, []string{fmt.Sprintf("expected %d columns, got %d", c.count, len(record))}
	}
	for _, field := range record {
		if !utf8.ValidString(field) {
			return Customer{}, []string{"not valid UTF-8 (save the file as CSV UTF-8)"}
		}
	}

	var problems []string
	customer := Customer{
		Email:    strings.ToLower(strings.TrimSpace(record[c.email])),
		Name:     strings.TrimSpace(record[c.name]),
		Country:  strings.ToUpper(strings.TrimSpace(record[c.country])),
		Plan:     "free",
		SignedUp: strings.TrimSpace(record[c.signedUp]),
	}
	if c.plan >= 0 {
		if plan := strings.ToLower(strings.TrimSpace(record[c.plan])); plan != "" {
			customer.Plan = plan
		}
	}

	// ParseAddress also accepts "Ada <ada@example.com>", so check that what
	// it found is the whole value.
	if addr, err := mail.ParseAddress(customer.Email); err != nil || addr.Address != customer.Email {
		problems = append(problems, "email is not valid")
	}
	if customer.Name == "" {
		problems = append(problems, "name is required")
	} else if utf8.RuneCountInString(customer.Name) > 255 {
		problems = append(problems, "name is longer than 255 characters")
	}
	if !isCountryCode(customer.Country) {
		problems = append(problems, "country must be a 2-letter code")
	}
	switch customer.Plan {
	case "free", "pro", "team":
	default:
		problems = append(problems, "plan must be free, pro or team")
	}
	if _, err := time.Parse(time.DateOnly, customer.SignedUp); err != nil {
		problems = append(problems, "signed_up must be a real date as YYYY-MM-DD")
	}

	if problems != nil {
		return Customer{}, problems
	}
	return customer, nil
}

func isCountryCode(s string) bool {
	return len(s) == 2 && s[0] >= 'A' && s[0] <= 'Z' && s[1] >= 'A' && s[1] <= 'Z'
}
