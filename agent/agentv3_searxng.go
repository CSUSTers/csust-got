package agentv3

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"csust-got/config"
)

type searXNGWebSearchArgs struct {
	Query          string   `json:"query"`
	PageNo         int      `json:"pageno,omitempty"`
	TimeRange      string   `json:"time_range,omitempty"`
	Language       string   `json:"language,omitempty"`
	SafeSearch     *int     `json:"safesearch,omitempty"`
	MinScore       *float64 `json:"min_score,omitempty"`
	NumResults     int      `json:"num_results,omitempty"`
	Categories     string   `json:"categories,omitempty"`
	Engines        string   `json:"engines,omitempty"`
	ResponseFormat string   `json:"response_format,omitempty"`
	ResultDetail   string   `json:"result_detail,omitempty"`
}
type searXNGSuggestionsArgs struct {
	Query    string `json:"query"`
	Language string `json:"language,omitempty"`
}
type searXNGInstanceInfoArgs struct {
	IncludeEngines  bool   `json:"include_engines,omitempty"`
	IncludeDisabled bool   `json:"include_disabled,omitempty"`
	Category        string `json:"category,omitempty"`
}
type searXNGClient struct {
	baseURL    *url.URL
	config     config.AgentV3SearXNGConfig
	httpClient *http.Client
	getenv     func(string) string
}
type searXNGError string

const (
	searXNGErrorUnavailable     = "unavailable"
	searXNGErrorInvalidResponse = "invalid_response"
	searXNGErrorTimeout         = "timeout"
	searXNGErrorRequestFailed   = "request_failed"

	searXNGResponseFormatText  = "text"
	searXNGResponseFormatJSON  = "json"
	searXNGResultDetailCompact = "compact"
	searXNGResultDetailFull    = "full"
)

var (
	errSearXNGConfigNil               = errors.New("searxng config is nil")
	errSearXNGInvalidQuery            = errors.New("invalid query")
	errSearXNGInvalidSearchArguments  = errors.New("invalid search arguments")
	errSearXNGMissingBaseURL          = errors.New("missing searxng base URL")
	errSearXNGOriginChanged           = errors.New("searxng origin changed")
	errSearXNGMissingResults          = errors.New("missing results")
	errSearXNGInvalidResult           = errors.New("invalid result")
	errSearXNGInvalidSuggestionsTuple = errors.New("invalid suggestions tuple")
	errSearXNGInvalidSuggestionsQuery = errors.New("invalid suggestions query")
	errSearXNGInvalidSuggestions      = errors.New("invalid suggestions")
	errSearXNGExpectedObject          = errors.New("expected object")
	errSearXNGExpectedArray           = errors.New("expected array")
	errSearXNGMissingRequiredField    = errors.New("missing required field")
	errSearXNGInvalidString           = errors.New("invalid string")
	errSearXNGInvalidStringArray      = errors.New("invalid string array")
	errSearXNGInvalidFiniteFloat      = errors.New("invalid finite float")
	errSearXNGInvalidSafeSearch       = errors.New("invalid safe search")
	errSearXNGInvalidBoolean          = errors.New("invalid boolean")
	errSearXNGTooManyTokens           = errors.New("too many tokens")
	errSearXNGInvalidToken            = errors.New("invalid token")
	errSearXNGDuplicateToken          = errors.New("duplicate token")
)

func (e searXNGError) Error() string        { return "[SearXNG Error] " + string(e) }
func newSearXNGError(category string) error { return searXNGError(category) }
func newSearXNGClient(cfg *config.AgentV3SearXNGConfig) (*searXNGClient, error) {
	if cfg == nil {
		return nil, errSearXNGConfigNil
	}
	baseURL, err := url.Parse(cfg.BaseURL)
	if err != nil {
		return nil, err
	}
	baseURL.Path, baseURL.RawPath = strings.TrimRight(baseURL.Path, "/"), strings.TrimRight(baseURL.RawPath, "/")
	timeout, err := time.ParseDuration(cfg.Timeout)
	if err != nil {
		return nil, err
	}
	return &searXNGClient{baseURL: baseURL, config: *cfg, httpClient: &http.Client{Timeout: timeout, CheckRedirect: func(_ *http.Request, _ []*http.Request) error { return http.ErrUseLastResponse }}, getenv: os.Getenv}, nil
}

type searXNGSearchSettings struct {
	query                        url.Values
	minScore                     *float64
	numResults                   int
	responseFormat, resultDetail string
}

