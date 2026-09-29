package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"regexp"
)

const (
	fxTwitterDomain        = "fxtwitter.com"
	twitterTranslationLang = "zh"
)

var (
	twitterDomainPatt     = regexp.MustCompile(`(?i)^(?:(?:www|mobile)\.)?(?:twitter|x)\.com$`)
	fxTwitterDomainPatt   = regexp.MustCompile(`(?i)^(?:[a-z0-9-]+\.)?(?:fxtwitter|fixupx|twittpr)\.com$`)
	twitterStatusPathPatt = regexp.MustCompile(`(?i)^/(\w+)/status/(\d+)(?:/.*)?$`)
	twitterProcessor      = newFixTwitterProcessor()
)

var _ translatedUrlProcessor = twitterProcessor

func init() {
	registerUrlProcessor(twitterProcessor)
}

// fixTwitterProcessor 清除 query 部分的所有参数, 并将 twitter/x 域名替换为 fxtwitter.com,
// 对推文链接额外提供 fxtwitter 的 /zh 翻译链接
type fixTwitterProcessor struct{}

func newFixTwitterProcessor() *fixTwitterProcessor {
	return &fixTwitterProcessor{}
}

func (c *fixTwitterProcessor) needProcess(u *urlx.Extra) bool {
	return twitterDomainPatt.MatchString(u.Url.Domain) || fxTwitterDomainPatt.MatchString(u.Url.Domain)
}

func (c *fixTwitterProcessor) fixUrl(u *urlx.ExtraUrl) urlx.ExtraUrl {
	fixed := *u
	fixed.Query = ""
	if twitterDomainPatt.MatchString(fixed.Domain) {
		fixed.Domain = fxTwitterDomain
	}
	return fixed
}

func (c *fixTwitterProcessor) writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	fixed := c.fixUrl(u)
	_, err := buf.WriteString(fixed.StringByFields())
	return err
}

func (c *fixTwitterProcessor) translatedUrl(u *urlx.ExtraUrl) (string, bool) {
	m := twitterStatusPathPatt.FindStringSubmatch(u.Path)
	if m == nil {
		return "", false
	}
	fixed := c.fixUrl(u)
	fixed.Path = "/" + m[1] + "/status/" + m[2] + "/" + twitterTranslationLang
	fixed.Hash = ""
	return fixed.StringByFields(), true
}
