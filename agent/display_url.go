package agentv3

import (
	"net"
	"net/url"
	"regexp"
	"strings"
	"unicode"
)

var displayURLPattern = regexp.MustCompile("(?i)https?://[^\\s<>\"'`“”‘’]+")

// maskDisplayURLs only changes the final host label, retaining the URL's original representation.
func maskDisplayURLs(text string) string {
	return displayURLPattern.ReplaceAllStringFunc(text, func(candidate string) string {
		raw := strings.TrimRight(candidate, "),;!}]，。；！？）】》」』")
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Host == "" || strings.HasPrefix(parsed.Host, "[") {
			return candidate
		}
		host := parsed.Hostname()
		domain := strings.TrimSuffix(host, ".")
		if net.ParseIP(domain) != nil || !strings.Contains(domain, ".") {
			return candidate
		}
		for _, label := range strings.Split(domain, ".") {
			if label == "" || strings.HasPrefix(label, "-") || strings.HasSuffix(label, "-") {
				return candidate
			}
			for _, r := range label {
				if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) && r != '-' && r != '*' {
					return candidate
				}
			}
		}
		labelStart := strings.LastIndexByte(domain, '.') + 1
		label := []rune(domain[labelStart:])
		if len(label) < 2 {
			return candidate
		}
		authorityStart := strings.Index(raw, "://") + 3
		authorityEnd := len(raw)
		if end := strings.IndexAny(raw[authorityStart:], "/?#"); end >= 0 {
			authorityEnd = authorityStart + end
		}
		hostStart := authorityStart + strings.LastIndexByte(raw[authorityStart:authorityEnd], '@') + 1
		if raw[hostStart:authorityEnd] != parsed.Host {
			return candidate
		}
		start := hostStart + labelStart
		end := hostStart + len(domain)
		return candidate[:start] + string(label[0]) + strings.Repeat("*", len(label)-1) + candidate[end:]
	})
}