func (c *searXNGClient) WebSearch(ctx context.Context, args searXNGWebSearchArgs) (string, error) {
	settings, err := c.validateWebSearchArgs(args)
	if err != nil {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	body, err := c.getJSON(ctx, "search", settings.query, nil)
	if err != nil {
		return "", err
	}
	results, err := decodeSearXNGSearchResults(body, settings)
	if err != nil {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	return formatSearXNGSearchResults(results, settings.responseFormat, settings.resultDetail, c.config.MaxResultChars), nil
}
func (c *searXNGClient) SearchSuggestions(ctx context.Context, args searXNGSuggestionsArgs) (string, error) {
	if !validSearXNGText(args.Query, 1000) {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	query := url.Values{"q": {args.Query}}
	if args.Language != "" {
		if !validSearXNGLanguage(args.Language) {
			return "", newSearXNGError(searXNGErrorInvalidResponse)
		}
		query.Set("language", args.Language)
	}
	body, err := c.getJSON(ctx, "autocompleter", query, map[string]string{"X-Requested-With": "XMLHttpRequest"})
	if err != nil {
		return "", err
	}
	suggestions, err := decodeSearXNGSuggestions(body)
	if err != nil {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	return formatSearXNGSuggestions(suggestions, c.config.MaxResults, c.config.MaxResultChars), nil
}
func (c *searXNGClient) InstanceInfo(ctx context.Context, args searXNGInstanceInfoArgs) (string, error) {
	if args.Category != "" && !validSearXNGToken(args.Category) {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	body, err := c.getJSON(ctx, "config", nil, nil)
	if err != nil {
		return "", err
	}
	instance, err := decodeSearXNGInstanceInfo(body, args.IncludeEngines)
	if err != nil {
		return "", newSearXNGError(searXNGErrorInvalidResponse)
	}
	return formatSearXNGInstanceInfo(instance, args, c.config.MaxResults, c.config.MaxResultChars), nil
}
func (c *searXNGClient) validateWebSearchArgs(args searXNGWebSearchArgs) (searXNGSearchSettings, error) {
	if c == nil || !validSearXNGText(args.Query, 1000) {
		return searXNGSearchSettings{}, errSearXNGInvalidQuery
	}
	pageNo := args.PageNo
	if pageNo == 0 {
		pageNo = 1
	}
	language := args.Language
	if language == "" {
		language = c.config.DefaultLanguage
	}
	safeSearch := c.config.DefaultSafeSearch
	if args.SafeSearch != nil {
		safeSearch = *args.SafeSearch
	}
	numResults := args.NumResults
	if numResults == 0 {
		numResults = c.config.MaxResults
	}
	categories, categoryErr := parseSearXNGTokenList(args.Categories)
	engines, engineErr := parseSearXNGTokenList(args.Engines)
	responseFormat := args.ResponseFormat
	if responseFormat == "" {
		responseFormat = c.config.DefaultResponseFormat
	}
	resultDetail := args.ResultDetail
	if resultDetail == "" {
		resultDetail = searXNGResultDetailCompact
	}
	validTimeRange := args.TimeRange == "" || args.TimeRange == "day" || args.TimeRange == "week" || args.TimeRange == "month" || args.TimeRange == "year"
	validMinScore := args.MinScore == nil || (!math.IsNaN(*args.MinScore) && !math.IsInf(*args.MinScore, 0))
	if pageNo < 1 || !validTimeRange || !validSearXNGLanguage(language) || safeSearch < 0 || safeSearch > 2 || !validMinScore || numResults < 1 || numResults > c.config.MaxResults || categoryErr != nil || engineErr != nil || (responseFormat != searXNGResponseFormatText && responseFormat != searXNGResponseFormatJSON) || (resultDetail != searXNGResultDetailCompact && resultDetail != searXNGResultDetailFull) {
		return searXNGSearchSettings{}, errSearXNGInvalidSearchArguments
	}
	query := url.Values{"format": {searXNGResponseFormatJSON}, "language": {language}, "pageno": {strconv.Itoa(pageNo)}, "q": {args.Query}, "safesearch": {strconv.Itoa(safeSearch)}}
	if args.TimeRange != "" {
		query.Set("time_range", args.TimeRange)
	}
	if categories != "" {
		query.Set("categories", categories)
	}
	if engines != "" {
		query.Set("engines", engines)
	}
	return searXNGSearchSettings{query: query, minScore: args.MinScore, numResults: numResults, responseFormat: responseFormat, resultDetail: resultDetail}, nil
}
