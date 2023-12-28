package urlx

import (
	"fmt"
	"regexp"
	"strings"
)

const regexTempl = `(?mi)((?P<schema>https?)://)?(?:(?P<domain>(?:[\w\d-]+\.)+(?<tld>(?:%[1]s)))(?:\:(?P<port>\d{1,5}))?(?P<path>(?:/[^\s?&:]*)*))(?P<query>\?(?:\S*))?`

var PATT = regexp.MustCompile(fmt.Sprintf(regexTempl, "("+strings.Join(TLDs, "|")+")"))
