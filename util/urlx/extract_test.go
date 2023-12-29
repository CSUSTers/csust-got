package urlx

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractStr(t *testing.T) {
	tests := []struct {
		name   string
		text   string
		extras []*Extra
	}{
		{
			name: "Test case 1",
			text: "This is a test https://example.com",
			extras: []*Extra{
				{
					Type: Plain,
					Text: "This is a test ",
				},
				{
					Type: Url,
					Text: "https://example.com",
					Url: &ExtraUrl{
						Text:   "https://example.com",
						Schema: "https",
						Domain: "example.com",
						Tld:    "com",
						Port:   "",
						Path:   "",
						Query:  "",
						Hash:   "",
					},
				},
			},
		},
		{
			name: "Test case 2",
			text: `This is a test http://example.com/echo?foo=bar 测试
测试 example.com?abc=def#this?hash 测试`,
			extras: []*Extra{
				{
					Type: Plain,
					Text: "This is a test ",
				},
				{
					Type: Url,
					Text: "http://example.com/echo?foo=bar",
					Url: &ExtraUrl{
						Text:   "http://example.com/echo?foo=bar",
						Schema: "http",
						Domain: "example.com",
						Tld:    "com",
						Port:   "",
						Path:   "/echo",
						Query:  "?foo=bar",
						Hash:   "",
					},
				},
				{
					Type: Plain,
					Text: " 测试\n测试 ",
				},
				{
					Type: Url,
					Text: "example.com?abc=def#this?hash",
					Url: &ExtraUrl{
						Text:   "example.com?abc=def#this?hash",
						Schema: "",
						Domain: "example.com",
						Tld:    "com",
						Port:   "",
						Path:   "",
						Query:  "?abc=def",
						Hash:   "#this?hash",
					},
				},
				{
					Type: Plain,
					Text: " 测试",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			extras := ExtractStr(tt.text)
			assert.Equal(t, tt.extras, extras)
		})
	}
}
