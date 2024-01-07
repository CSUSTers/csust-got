package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"regexp"
)

func init() {
	registerUrlProcessor(newClearAllQueryProcessor(`^(?:.*\.)?(zhihu|jd)\.com$`))
}

// clearAllQueryProcessor 清除 query 部分的所有参数
type clearAllQueryProcessor struct {
	reg *regexp.Regexp
}

func newClearAllQueryProcessor(regex string) urlProcessor {
	return &clearAllQueryProcessor{
		reg: regexp.MustCompile(regex),
	}
}

func (c *clearAllQueryProcessor) needProcess(u *urlx.Extra) bool {
	return c.reg.MatchString(u.Url.Domain)
}

func (c *clearAllQueryProcessor) writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	u.Query = ""
	_, err := buf.WriteString(u.StringByFields())
	return err
}
