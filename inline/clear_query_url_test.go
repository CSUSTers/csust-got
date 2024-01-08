package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestClearAllQueryProcessor(t *testing.T) {
	tests := []struct {
		name        string
		regex       string
		url         string
		needProcess bool
		want        string
		wantErr     bool
	}{
		{
			name:        "Clear query from zhihu.com URL",
			regex:       `^(?:.*\.)?zhihu\.com$`,
			url:         "https://www.zhihu.com/question/34923126?sort=created",
			needProcess: true,
			want:        "https://www.zhihu.com/question/34923126",
		},
		{
			name:        "Clear query from jd URL with multiple queries",
			regex:       `^(?:.*\.)?jd\.com$`,
			url:         "https://item.jd.com/100008348542.html?dist=jd",
			needProcess: true,
			want:        "https://item.jd.com/100008348542.html",
		},
		{
			name:        "Clear query from zhihu.com URL with multiple queries and hash",
			regex:       `^(?:.*\.)?zhihu\.com$`,
			url:         "https://www.zhihu.com/question/34923126?sort=created&page=2#hash",
			needProcess: true,
			want:        "https://www.zhihu.com/question/34923126#hash",
		},
		{
			name:        "Clear query from jd URL with only hash",
			regex:       `^(?:.*\.)?jd\.com$`,
			url:         "https://item.jd.com/100008348542.html#hash",
			needProcess: true,
			want:        "https://item.jd.com/100008348542.html#hash",
		},
		{
			name:        "Clear query from zhihu.com URL without query",
			regex:       `^(?:.*\.)?zhihu\.com$`,
			url:         "https://www.zhihu.com/question/34923126",
			needProcess: true,
			want:        "https://www.zhihu.com/question/34923126",
		},
		{
			name:        "needn't process URL",
			regex:       `^(?:.*\.)?zhihu\.com$`,
			url:         "https://www.baidu.com",
			needProcess: false,
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			proc := newClearAllQueryProcessor(tt.regex)
			needProcess := proc.needProcess(u)
			assert.Equal(t, tt.needProcess, needProcess)
			if !needProcess {
				return
			}
			err := proc.writeUrl(buf, u.Url)
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
