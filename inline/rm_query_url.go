package inline

import (
	"bytes"
	"csust-got/util/urlx"
)

var removeAllQueryDomains = []string{
	"zhihu.com",
	"www.zhihu.com",
}

func writeClearAllQuery(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	u.Query = ""
	_, err := buf.WriteString(u.StringByFields())
	return err
}
