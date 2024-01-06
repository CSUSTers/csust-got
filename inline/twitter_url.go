package inline

import (
	"bytes"
	"csust-got/util/urlx"
)

func init() {
	registerRegexUrlProcessConfig(`^(?:www\.)?(twitter|x)\.com$`, writeFxTwitterUrl)
}

func writeFxTwitterUrl(buf *bytes.Buffer, u *urlx.ExtraUrl) error {
	u.Query = ""
	u.Domain = "fxtwitter.com"
	_, err := buf.WriteString(u.StringByFields())
	return err
}
