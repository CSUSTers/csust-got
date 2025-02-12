package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_writeBiliUrl(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "`b23.tv` shorten URL",
			url:  "https://b23.tv/F6HmLCU",
			want: "https://b23.tv/BV1hD4y1X7Rm",
		},
		{
			name: "`b23.tv` shorten URL with http",
			url:  "http://b23.tv/F6HmLCU",
			want: "https://b23.tv/BV1hD4y1X7Rm",
		},
		{
			name: "`b23.tv` shorten URL without http/https",
			url:  "b23.tv/F6HmLCU",
			want: "https://b23.tv/BV1hD4y1X7Rm",
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			err := bProcessor.writeUrl(buf, u.Url)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeBiliUrl() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				assert.Equal(t, tt.want, buf.String())
			}
		})
	}
}
