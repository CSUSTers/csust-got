package inline

import (
	"bytes"
	"csust-got/util/urlx"
)

func init() {
	registerRegexUrlProcessConfig(`^(?:.*\.)?(zhihu|jd)\.com$`, writeClearAllQuery)
	registerRegexUrlProcessConfig(`^(?:.*\.)?taobao\.com$`, clearQueryWithKeepParams("id"))
	registerRegexUrlProcessConfig(`^(?:.*\.)?tb\.cn$`, clearQueryWithKeepParams("id"))
}

// writeClearAllQuery 清除 query 部分的所有参数
func writeClearAllQuery(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	u.Query = ""
	_, err := buf.WriteString(u.StringByFields())
	return err
}

// clearQueryWithKeepParams 清除 query 部分的所有参数，但保留指定的参数
func clearQueryWithKeepParams(keepParams ...string) writeUrlFunc {
	return func(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
		q, err := filterParamFromQuery(u.Query, keepParams...)
		if err != nil {
			return err
		}
		u.Query = q
		_, err = buf.WriteString(u.StringByFields())
		return err
	}
}
