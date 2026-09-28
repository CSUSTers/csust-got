package base

import (
	"net/url"
	"regexp"
	"sort"
	"strings"
	"unicode/utf16"

	tb "gopkg.in/telebot.v3"
)

var twitterURLPattern = regexp.MustCompile(`(?i)https?://[^\s<>"'\p{Z}]+`)

func canonicalTwitterURL(raw string) (string, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.User != nil || u.Opaque != "" || (!strings.EqualFold(u.Scheme, "http") && !strings.EqualFold(u.Scheme, "https")) {
		return "", false
	}
	escapedPath := strings.ToLower(u.EscapedPath())
	if !strings.EqualFold(u.Host, "x.com") && !strings.EqualFold(u.Host, "fxtwitter.com") ||
		u.RawPath != "" || strings.ContainsAny(u.Path, `\%`) || strings.Contains(escapedPath, "%2f") || strings.Contains(escapedPath, "%5c") {
		return "", false
	}
	parts := strings.Split(strings.TrimSuffix(u.Path, "/"), "/")
	if len(parts) == 5 && parts[4] == "zh" {
		parts = parts[:4]
	}
	if len(parts) != 4 || parts[0] != "" || parts[2] != "status" || parts[1] == "" || parts[3] == "" {
		return "", false
	}
	for _, r := range parts[1] {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' {
			continue
		}
		return "", false
	}
	for _, r := range parts[3] {
		if r < '0' || r > '9' {
			return "", false
		}
	}
	return "https://fxtwitter.com/" + parts[1] + "/status/" + parts[3] + "/zh", true
}

type twitterCandidate struct {
	position int
	url      string
}

func twitterCandidates(m *tb.Message) []string {
	if m == nil {
		return nil
	}
	text, entities := m.Text, m.Entities
	if text == "" {
		text, entities = m.Caption, m.CaptionEntities
	}
	if text == "" {
		return nil
	}
	var candidates []twitterCandidate
	for _, match := range twitterURLPattern.FindAllStringIndex(text, -1) {
		start, end := match[0], match[1]
		if start > 0 && (text[start-1] == '@' || text[start-1] == '/' || text[start-1] == '\\' || text[start-1] >= 'a' && text[start-1] <= 'z' || text[start-1] >= 'A' && text[start-1] <= 'Z') {
			continue
		}
		candidates = append(candidates, twitterCandidate{start, strings.TrimRight(text[start:end], ".,!?;:)]}。！，；：")})
	}
	for _, e := range entities {
		if e.Offset < 0 || e.Length <= 0 {
			continue
		}
		start, end, ok := twitterUTF16Range(text, e.Offset, e.Length)
		if !ok {
			continue
		}
		switch e.Type {
		case tb.EntityTextLink:
			candidates = append(candidates, twitterCandidate{start, e.URL})
		case tb.EntityURL:
			candidates = append(candidates, twitterCandidate{start, text[start:end]})
		default:
		}
	}
	sort.SliceStable(candidates, func(i, j int) bool { return candidates[i].position < candidates[j].position })
	seen := make(map[string]bool)
	var result []string
	for _, candidate := range candidates {
		if canonical, ok := canonicalTwitterURL(candidate.url); ok && !seen[canonical] {
			seen[canonical] = true
			result = append(result, canonical)
		}
	}
	return result
}

func twitterUTF16Range(text string, offset, length int) (int, int, bool) {
	start, end := -1, -1
	units := 0
	for byteIndex, r := range text {
		if units == offset {
			start = byteIndex
		}
		if units == offset+length {
			end = byteIndex
		}
		units += len(utf16.Encode([]rune{r}))
	}
	if units == offset {
		start = len(text)
	}
	if units == offset+length {
		end = len(text)
	}
	if start < 0 || end < start {
		return 0, 0, false
	}
	return start, end, true
}
