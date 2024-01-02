package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_writeUrl(t *testing.T) {
	tests := []struct {
		name      string
		url       string
		want      string
		shouldErr bool
	}{
		{
			name:      "bilibili url",
			url:       "https://bilibili.com/video/avnotav",
			want:      "https://bilibili.com/video/avnotav",
			shouldErr: false,
		},
		{
			name:      "bilibili url with query",
			url:       "https://bilibili.com/video/avnotav?query=1&p=2&trace=j923n9f2h&t=45.6",
			want:      "https://bilibili.com/video/avnotav?p=2&t=45.6",
			shouldErr: false,
		},
	}
	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			es := urlx.ExtractStr(tt.url)
			assert.Equal(t, 1, len(es), "extracted url count should be 1")
			e := es[0]
			err := writeUrl(buf, e)
			if (err != nil) != tt.shouldErr {
				t.Errorf("writeUrl() error = %v, wantErr %v", err, tt.shouldErr)
			} else if err == nil {
				assert.Equal(t, tt.want, buf.String())
			}
		})
	}
}

func Test_writeAll(t *testing.T) {
	tests := []struct {
		name      string
		input     string
		want      string
		shouldErr bool
	}{
		{
			name:  "single URL",
			input: "https://example.com",
			want:  "https://example.com",
		},
		{
			name:  "multiple URLs",
			input: `https://example.com?q=123 https://bilibili.com?trace=456 https://example.com?t=789`,
			want:  `https://example.com?q=123 https://bilibili.com https://example.com?t=789`,
		},
		{
			name:  "no URL",
			input: "no url",
			want:  "no url",
		},
		{
			name:  "empty string",
			input: "",
			want:  "",
		},
		{
			name:  "URL in text",
			input: "here is a URL https://example.com in text, and another on next line \nhttps://bilibili.com/dynamic?from=homepage",
			want:  "here is a URL https://example.com in text, and another on next line \nhttps://bilibili.com/dynamic",
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			err := writeAll(buf, urlx.ExtractStr(tt.input))
			if (err != nil) != tt.shouldErr {
				t.Errorf("writeAll() error = %v, wantErr %v", err, tt.shouldErr)
			} else if err == nil {
				assert.Equal(t, tt.want, buf.String())
			}
		})
	}
}
