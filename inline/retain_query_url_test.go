package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"github.com/stretchr/testify/assert"
	"net/url"
	"sort"
	"strings"
	"testing"
)

func TestRetainQueryProcessor(t *testing.T) {
	tests := []struct {
		name        string
		regex       string
		keepParams  []string
		url         string
		needProcess bool
		want        string
		wantErr     bool
	}{
		{
			name:        "Retain id query from taobao.com URL",
			regex:       `^(?:.*\.)?taobao\.com$`,
			keepParams:  []string{"id"},
			url:         "https://www.taobao.com/product?id=123&sort=created",
			needProcess: true,
			want:        "https://www.taobao.com/product?id=123",
		},
		{
			name:        "No matching domain, no processing",
			regex:       `^(?:.*\.)?taobao\.com$`,
			keepParams:  []string{"id"},
			url:         "https://www.notmatching.com/product?id=123&sort=created",
			needProcess: false,
		},
		{
			name:        "No keep query param, clear all",
			regex:       `^(?:.*\.)?tb\.cn`,
			keepParams:  []string{},
			url:         "https://www.tb.cn/product?id=123&sort=created",
			needProcess: true,
			want:        "https://www.tb.cn/product",
		},
		{
			name:        "Retain multiple query params",
			regex:       `^(?:.*\.)?tb\.cn`,
			keepParams:  []string{"id", "sort"},
			url:         "https://www.tb.cn/product?id=123&sort=created",
			needProcess: true,
			want:        "https://www.tb.cn/product?id=123&sort=created",
		},
		{
			name:        "Retain multiple query params with hash",
			regex:       `^(?:.*\.)?tb\.cn`,
			keepParams:  []string{"id", "sort"},
			url:         "https://www.tb.cn/product?id=123&sort=created#hash",
			needProcess: true,
			want:        "https://www.tb.cn/product?id=123&sort=created#hash",
		},
		{
			name:        "needn't process URL",
			regex:       `^(?:.*\.)?tb\.cn`,
			keepParams:  []string{"id"},
			url:         "https://www.baidu.com",
			needProcess: false,
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			proc := newRetainQueryProcessor(tt.regex, tt.keepParams...)
			needProcess := proc.needProcess(u)
			assert.Equal(t, tt.needProcess, needProcess)
			if !needProcess {
				return
			}
			err := proc.writeUrl(buf, u.Url)
			if (err != nil) != tt.wantErr {
				t.Errorf("writeUrl() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				assert.True(t, compareURLs(tt.want, buf.String()),
					"name: %s, want: %s, got: %s", tt.name, tt.want, buf.String())
			}
		})
	}
}

// 比较查询参数，忽略顺序
func compareQueries(q1, q2 url.Values) bool {
	if len(q1) != len(q2) {
		return false
	}

	for key, values := range q1 {
		values2, ok := q2[key]
		if !ok {
			return false
		}

		sort.Strings(values)
		sort.Strings(values2)

		if strings.Join(values, " ") != strings.Join(values2, " ") {
			return false
		}
	}

	return true
}

// 比较两个URL是否一致，忽略查询参数的顺序
func compareURLs(url1, url2 string) bool {
	u1, err := url.Parse(url1)
	if err != nil {
		return false
	}

	u2, err := url.Parse(url2)
	if err != nil {
		return false
	}

	if u1.Scheme != u2.Scheme || u1.Host != u2.Host || u1.Path != u2.Path || u1.Fragment != u2.Fragment {
		return false
	}

	return compareQueries(u1.Query(), u2.Query())
}
