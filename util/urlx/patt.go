package urlx

import (
	"fmt"
	"regexp"
)

const regexTempl = `(?mi)(?:^|\s)(?P<url>(?:(?P<scheme>https?)://)?(?:(?P<domain>(?:[\w\d~-]+\.)+(?P<tld>(?:%[1]s)))(?:\:(?P<port>\d{1,5}))?(?P<path>(?:/[^\s\?&:()$!"'#]*)*))(?P<query>\?(?:[^\s()^$!"'#]*))?(?P<hash>#(?:[^\s()^$!"']*))?)`

// Patt is alias to [`UrlPatt`]
var Patt = UrlPatt

// UrlPatt matches all URLs
var UrlPatt = regexp.MustCompile(fmt.Sprintf(regexTempl, TLDRegex))

// UrlPattAscii matches URLs with ascii TLD
var UrlPattAscii = regexp.MustCompile(fmt.Sprintf(regexTempl, TLDAsciiRegex))

// SubmatchGroupString get group from regex matches indexes
func SubmatchGroupString(s string, matchIndexes []int, groupIdx int) string {
	if groupIdx < 0 || groupIdx >= len(matchIndexes)/2 {
		return ""
	}
	if matchIndexes[groupIdx*2] < 0 || matchIndexes[groupIdx*2+1] < 0 {
		return ""
	}
	return s[matchIndexes[groupIdx*2]:matchIndexes[groupIdx*2+1]]
}

// SubmatchGroupStringByName get group from regex matches indexes by group name
func SubmatchGroupStringByName(p *regexp.Regexp, s string, matchIndexes []int, groupName string) string {
	groupIdx := p.SubexpIndex(groupName)
	return SubmatchGroupString(s, matchIndexes, groupIdx)
}

func init() {
	UrlPatt.Longest()
}
