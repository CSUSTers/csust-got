package base

import (
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

func TestUrlConverter(t *testing.T) {
	tests := []struct {
		name    string
		args    string
		want    string
		wantErr bool
	}{
		{
			"bilibili.com Test",
			"假设你的新游戏对光影有极高的要求，目前最方便的解法就是光追 https://www.bilibili.com/video/BV14m4y1y7U8/ 3214324564523",
			"https://www.bilibili.com/video/av697839032\n",
			false,
		},
		{
			"b23.tv Test 1",
			"光追的情况下甚至得重新开发一套特效 https://b23.tv/BV1es4y1d7Gu 不止是手机，这种情况甚至包括NS和SteamDeck",
			"https://www.bilibili.com/video/av995285358\n",
			false,
		},
		{
			"b23.tv Test 2",
			"光追的情况下甚至得重新开发一套特效 https://b23.tv/av697839032 不止是手机，这种情况甚至包括NS和SteamDeck",
			"https://www.bilibili.com/video/av697839032\n",
			false,
		},
		{
			"b23.tv Test 3",
			"光追的情况下甚至得重新开发一套特效 https://b23.tv/mpnbvw2 不止是手机，这种情况甚至包括NS和SteamDeck",
			"https://www.bilibili.com/video/av995285358\n",
			false,
		},
		{
			"b23.tv Test 4",
			"光追的情况下甚至得重新开发一套特效 不止是手机，这种情况甚至包括NS和SteamDeck",
			"",
			false,
		},
		{
			"b23.tv Test 5",
			"光追的情况下甚至得重新开发一套特效 https://t.me/CE_Observe 不止是手机，这种情况甚至包括NS和SteamDeck",
			"",
			false,
		},
		{
			"b23.tv Test 6",
			"光追的情况下甚 https://www.bilibili.com/video/BV19L411e7bt/ 至得 https://www.bilibili.com/video/BV1es4y1d7Gu?buvid=YE4F98ED85781C8D4B4D8FE2F4FDDC3A2016&amp;is_story_h5=false&amp;mid=o3R3GNkGkdjbo4WgkXVZlw%3D%3D&amp;p=1&amp;plat_id=116&amp;share_from=ugc&amp;share_medium=iphone&amp;share_plat=ios&amp;share_session_id=83CF26A4-186E-4B3D-A060-52B81F7811C9&amp;share_source=COPY&amp;share_tag=s_i&amp;timestamp=1682132738&amp;unique_k=mpnbvw2&amp;up_id=514347794 重新 https://b23.tv/BV19L411D7hr 开发一套特效 https://b23.tv/mpnbvw2 不止是手机，这种情况 https://b23.tv/ZAUoajf 甚至包括NS和SteamDeck",
			"https://www.bilibili.com/video/av442654976" +
				"\n" + "https://www.bilibili.com/video/av995285358" +
				"\n" + "https://www.bilibili.com/video/av441555975" +
				"\n" + "https://www.bilibili.com/video/av995285358" +
				"\n" + "https://www.bilibili.com/video/av697661975" + "\n",
			false,
		},
		{
			"twitter Test",
			"光追的情况下甚至得重新开发一套特效 https://twitter.com/Gunjou_row/status/1640669724865617920/ 不止是手机，这种情况甚至包括NS和SteamDeck",
			"https://fxtwitter.com/Gunjou_row/status/1640669724865617920/\n",
			false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := urlConverter(tt.args)
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
