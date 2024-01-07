package inline

import (
	"bytes"
	"csust-got/util/urlx"
	"regexp"
)

func init() {
	registerUrlProcessor(
		newRetainQueryProcessor(`^(?:.*\.)?taobao\.com$`, "id"),
		newRetainQueryProcessor(`^(?:.*\.)?tb\.cn$`, "id"),
		newRetainQueryProcessor(`^(?:www\.)?(?:cn\.)?bing\.com$`, "q"),
		newRetainQueryProcessor(`^(?:www\.)?google\.com$`, "q"),
	)
}

// retainQueryProcessor 清除 query 部分的所有参数，保留指定的 query 参数
type retainQueryProcessor struct {
	reg        *regexp.Regexp
	keepParams []string
}

func newRetainQueryProcessor(regex string, keepParams ...string) urlProcessor {
	return &retainQueryProcessor{
		reg:        regexp.MustCompile(regex),
		keepParams: keepParams,
	}
}

func (r *retainQueryProcessor) needProcess(u *urlx.Extra) bool {
	return r.reg.MatchString(u.Url.Domain)
}

func (r *retainQueryProcessor) writeUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	q, err := filterParamFromQuery(u.Query, r.keepParams...)
	if err != nil {
		return err
	}
	u.Query = q
	_, err = buf.WriteString(u.StringByFields())
	return err
}
