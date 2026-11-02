package slug

import "testing"

func TestMake(t *testing.T) {
	tests := []struct {
		in, sep, want string
	}{
		{"Hello, World!", "-", "hello-world"},
		{"  Learn   Go from PHP  ", "-", "learn-go-from-php"},
		{"Crème brûlée", "_", "crème_brûlée"},
		{"Lesson 13", "-", "lesson-13"},
		{"!!!", "-", ""},
	}
	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			if got := Make(tt.in, tt.sep); got != tt.want {
				t.Errorf("Make(%q, %q) = %q, want %q", tt.in, tt.sep, got, tt.want)
			}
		})
	}
}
