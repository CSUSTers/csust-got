package urlx

import (
	"fmt"
	"regexp"
	"strings"
)

const regexTempl = `(?mi)(?:^|\s)(?P<url>(?:(?P<schema>https?)://)?(?:(?P<domain>(?:[\w\d~-]+\.)+(?P<tld>(?:%[1]s)))(?:\:(?P<port>\d{1,5}))?(?P<path>(?:/[^\s\?&:()$!"'#]*)*))(?P<query>\?(?:[^\s()^$!"'#]*))?(?P<hash>#(?:[^\s()^$!"']*))?)`

// Patt is alias to [`UrlPatt`]
var Patt = regexp.MustCompile(fmt.Sprintf(regexTempl, TLDRegex))

var UrlPatt = Patt

func SubmatchGroupString(s string, matchIndexes []int, groupIdx int) string {
	if groupIdx < 0 || groupIdx >= len(matchIndexes)/2 {
		return ""
	}
	if matchIndexes[groupIdx*2] < 0 || matchIndexes[groupIdx*2+1] < 0 {
		return ""
	}
	return s[matchIndexes[groupIdx*2]:matchIndexes[groupIdx*2+1]]
}

func SubmatchGroupStringByName(p *regexp.Regexp, s string, matchIndexes []int, groupName string) string {
	groupIdx := p.SubexpIndex(groupName)
	return SubmatchGroupString(s, matchIndexes, groupIdx)
}

func init() {
	UrlPatt.Longest()
}
