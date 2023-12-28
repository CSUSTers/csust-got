package urlx

import "testing"

func TestUrlRegexMatch(t *testing.T) {
	cases := []struct {
		text string
		want bool
	}{
		{"https://example.com", true},
		{"http://example.com", true},
		{"example.com", true},
		{"example.com:8080", true},
		{"example.com:8080/echo", true},
		{"example.com/echo", true},
		{"example.com/echo?q=hello", true},
		{"example.com/echo?q=hello#world", true},
		{"example.com/echo?q=hello#world&foo=bar", true},
		{"https://example.com", true},
		{"https://example.com:8080", true},
		{"https://example.com:8080/echo", true},
		{"https://example.com/echo", true},
		{"https://example.com/echo?q=hello", true},
		{"https://example.com/echo?q=hello#world", true},
		{"https://example.com/echo?q=hello#world&foo=bar", true},
	}

	for _, c := range cases {
		if got := Patt.MatchString(c.text); got != c.want {
			t.Errorf("Patt.MatchString(%q) = %v, want %v", c.text, got, c.want)
		}
	}
}
