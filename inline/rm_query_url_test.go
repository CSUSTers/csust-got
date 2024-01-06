package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
)

func Test_writeClearAllQuery(t *testing.T) {
	tests := []struct {
		name    string
		url     string
		want    string
		wantErr bool
	}{
		{
			name: "Clear query from zhihu.com URL",
			url:  "https://www.zhihu.com/question/34923126?sort=created",
			want: "https://www.zhihu.com/question/34923126",
		},
		{
			name: "Clear query from zhihu.com URL with multiple queries",
			url:  "https://www.zhihu.com/question/34923126?sort=created&page=2",
			want: "https://www.zhihu.com/question/34923126",
		},
		{
			name: "Clear query from zhihu.com URL with multiple queries and hash",
			url:  "https://www.zhihu.com/question/34923126?sort=created&page=2#hash",
			want: "https://www.zhihu.com/question/34923126#hash",
		},
		{
			name: "Clear query from zhihu.com URL with only hash",
			url:  "https://www.zhihu.com/question/34923126#hash",
			want: "https://www.zhihu.com/question/34923126#hash",
		},
		{
			name: "Clear query from zhihu.com URL without query",
			url:  "https://www.zhihu.com/question/34923126",
			want: "https://www.zhihu.com/question/34923126",
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			err := writeClearAllQuery(buf, u.Url)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeClearAllQuery() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				assert.Equal(t, tt.want, buf.String())
			}
		})
	}
}
