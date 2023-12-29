package urlx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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

func TestUrlRegexSubmatch(t *testing.T) {
	t.Run("test-1", func(t *testing.T) {
		text := `
abc https://example.com
测试 http://example.com/echo 测试
测试 example.com?abc=def 测试
`

		ms := Patt.FindAllStringSubmatchIndex(text, -1)
		if len(ms) != 3 {
			t.Errorf("len(matches) = %d, want %d", len(ms), 3)
		}
		s1 := text[ms[0][0]:ms[0][1]]
		s2 := text[ms[1][0]:ms[1][1]]
		s3 := text[ms[2][0]:ms[2][1]]
		t.Log(ms)
		assert.Equal(t, "https://example.com", s1)
		assert.Equal(t, "http://example.com/echo", s2)
		assert.Equal(t, "example.com?abc=def", s3)
	})
}
