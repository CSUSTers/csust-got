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
	https://example.com/echo1/echo2?text1=hello#here?is=world"
`

		ms := Patt.FindAllStringSubmatchIndex(text, -1)
		t.Log(ms)
		if len(ms) != 4 {
			t.Errorf("len(matches) = %d, want %d", len(ms), 4)
		}

		s1 := text[ms[0][2]:ms[0][3]]
		s2 := text[ms[1][2]:ms[1][3]]
		s3 := text[ms[2][2]:ms[2][3]]
		s4 := text[ms[3][2]:ms[3][3]]

		assert.Equal(t, "https://example.com", s1)
		assert.Equal(t, "http://example.com/echo", s2)
		assert.Equal(t, "example.com?abc=def", s3)
		assert.Equal(t, "https://example.com/echo1/echo2?text1=hello#here?is=world", s4)
	})

	t.Run("test-2", func(t *testing.T) {
		text := `
测试https://example.com (不匹配)
test+http://example.com/echo (不匹配)
^example.com?abc=def (不匹配)
`

		ms := Patt.FindAllStringSubmatchIndex(text, -1)
		if len(ms) != 0 {
			t.Errorf("len(matches) = %d, want %d", len(ms), 0)
		}
	})

	t.Run("test-3", func(t *testing.T) {
		text := `
测试命名捕获组 https://example.com
测试命名捕获组 http://example.com/echo 测试
测试命名捕获组 example.com?abc=def 测试
测试命名捕获组 example.com:8080 测试
测试命名捕获组 https://example.com/echo?text1=hello&text2=#here
测试命名捕获组 https://example.com/echo?text1=hello&text2=#here?foo=bar
`

		ms := Patt.FindAllStringSubmatchIndex(text, -1)
		t.Log(ms)
		if len(ms) != 6 {
			t.Errorf("len(matches) = %d, want %d", len(ms), 6)
		}

		m1 := ms[0]
		m2 := ms[1]
		m3 := ms[2]
		m4 := ms[3]
		m5 := ms[4]
		m6 := ms[5]

		urlIdx := Patt.SubexpIndex("url")
		schemaIdx := Patt.SubexpIndex("schema")
		domainIdx := Patt.SubexpIndex("domain")
		tldIdx := Patt.SubexpIndex("tld")
		portIdx := Patt.SubexpIndex("port")
		pathIdx := Patt.SubexpIndex("path")
		queryIdx := Patt.SubexpIndex("query")
		hashIdx := Patt.SubexpIndex("hash")

		t.Log(urlIdx, schemaIdx, domainIdx, tldIdx, portIdx, pathIdx, queryIdx, hashIdx)

		// match 1
		assert.Equal(t, "https://example.com", SubmatchGroupString(text, m1, urlIdx))
		assert.Equal(t, "https", SubmatchGroupString(text, m1, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m1, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m1, tldIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m1, portIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m1, pathIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m1, queryIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m1, hashIdx))

		// match 2
		assert.Equal(t, "http://example.com/echo", SubmatchGroupString(text, m2, urlIdx))
		assert.Equal(t, "http", SubmatchGroupString(text, m2, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m2, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m2, tldIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m2, portIdx))
		assert.Equal(t, "/echo", SubmatchGroupString(text, m2, pathIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m2, queryIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m2, hashIdx))

		// match 3
		assert.Equal(t, "example.com?abc=def", SubmatchGroupString(text, m3, urlIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m3, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m3, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m3, tldIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m3, portIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m3, pathIdx))
		assert.Equal(t, "?abc=def", SubmatchGroupString(text, m3, queryIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m3, hashIdx))

		// match 4
		assert.Equal(t, "example.com:8080", SubmatchGroupString(text, m4, urlIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m4, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m4, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m4, tldIdx))
		assert.Equal(t, "8080", SubmatchGroupString(text, m4, portIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m4, pathIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m4, queryIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m4, hashIdx))

		// match 5
		assert.Equal(t, "https://example.com/echo?text1=hello&text2=#here", SubmatchGroupString(text, m5, urlIdx))
		assert.Equal(t, "https", SubmatchGroupString(text, m5, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m5, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m5, tldIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m5, portIdx))
		assert.Equal(t, "/echo", SubmatchGroupString(text, m5, pathIdx))
		assert.Equal(t, "?text1=hello&text2=", SubmatchGroupString(text, m5, queryIdx))
		assert.Equal(t, "#here", SubmatchGroupString(text, m5, hashIdx))

		// match 6
		assert.Equal(t, "https://example.com/echo?text1=hello&text2=#here?foo=bar", SubmatchGroupString(text, m6, urlIdx))
		assert.Equal(t, "https", SubmatchGroupString(text, m6, schemaIdx))
		assert.Equal(t, "example.com", SubmatchGroupString(text, m6, domainIdx))
		assert.Equal(t, "com", SubmatchGroupString(text, m6, tldIdx))
		assert.Equal(t, "", SubmatchGroupString(text, m6, portIdx))
		assert.Equal(t, "/echo", SubmatchGroupString(text, m6, pathIdx))
		assert.Equal(t, "?text1=hello&text2=", SubmatchGroupString(text, m6, queryIdx))
		assert.Equal(t, "#here?foo=bar", SubmatchGroupString(text, m6, hashIdx))
	})
}
