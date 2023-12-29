package urlx

import (
	"fmt"
	"regexp"
	"strings"
)

const regexTempl = `(?mi)\b(?:(?P<schema>https?)://)?(?:(?P<domain>(?:[\w\d~-]+\.)+(?P<tld>(?:%[1]s)))(?:\:(?P<port>\d{1,5}))?(?P<path>(?:/[^\s\?&:()$!]*)*))(?P<query>\?(?:[^\s()^$!]*))?`

// Patt is alias to [`UrlPatt`]
var Patt = regexp.MustCompile(fmt.Sprintf(regexTempl, strings.Join(TLDs, "|")))

var UrlPatt = Patt

func init() {
	UrlPatt.Longest()
}
