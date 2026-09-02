package agentv3

import (
	"encoding/json"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"
)

const searXNGTruncationMarker = "…[truncated]"

var searXNGTokenPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.+-]{0,63}$`)

type searXNGResult struct {
	title, url, summary, engine, category, publishedDate string
	engines                                              []string
	score                                                *float64
}
type searXNGInstanceInfo struct {
	instanceName, defaultLocale string
	safeSearch                  *int
	categories                  []string
	categoriesPresent           bool
	engines                     []searXNGInstanceEngine
}
type searXNGInstanceEngine struct {
	name, shortcut string
	categories     []string
	enabled        bool
}
type searXNGSearchJSONResult struct {
	Rank          int      `json:"rank"`
	Title         string   `json:"title"`
	URL           string   `json:"url"`
	Summary       string   `json:"summary"`
	Source        string   `json:"source,omitempty"`
	PublishedDate string   `json:"published_date,omitempty"`
	Score         *float64 `json:"score,omitempty"`
	Categories    string   `json:"categories,omitempty"`
}
type searXNGSearchJSONOutput struct {
	Results []searXNGSearchJSONResult `json:"results"`
}
type searXNGInstanceEngineJSON struct {
	Name       string   `json:"name"`
	Shortcut   string   `json:"shortcut"`
	Categories []string `json:"categories"`
	Enabled    bool     `json:"enabled"`
}
type searXNGInstanceJSONOutput struct {
	InstanceName  string                       `json:"instance_name,omitempty"`
	DefaultLocale string                       `json:"default_locale,omitempty"`
	SafeSearch    *int                         `json:"safe_search,omitempty"`
	Categories    *[]string                    `json:"categories,omitempty"`
	Engines       *[]searXNGInstanceEngineJSON `json:"engines,omitempty"`
}

func formatSearXNGSearchResults(results []searXNGResult, responseFormat, detail string, maxChars int) string {
	formatted := make([]searXNGSearchJSONResult, 0, len(results))
	for index, result := range results {
		formatted = append(formatted, formatSearXNGSearchResult(result, index+1, detail, maxChars))
	}
	if responseFormat == searXNGResponseFormatJSON {
		encoded, _ := json.Marshal(searXNGSearchJSONOutput{Results: formatted})
		return string(encoded)
	}
	blocks := make([]string, 0, len(formatted))
	for _, result := range formatted {
		blocks = append(blocks, formatSearXNGSearchText(result, detail))
	}
	return strings.Join(blocks, "\n---\n")
}
func formatSearXNGSearchResult(result searXNGResult, rank int, detail string, maxChars int) searXNGSearchJSONResult {
	formatted := searXNGSearchJSONResult{Rank: rank, Title: truncateSearXNGText(result.title, maxChars), URL: truncateSearXNGText(result.url, maxChars), Summary: truncateSearXNGText(result.summary, maxChars)}
	if detail != searXNGResultDetailFull {
		return formatted
	}
	source := result.engine
	if source == "" {
		source = strings.Join(result.engines, ", ")
	}
	formatted.Source, formatted.PublishedDate = truncateSearXNGText(source, maxChars), truncateSearXNGText(result.publishedDate, maxChars)
	formatted.Score, formatted.Categories = result.score, truncateSearXNGText(result.category, maxChars)
	return formatted
}
func formatSearXNGSearchText(result searXNGSearchJSONResult, detail string) string {
	var output strings.Builder
	output.WriteString("rank: ")
	output.WriteString(strconv.Itoa(result.Rank))
	output.WriteString("\ntitle: ")
	output.WriteString(result.Title)
	output.WriteString("\nurl: ")
	output.WriteString(result.URL)
	output.WriteString("\nsummary: ")
	output.WriteString(result.Summary)
	if detail == searXNGResultDetailFull {
		if result.Source != "" {
			output.WriteString("\nsource: ")
			output.WriteString(result.Source)
		}
		if result.PublishedDate != "" {
			output.WriteString("\npublished_date: ")
			output.WriteString(result.PublishedDate)
		}
		if result.Score != nil {
			output.WriteString("\nscore: ")
			output.WriteString(strconv.FormatFloat(*result.Score, 'g', -1, 64))
		}
		if result.Categories != "" {
			output.WriteString("\ncategories: ")
			output.WriteString(result.Categories)
		}
	}
	return output.String()
}
func formatSearXNGSuggestions(values []string, maxItems, maxChars int) string {
	encoded, _ := json.Marshal(normalizeSearXNGStrings(values, maxItems, maxChars))
	return string(encoded)
}
func formatSearXNGInstanceInfo(info searXNGInstanceInfo, args searXNGInstanceInfoArgs, maxItems, maxChars int) string {
	output := searXNGInstanceJSONOutput{InstanceName: truncateSearXNGText(info.instanceName, maxChars), DefaultLocale: truncateSearXNGText(info.defaultLocale, maxChars), SafeSearch: info.safeSearch}
	if info.categoriesPresent {
		categories := normalizeSearXNGStrings(info.categories, maxItems, maxChars)
		output.Categories = &categories
	}
	if args.IncludeEngines {
		engines := make([]searXNGInstanceEngineJSON, 0, min(len(info.engines), maxItems))
		for _, engine := range info.engines {
			if (!args.IncludeDisabled && !engine.enabled) || (args.Category != "" && !containsSearXNGString(engine.categories, args.Category)) {
				continue
			}
			engines = append(engines, searXNGInstanceEngineJSON{Name: truncateSearXNGText(engine.name, maxChars), Shortcut: truncateSearXNGText(engine.shortcut, maxChars), Categories: normalizeSearXNGStrings(engine.categories, maxItems, maxChars), Enabled: engine.enabled})
		}
		sort.Slice(engines, func(i, j int) bool {
			if engines[i].Name == engines[j].Name {
				return engines[i].Shortcut < engines[j].Shortcut
			}
			return engines[i].Name < engines[j].Name
		})
		if len(engines) > maxItems {
			engines = engines[:maxItems]
		}
		output.Engines = &engines
	}
	encoded, _ := json.Marshal(output)
	return string(encoded)
}
func normalizeSearXNGStrings(values []string, maxItems, maxChars int) []string {
	unique := make(map[string]struct{}, len(values))
	for _, value := range values {
		unique[truncateSearXNGText(value, maxChars)] = struct{}{}
	}
	normalized := make([]string, 0, len(unique))
	for value := range unique {
		normalized = append(normalized, value)
	}
	sort.Strings(normalized)
	if len(normalized) > maxItems {
		normalized = normalized[:maxItems]
	}
	return normalized
}
func validSearXNGResultURL(value string) bool {
	parsed, err := url.Parse(value)
	if err != nil || !parsed.IsAbs() || parsed.Host == "" {
		return false
	}
	return strings.EqualFold(parsed.Scheme, "http") || strings.EqualFold(parsed.Scheme, "https")
}
func sameSearXNGOrigin(baseURL, requestURL *url.URL) bool {
	return baseURL != nil && requestURL != nil && strings.EqualFold(baseURL.Scheme, requestURL.Scheme) && strings.EqualFold(baseURL.Hostname(), requestURL.Hostname()) && effectiveSearXNGPort(baseURL) == effectiveSearXNGPort(requestURL)
}
func effectiveSearXNGPort(u *url.URL) string {
	if port := u.Port(); port != "" {
		return port
	}
	if strings.EqualFold(u.Scheme, "http") {
		return "80"
	}
	if strings.EqualFold(u.Scheme, "https") {
		return "443"
	}
	return ""
}
func validSearXNGText(value string, maxRunes int) bool {
	return value != "" && utf8.RuneCountInString(value) <= maxRunes
}
func validSearXNGLanguage(value string) bool {
	if value == "" || utf8.RuneCountInString(value) > 64 {
		return false
	}
	for _, r := range value {
		if unicode.IsControl(r) {
			return false
		}
	}
	return true
}
func validSearXNGToken(value string) bool { return searXNGTokenPattern.MatchString(value) }
func parseSearXNGTokenList(value string) (string, error) {
	if value == "" {
		return "", nil
	}
	tokens := strings.Split(value, ",")
	if len(tokens) > 16 {
		return "", errSearXNGTooManyTokens
	}
	seen := make(map[string]struct{}, len(tokens))
	for _, token := range tokens {
		if !validSearXNGToken(token) {
			return "", errSearXNGInvalidToken
		}
		if _, ok := seen[token]; ok {
			return "", errSearXNGDuplicateToken
		}
		seen[token] = struct{}{}
	}
	return strings.Join(tokens, ","), nil
}
func truncateSearXNGText(value string, maxRunes int) string {
	runes := []rune(value)
	if len(runes) <= maxRunes {
		return value
	}
	marker := []rune(searXNGTruncationMarker)
	if maxRunes <= len(marker) {
		return string(marker[:maxRunes])
	}
	return string(runes[:maxRunes-len(marker)]) + searXNGTruncationMarker
}
func containsSearXNGString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
