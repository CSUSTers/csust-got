package urlx

import (
	"fmt"
	"regexp"
	"strings"
)

const regexTempl = `(?mi)((?P<schema>https?)://)?(?:(?P<domain>(?:[\w\d-]+\.)+(?P<tld>(?:%[1]s)))(?:\:(?P<port>\d{1,5}))?(?P<path>(?:/[^\s?&:]*)*))(?P<query>\?(?:\S*))?`

var Patt = regexp.MustCompile(fmt.Sprintf(regexTempl, "("+strings.Join(TLDs, "|")+")"))
