package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_writeFxTwitterUrl(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "Valid twitter URL",
			url:  "https://twitter.com/nocatsnolife_m/status/1743271045698924555?s=123&t=ABCD-EFGH",
			want: "https://fxtwitter.com/nocatsnolife_m/status/1743271045698924555",
		},
		{
			name: "Valid x URL",
			url:  "https://x.com/nocatsnolife_m/status/1743271045698924555?s=123&t=ABCD-EFGH",
			want: "https://fxtwitter.com/nocatsnolife_m/status/1743271045698924555",
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			err := twitterProcessor.writeUrl(buf, u.Url)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeFxTwitterUrl() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				assert.Equal(t, tt.want, buf.String())
			}
		})
	}
}
