package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"net/url"
	"sort"
	"strings"
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

func Test_clearQueryWithKeepParams(t *testing.T) {
	tests := []struct {
		name       string
		url        string
		keepParams []string
		want       string
		wantErr    bool
	}{
		{
			name:       "Clear query but keep id param",
			url:        "https://www.taobao.com/product?id=12345&sort=price",
			keepParams: []string{"id"},
			want:       "https://www.taobao.com/product?id=12345",
		},
		{
			name:       "Clear query but keep multiple params",
			url:        "https://www.taobao.com/product?id=12345&sort=price&color=red",
			keepParams: []string{"id", "color"},
			want:       "https://www.taobao.com/product?id=12345&color=red",
		},
		{
			name:       "Clear query with no params to keep",
			url:        "https://www.taobao.com/product?id=12345&sort=price",
			keepParams: []string{},
			want:       "https://www.taobao.com/product",
		},
		{
			name:       "Clear query with non-existing params to keep",
			url:        "https://www.taobao.com/product?id=12345&sort=price",
			keepParams: []string{"nonexistent"},
			want:       "https://www.taobao.com/product",
		},
	}

	buf := bytes.NewBufferString("")
	for _, tt := range tests {
		buf.Reset()
		t.Run(tt.name, func(t *testing.T) {
			u := urlx.ExtractStr(tt.url)[0]
			clearFunc := clearQueryWithKeepParams(tt.keepParams...)
			err := clearFunc(buf, u.Url)
			if (err != nil) != tt.wantErr {
				t.Errorf("clearQueryWithKeepParams() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if err == nil {
				assert.True(t, compareURLs(tt.want, buf.String()))
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
