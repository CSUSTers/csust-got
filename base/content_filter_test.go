package base

import (
	"net/url"
	"reflect"
	"testing"
)

func Test_findUrls(t *testing.T) {
	tests := []struct {
		args string
		want []string
	}{
		{
			"12345677544",
			[]string{},
		},
		{
			"12345677544 https://www.baidu.com 3214324564523",
			[]string{"https://www.baidu.com"},
		}, {
			"12345677544 www.baidu.com说得对 https://www.google.com",
			[]string{"www.baidu.com", "https://www.google.com"},
		},
		{
			"sapphire rapid 也感觉没https://www.bilibili.com/video/av697839032有压倒性对 epyc 那边的优势 https://t.me/Loidenatub/1047 ",
			[]string{"https://www.bilibili.com/video/av697839032%E6%9C%89%E5%8E%8B%E5%80%92%E6%80%A7%E5%AF%B9", "https://t.me/Loidenatub/1047"},
		},
		{
			"这是什么，make，-j8 https://www.baidu.com \n这是什么，make，-j8\n这是什么，make，-j8\n这 t.me是什么，make，-j8 https://www.google.com\n这是什么，make，-j8\n这是什么，make，-j8",
			[]string{"https://www.baidu.com", "t.me", "https://www.google.com"},
		},
	}
	for _, tt := range tests {
		got, _ := findUrls(tt.args)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("findUrls() = %v, want %v", got, tt.want)
		}
	}
}

func Test_getOriginalURL(t *testing.T) {
	tests := []struct {
		args    string
		want    string
		wantErr bool
	}{
		{
			"https://b23.tv/mpnbvw2",
			"https://www.bilibili.com/video/BV1es4y1d7Gu/",
			false,
		},
		{
			"https://b23.tv/av697839032",
			"https://www.bilibili.com/video/BV14m4y1y7U8/",
			false,
		},
		{
			"https://b23.tv/BV1es4y1d7Gu",
			"https://www.bilibili.com/video/BV1es4y1d7Gu/",
			false,
		},
		{
			"",
			"",
			true,
		},
	}
	for _, tt := range tests {
		got, err := getOriginalURL(tt.args)
		if (err != nil) != tt.wantErr {
			t.Errorf("getOriginalURL() error = %v, wantErr %v", err, tt.wantErr)
			return
		}
		if got != tt.want {
			t.Errorf("getOriginalURL() got = %v, want %v", got, tt.want)
		}
	}
}

func TestBilibiliHandler(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr bool
	}{
		{
			"bilibili.com Test",
			"https://www.bilibili.com/video/BV14m4y1y7U8/",
			"https://www.bilibili.com/video/av697839032",
			false,
		},
		{
			"b23.tv Test 1",
			"https://b23.tv/BV1es4y1d7Gu",
			"https://www.bilibili.com/video/av995285358",
			false,
		},
		{
			"b23.tv Test 2",
			"https://b23.tv/av697839032",
			"https://www.bilibili.com/video/av697839032",
			false,
		},
		{
			"b23.tv Test 3",
			"https://b23.tv/mpnbvw2",
			"https://www.bilibili.com/video/av995285358",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			urlTest, _ := url.Parse(tt.args)
			got, err := bilibiliHandler(urlTest)
			if (err != nil) != tt.wantErr {
				t.Errorf("UrlConverter() error = %v, wantErr %v", err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("UrlConverter() got = \n %v \n, want: \n %v \n", got, tt.want)
			}
		})
	}
}
