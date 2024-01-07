package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"regexp"
)

var (
	twitterProcessor = newFixTwitterProcessor(`^(?:www\.)?(twitter|x)\.com$`)
)

func init() {
	registerUrlProcessor(twitterProcessor)
}

// fixTwitterProcessor 清除 query 部分的所有参数, 并将域名替换为 fxtwitter.com
type fixTwitterProcessor struct {
	reg *regexp.Regexp
}

func newFixTwitterProcessor(regex string) urlProcessor {
	return &fixTwitterProcessor{
		reg: regexp.MustCompile(regex),
	}
}

func (c *fixTwitterProcessor) needProcess(u *urlx.Extra) bool {
	return c.reg.MatchString(u.Url.Domain)
}

func (c *fixTwitterProcessor) writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	u.Query = ""
	u.Domain = "fxtwitter.com"
	_, err := buf.WriteString(u.StringByFields())
	return err
}
