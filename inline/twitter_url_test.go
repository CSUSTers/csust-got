package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
		{
			name: "fixupx URL keeps domain",
			url:  "https://fixupx.com/nocatsnolife_m/status/1743271045698924555?s=123",
			want: "https://fixupx.com/nocatsnolife_m/status/1743271045698924555",
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

func Test_twitterTranslatedUrl(t *testing.T) {
	tests := []struct {
		name   string
		url    string
		want   string
		wantOk bool
	}{
		{
			name:   "x status",
			url:    "https://x.com/nocatsnolife_m/status/1743271045698924555?s=123&t=ABCD-EFGH",
			want:   "https://fxtwitter.com/nocatsnolife_m/status/1743271045698924555/zh",
			wantOk: true,
		},
		{
			name:   "twitter status with www",
			url:    "https://www.twitter.com/a_b/status/1",
			want:   "https://fxtwitter.com/a_b/status/1/zh",
			wantOk: true,
		},
		{
			name:   "mobile x status",
			url:    "https://mobile.x.com/a/status/1",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "fxtwitter status",
			url:    "https://fxtwitter.com/a/status/1",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "fixupx status",
			url:    "https://fixupx.com/a/status/1?s=20",
			want:   "https://fixupx.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "twittpr status",
			url:    "https://twittpr.com/a/status/1",
			want:   "https://twittpr.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "fxtwitter subdomain status",
			url:    "https://d.fxtwitter.com/a/status/1",
			want:   "https://d.fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "status with trailing slash",
			url:    "https://x.com/a/status/1/",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "status with existing language",
			url:    "https://fxtwitter.com/a/status/1/en",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "status already zh",
			url:    "https://fxtwitter.com/a/status/1/zh",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "status photo",
			url:    "https://x.com/a/status/1/photo/1",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name:   "status with hash",
			url:    "https://x.com/a/status/1#m",
			want:   "https://fxtwitter.com/a/status/1/zh",
			wantOk: true,
		},
		{
			name: "profile",
			url:  "https://x.com/a",
		},
		{
			name: "non-numeric status id",
			url:  "https://x.com/a/status/abc",
		},
		{
			name: "home",
			url:  "https://x.com/home",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			exs := urlx.ExtractStr(tt.url)
			require.Len(t, exs, 1)
			require.True(t, twitterProcessor.needProcess(exs[0]))
			got, ok := twitterProcessor.translatedUrl(exs[0].Url)
			assert.Equal(t, tt.wantOk, ok)
			assert.Equal(t, tt.want, got)
		})
	}
}
