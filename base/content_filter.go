package base

import (
	"csust-got/config"
	"csust-got/log"
	"errors"
	bg "github.com/iyear/biligo"
	"go.uber.org/zap"
	"golang.org/x/exp/slices"
	"gopkg.in/telebot.v3"
	"io"
	"mvdan.cc/xurls/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var (
	errNoUrlsFound       = errors.New("no URLs found in the input string")
	errRetrieveVideoInfo = errors.New("failed to retrieve video info")
)

// findUrls returns all urls from input string
func findUrls(text string) ([]string, error) {
	rxRelaxed := xurls.Relaxed()
	matches := rxRelaxed.FindAllString(text, -1)
	urls := make([]string, 0, len(matches))
	for _, match := range matches {
		parsedUrl, err := url.Parse(match)
		if err != nil {
			return nil, err
		}
		urls = append(urls, parsedUrl.String())
	}

	return urls, nil
}

// handleSwitch forwarding url to corresponding handler
func handleSwitch(urlStr string, uid string) (string, error) {
	parsedUrl, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	switch parsedUrl.Host {
	// bv转av，去除跟踪链接
	case "www.bilibili.com", "bilibili.com", "b23.tv":
		// 如果uid不在启用列表中，不做处理
		enabledList := config.BotConfig.ContentFilterConfig.UrlFilterConfig.Bv2av.EnabledUserList
		log.Debug("bv enabled list", zap.Strings("list", enabledList))
		if slices.Contains(enabledList, uid) {
			return bilibiliHandler(parsedUrl)
		}
	case "twitter.com":
		enabledList := config.BotConfig.ContentFilterConfig.UrlFilterConfig.Tw2fx.EnabledUserList
		log.Debug("tw enabled list", zap.Strings("list", enabledList))
		if slices.Contains(enabledList, uid) {
			return twitterHandler(parsedUrl)
		}
	}

	return "", nil
}

// twitterHandler handles twitter urls，remove tracking params，replace twitter.com with fxtwitter.com
func twitterHandler(twitterUrl *url.URL) (string, error) {
	twitterUrl.Host = "fxtwitter.com"
	twitterUrl.RawQuery = ""
	return twitterUrl.String(), nil
}

// bilibiliHandler handles bilibili urls
func bilibiliHandler(biliUrl *url.URL) (string, error) {
	// 如果host是b23.tv， 获取原始地址
	if biliUrl.Host == "b23.tv" {
		originalURL, err := getOriginalURL(biliUrl.String())
		if err != nil {
			return "", err
		}
		biliUrl, err = url.Parse(originalURL)
		if err != nil {
			return "", err
		}
	}
	// 如果路径中包含bv号，则转换为av号
	if strings.Contains(biliUrl.Path, "BV") {
		bv := strings.TrimPrefix(biliUrl.Path, "/video/")
		av := bg.BV2AV(bv)
		biliUrl.Path = "/video/av" + strconv.FormatInt(av, 10)
		biliUrl.RawQuery = ""
	}
	return biliUrl.String(), nil
}

func getOriginalURL(shortURL string) (string, error) {
	// 添加一些ua头
	client := &http.Client{}

	req, err := http.NewRequest("GET", shortURL, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("User-Agent", "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/237.84.2.178 Safari/537.36")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}

	if resp.StatusCode != http.StatusOK {
		return "", errRetrieveVideoInfo
	}

	bodyBytes, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	body := string(bodyBytes)

	re := regexp.MustCompile(`href="(https?://.*?)">`)
	matches := re.FindStringSubmatch(body)
	if len(matches) < 2 {
		return "", errNoUrlsFound
	}

	originalURL := matches[1]
	return originalURL, nil
}

func urlConverter(text string, uid string) (string, error) {
	urls, err := findUrls(text)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", nil
	}
	text = ""
	for _, oneUrl := range urls {
		urlStr, err := handleSwitch(oneUrl, uid)
		if err != nil {
			return "", err
		}
		if urlStr != "" {
			text += urlStr + "\n"
		}
	}
	return text, nil
}

// UrlFilter get all urls in text to new urls
func UrlFilter(ctx telebot.Context, text string) {
	if !config.BotConfig.ContentFilterConfig.UrlFilterConfig.Enabled {
		return
	}
	msgSendBack, err := urlConverter(text, strconv.FormatInt(ctx.Message().Sender.ID, 10))
	if err != nil {
		log.Error("[Content filter] UrlConverter error: %v", zap.Error(err))
	}
	if msgSendBack != "" {
		err = ctx.Reply(msgSendBack)
		if err != nil {
			log.Error("[Content filter] Reply error: %v", zap.Error(err))
		}
	}
}
