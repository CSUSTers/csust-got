package urlx

import (
	"net/url"
	"reflect"
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
					Type: TypePlain,
					Text: "This is a test ",
				},
				{
					Type: TypeUrl,
					Text: "https://example.com",
					Url: &ExtraUrl{
						Text:   "https://example.com",
						Scheme: "https",
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
					Type: TypePlain,
					Text: "This is a test ",
				},
				{
					Type: TypeUrl,
					Text: "http://example.com/echo?foo=bar",
					Url: &ExtraUrl{
						Text:   "http://example.com/echo?foo=bar",
						Scheme: "http",
						Domain: "example.com",
						Tld:    "com",
						Port:   "",
						Path:   "/echo",
						Query:  "?foo=bar",
						Hash:   "",
					},
				},
				{
					Type: TypePlain,
					Text: " 测试\n测试 ",
				},
				{
					Type: TypeUrl,
					Text: "example.com?abc=def#this?hash",
					Url: &ExtraUrl{
						Text:   "example.com?abc=def#this?hash",
						Scheme: "",
						Domain: "example.com",
						Tld:    "com",
						Port:   "",
						Path:   "",
						Query:  "?abc=def",
						Hash:   "#this?hash",
					},
				},
				{
					Type: TypePlain,
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

func TestUrlToExtraUrl(t *testing.T) {
	tests := []struct {
		name string
		url  string
		want *ExtraUrl
	}{
		{
			name: "url test",
			url:  "https://example.com/echo?q=query#hello",
			want: &ExtraUrl{
				Text:   "https://example.com/echo?q=query#hello",
				Scheme: "https",
				Domain: "example.com",
				Tld:    "com",
				Port:   "",
				Path:   "/echo",
				Query:  "?q=query",
				Hash:   "#hello",
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, err := url.Parse(tt.url)
			if err != nil {
				t.Fatalf("error wheme parse test case url to `url.Url`: %v", err)
			}
			if got := UrlToExtraUrl(u); !reflect.DeepEqual(got, tt.want) {
				t.Errorf("UrlToExtraUrl() = %v, want %v", got, tt.want)
			}
		})
	}
}
