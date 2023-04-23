package base

import (
	"csust-got/log"
	"errors"
	bg "github.com/iyear/biligo"
	"go.uber.org/zap"
	"gopkg.in/telebot.v3"
	"io"
	"mvdan.cc/xurls/v2"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

var errNoUrlsFound = errors.New("no URLs found in the input string")
var errRetrieveVideoInfo = errors.New("failed to retrieve video info")

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
func handleSwitch(urlStr string) (string, error) {
	parsedUrl, err := url.Parse(urlStr)
	if err != nil {
		return "", err
	}
	switch parsedUrl.Host {
	// bv转av，去除跟踪链接
	case "www.bilibili.com", "bilibili.com", "b23.tv":
		return bilibiliHandler(parsedUrl)
	case "twitter.com":
		return twitterHandler(parsedUrl)
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
	resp, err := http.Get(shortURL)
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

func urlConverter(text string) (string, error) {
	urls, err := findUrls(text)
	if err != nil {
		return "", err
	}
	if len(urls) == 0 {
		return "", nil
	}
	text = ""
	for _, oneUrl := range urls {
		urlStr, err := handleSwitch(oneUrl)
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
func UrlFilter(ctx telebot.Context, text string) error {
	msgSendBack, err := urlConverter(text)
	if err != nil {
		log.Error("[Content filter] UrlConverter error: %v", zap.Error(err))
	}
	if msgSendBack != "" {
		err = ctx.Reply(msgSendBack)
		if err != nil {
			log.Error("[Content filter] Reply error: %v", zap.Error(err))
		}
	}
	return err
}
