//go:build !386 && !arm

package agentv3

import (
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"
)

type searXNGTestRequest struct {
	Method        string
	Path          string
	RawQuery      string
	UserAgent     string
	RequestedWith string
	Authorization string
}

type searXNGTestFixture struct {
	testing    *testing.T
	server     *httptest.Server
	mu         sync.Mutex
	status     int
	body       string
	delay      time.Duration
	redirectTo string
	requests   []searXNGTestRequest
}

func newSearXNGTestFixture(t *testing.T) *searXNGTestFixture {
	t.Helper()
	fixture := &searXNGTestFixture{testing: t, status: http.StatusOK, body: `{"results":[]}`}
	fixture.server = httptest.NewServer(http.HandlerFunc(fixture.handle))
	t.Cleanup(fixture.server.Close)
	return fixture
}

func (f *searXNGTestFixture) handle(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	f.requests = append(f.requests, searXNGTestRequest{
		Method:        r.Method,
		Path:          r.URL.Path,
		RawQuery:      r.URL.RawQuery,
		UserAgent:     r.UserAgent(),
		RequestedWith: r.Header.Get("X-Requested-With"),
		Authorization: r.Header.Get("Authorization"),
	})
	status, body, delay, redirectTo := f.status, f.body, f.delay, f.redirectTo
	f.mu.Unlock()
	if delay > 0 {
		time.Sleep(delay)
	}
	if redirectTo != "" {
		w.Header().Set("Location", redirectTo)
	}
	w.WriteHeader(status)
	_, _ = w.Write([]byte(body))
}

func (f *searXNGTestFixture) setResponse(status int, body string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.body, f.delay, f.redirectTo = status, body, 0, ""
}

func (f *searXNGTestFixture) setDelay(delay time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delay = delay
}

func (f *searXNGTestFixture) setRedirect(location string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.status, f.redirectTo = http.StatusFound, location
}

func (f *searXNGTestFixture) lastRequest(t *testing.T) searXNGTestRequest {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.requests) == 0 {
		t.Fatal("fixture received no request")
	}
	return f.requests[len(f.requests)-1]
}

func (f *searXNGTestFixture) requestCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.requests)
}

func testSearXNGConfig(baseURL string) config.AgentV3SearXNGConfig {
	return config.AgentV3SearXNGConfig{
		Enable:                true,
		BaseURL:               baseURL,
		Timeout:               "100ms",
		MaxResponseBytes:      4096,
		MaxResults:            3,
		MaxResultChars:        80,
		DefaultLanguage:       "zh-CN",
		DefaultSafeSearch:     1,
		DefaultResponseFormat: "text",
		UserAgent:             "searxng-client-test",
	}
}

func newTestSearXNGClient(t *testing.T, cfg config.AgentV3SearXNGConfig) *searXNGClient {
	t.Helper()
	client, err := newSearXNGClient(&cfg)
	if err != nil {
		t.Fatalf("new client: %v", err)
	}
	return client
}

func requireSearXNGError(t *testing.T, err error, category string) {
	t.Helper()
	if err == nil || err.Error() != "[SearXNG Error] "+category {
		t.Fatalf("error = %v, want stable category %q", err, category)
	}
}

func TestSearXNGWebSearchUsesFixedOriginStableQueryAndDefaults(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	fixture.setResponse(http.StatusOK, `{"results":[{"title":"Result","url":"https://example.org/a","content":"Summary"}]}`)
	client := newTestSearXNGClient(t, testSearXNGConfig(fixture.server.URL+"/prefix/"))

	got, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
	if err != nil {
		t.Fatalf("web search: %v", err)
	}
	if want := "rank: 1\ntitle: Result\nurl: https://example.org/a\nsummary: Summary"; got != want {
		t.Fatalf("output = %q, want %q", got, want)
	}

	req := fixture.lastRequest(t)
	if req.Method != http.MethodGet || req.Path != "/prefix/search" {
		t.Fatalf("request = %#v, want GET /prefix/search", req)
	}
	wantQuery := url.Values{"format": {"json"}, "language": {"zh-CN"}, "pageno": {"1"}, "q": {"tea"}, "safesearch": {"1"}}.Encode()
	if req.RawQuery != wantQuery {
		t.Fatalf("raw query = %q, want %q", req.RawQuery, wantQuery)
	}
	if req.UserAgent != "searxng-client-test" {
		t.Fatalf("user agent = %q", req.UserAgent)
	}
}

func TestSearXNGWebSearchValidatesParametersAndReconstructsCompactAndFullResults(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	client := newTestSearXNGClient(t, testSearXNGConfig(fixture.server.URL))
	invalidSafeSearch := 3
	notFinite := math.NaN()
	infinite := math.Inf(1)

	for _, args := range []searXNGWebSearchArgs{
		{Query: ""},
		{Query: strings.Repeat("界", 1001)},
		{Query: "tea", PageNo: -1},
		{Query: "tea", TimeRange: "forever"},
		{Query: "tea", Language: "en\nUS"},
		{Query: "tea", SafeSearch: &invalidSafeSearch},
		{Query: "tea", MinScore: &notFinite},
		{Query: "tea", MinScore: &infinite},
		{Query: "tea", NumResults: 4},
		{Query: "tea", Categories: "general,not allowed"},
		{Query: "tea", Categories: "general,general"},
		{Query: "tea", Engines: strings.Repeat("a,", 16) + "a"},
		{Query: "tea", ResponseFormat: "xml"},
		{Query: "tea", ResultDetail: "verbose"},
	} {
		_, err := client.WebSearch(t.Context(), args)
		requireSearXNGError(t, err, "invalid_response")
	}
	if fixture.requestCount() != 0 {
		t.Fatalf("invalid arguments made %d requests", fixture.requestCount())
	}

	cfg := testSearXNGConfig(fixture.server.URL)
	cfg.MaxResultChars = 15
	client = newTestSearXNGClient(t, cfg)
	fixture.setResponse(http.StatusOK, `{"results":[{"title":"First","url":"https://example.org/a","content":"界界界界界界界界界界界界界界界界界界界界","engine":"alpha","engines":["beta","gamma"],"category":"general","score":0.9,"publishedDate":"2026-01-02","ignored":"raw"},{"title":"Filtered","url":"https://example.org/b","content":"nope","score":0.1}]}`)
	minScore := 0.5
	compact, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea", MinScore: &minScore, NumResults: 2})
	if err != nil {
		t.Fatalf("compact search: %v", err)
	}
	wantCompact := "rank: 1\ntitle: First\nurl: htt…[truncated]\nsummary: 界界界…[truncated]"
	if compact != wantCompact {
		t.Fatalf("compact = %q, want %q", compact, wantCompact)
	}

	full, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea", ResultDetail: "full"})
	if err != nil {
		t.Fatalf("full search: %v", err)
	}
	wantFull := "rank: 1\ntitle: First\nurl: htt…[truncated]\nsummary: 界界界…[truncated]\nsource: alpha\npublished_date: 2026-01-02\nscore: 0.9\ncategories: general\n---\nrank: 2\ntitle: Filtered\nurl: htt…[truncated]\nsummary: nope\nscore: 0.1"
	if full != wantFull {
		t.Fatalf("full = %q, want %q", full, wantFull)
	}

	fixture.setResponse(http.StatusOK, `{"results":[{"title":"Bad","url":"ftp://example.org/a","content":"bad"}]}`)
	_, err = client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
	requireSearXNGError(t, err, "invalid_response")
	fixture.setResponse(http.StatusOK, `{"results":[{"title":null,"url":"https://example.org/a","content":"bad"}]}`)
	_, err = client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
	requireSearXNGError(t, err, "invalid_response")
}

func TestSearXNGWebSearchReconstructsStableJSONWithoutRawFields(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	fixture.setResponse(http.StatusOK, `{"results":[{"title":"Result","url":"https://example.org/a","content":"Summary","engine":"alpha","score":0.5,"raw_secret":"do-not-forward"}]}`)
	client := newTestSearXNGClient(t, testSearXNGConfig(fixture.server.URL))

	got, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea", ResponseFormat: "json", ResultDetail: "full"})
	if err != nil {
		t.Fatalf("json search: %v", err)
	}
	want := `{"results":[{"rank":1,"title":"Result","url":"https://example.org/a","summary":"Summary","source":"alpha","score":0.5}]}`
	if got != want {
		t.Fatalf("json = %q, want %q", got, want)
	}
	if strings.Contains(got, "raw_secret") || strings.Contains(got, "do-not-forward") {
		t.Fatalf("raw response field leaked in %q", got)
	}
}

func TestSearXNGWebSearchAcceptsFiniteOutOfRangeMinScoresAndFiltersResults(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	fixture.setResponse(http.StatusOK, `{"results":[{"title":"Below negative threshold","url":"https://example.org/below","content":"below","score":-2},{"title":"Negative threshold match","url":"https://example.org/negative","content":"negative","score":-0.25},{"title":"High threshold match","url":"https://example.org/high","content":"high","score":2}]}`)
	client := newTestSearXNGClient(t, testSearXNGConfig(fixture.server.URL))

	for _, testCase := range []struct {
		name     string
		minScore float64
		contains []string
		absent   []string
	}{
		{
			name:     "negative threshold",
			minScore: -0.5,
			contains: []string{"Negative threshold match", "High threshold match"},
			absent:   []string{"Below negative threshold"},
		},
		{
			name:     "threshold above one",
			minScore: 1.5,
			contains: []string{"High threshold match"},
			absent:   []string{"Below negative threshold", "Negative threshold match"},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			minScore := testCase.minScore
			result, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea", MinScore: &minScore})
			if err != nil {
				t.Fatalf("web search: %v", err)
			}
			for _, want := range testCase.contains {
				if !strings.Contains(result, want) {
					t.Fatalf("result = %q, want %q", result, want)
				}
			}
			for _, unwanted := range testCase.absent {
				if strings.Contains(result, unwanted) {
					t.Fatalf("result = %q, unexpectedly contains %q", result, unwanted)
				}
			}
		})
	}
	if fixture.requestCount() != 2 {
		t.Fatalf("finite min_score values made %d requests, want 2", fixture.requestCount())
	}
}

func TestSearXNGSuggestionsReturnSortedDeduplicatedBoundedStrings(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	cfg := testSearXNGConfig(fixture.server.URL + "/prefix")
	cfg.MaxResults = 2
	client := newTestSearXNGClient(t, cfg)
	t.Run("accepts top-level string array", func(t *testing.T) {
		fixture.setResponse(http.StatusOK, `["beta","alpha","beta","zeta"]`)
		got, err := client.SearchSuggestions(t.Context(), searXNGSuggestionsArgs{Query: "tea", Language: "en"})
		if err != nil {
			t.Fatalf("suggestions: %v", err)
		}
		if want := `["alpha","beta"]`; got != want {
			t.Fatalf("suggestions = %q, want %q", got, want)
		}
		req := fixture.lastRequest(t)
		if req.Path != "/prefix/autocompleter" || req.RawQuery != (url.Values{"language": {"en"}, "q": {"tea"}}).Encode() || req.RequestedWith != "XMLHttpRequest" {
			t.Fatalf("request = %#v", req)
		}
	})
	t.Run("accepts two-element tuple", func(t *testing.T) {
		fixture.setResponse(http.StatusOK, `["tea",["delta","alpha","delta"]]`)
		got, err := client.SearchSuggestions(t.Context(), searXNGSuggestionsArgs{Query: "tea"})
		if err != nil {
			t.Fatalf("tuple suggestions: %v", err)
		}
		if want := `["alpha","delta"]`; got != want {
			t.Fatalf("tuple suggestions = %q, want %q", got, want)
		}
	})
	for name, body := range map[string]string{
		"three-element tuple":       `["tea",["alpha"],[]]`,
		"non-string query":          `[1,["alpha"]]`,
		"non-array suggestions":     `["tea",{}]`,
		"non-string suggestion":     `["tea",["alpha",1]]`,
		"five-element old envelope": `["tea",["alpha"],[],{},{}]`,
	} {
		t.Run("rejects "+name, func(t *testing.T) {
			fixture.setResponse(http.StatusOK, body)
			_, err := client.SearchSuggestions(t.Context(), searXNGSuggestionsArgs{Query: "tea"})
			requireSearXNGError(t, err, "invalid_response")
		})
	}
}

func TestSearXNGInstanceInfoFiltersAndBoundsPublicMetadata(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	fixture.setResponse(http.StatusOK, `{"instance_name":"Fixture","default_locale":"en","safe_search":1,"categories":["images","general"],"engines":[{"name":"Zulu","shortcut":"z","categories":["images"],"enabled":true,"secret":"no"},{"name":"Alpha","shortcut":"a","categories":["general"],"enabled":true},{"name":"Disabled","shortcut":"d","categories":["general"],"enabled":false}],"raw_secret":"do-not-forward"}`)
	client := newTestSearXNGClient(t, testSearXNGConfig(fixture.server.URL+"/prefix"))

	got, err := client.InstanceInfo(t.Context(), searXNGInstanceInfoArgs{IncludeEngines: true, Category: "general"})
	if err != nil {
		t.Fatalf("instance info: %v", err)
	}
	want := `{"instance_name":"Fixture","default_locale":"en","safe_search":1,"categories":["general","images"],"engines":[{"name":"Alpha","shortcut":"a","categories":["general"],"enabled":true}]}`
	if got != want {
		t.Fatalf("instance info = %q, want %q", got, want)
	}
	req := fixture.lastRequest(t)
	if req.Path != "/prefix/config" || req.RawQuery != "" {
		t.Fatalf("request = %#v", req)
	}

	got, err = client.InstanceInfo(t.Context(), searXNGInstanceInfoArgs{})
	if err != nil {
		t.Fatalf("instance info without engines: %v", err)
	}
	if strings.Contains(got, "engines") || strings.Contains(got, "raw_secret") {
		t.Fatalf("unexpected private or engine data in %q", got)
	}
}

func TestSearXNGClientReadsCredentialsOnlyAtRequestTime(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	fixture.setResponse(http.StatusOK, `{"results":[]}`)
	cfg := testSearXNGConfig(fixture.server.URL)
	cfg.UsernameEnv, cfg.PasswordEnv = "SEARXNG_TEST_USER", "SEARXNG_TEST_PASSWORD"
	client := newTestSearXNGClient(t, cfg)

	var getenvCalls atomic.Int64
	client.getenv = func(name string) string {
		getenvCalls.Add(1)
		if name == cfg.UsernameEnv {
			return "user"
		}
		return ""
	}
	if getenvCalls.Load() != 0 {
		t.Fatal("constructor read credentials")
	}
	_, err := client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
	requireSearXNGError(t, err, "unavailable")
	if fixture.requestCount() != 0 {
		t.Fatal("missing credentials made an HTTP request")
	}

	client.getenv = func(name string) string {
		getenvCalls.Add(1)
		if name == cfg.UsernameEnv {
			return "user"
		}
		return "password"
	}
	_, err = client.WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
	if err != nil {
		t.Fatalf("credentialed search: %v", err)
	}
	if getenvCalls.Load() != 4 {
		t.Fatalf("getenv calls = %d, want 4", getenvCalls.Load())
	}
	if req := fixture.lastRequest(t); req.Authorization != "Basic dXNlcjpwYXNzd29yZA==" {
		t.Fatalf("authorization = %q", req.Authorization)
	}
}

func TestSearXNGClientRejectsRedirectTimeoutOversizeStatusAndMalformedJSON(t *testing.T) {
	fixture := newSearXNGTestFixture(t)
	baseConfig := testSearXNGConfig(fixture.server.URL)

	t.Run("redirect", func(t *testing.T) {
		fixture.setRedirect(fixture.server.URL + "/other")
		_, err := newTestSearXNGClient(t, baseConfig).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "request_failed")
	})
	t.Run("timeout", func(t *testing.T) {
		cfg := baseConfig
		cfg.Timeout = "5ms"
		fixture.setResponse(http.StatusOK, `{"results":[]}`)
		fixture.setDelay(50 * time.Millisecond)
		_, err := newTestSearXNGClient(t, cfg).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "timeout")
	})
	t.Run("oversize", func(t *testing.T) {
		cfg := baseConfig
		cfg.MaxResponseBytes = 10
		fixture.setResponse(http.StatusOK, strings.Repeat("x", 11))
		_, err := newTestSearXNGClient(t, cfg).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "invalid_response")
	})
	t.Run("statuses", func(t *testing.T) {
		fixture.setResponse(http.StatusInternalServerError, `{}`)
		_, err := newTestSearXNGClient(t, baseConfig).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "unavailable")
		fixture.setResponse(http.StatusBadRequest, `{}`)
		_, err = newTestSearXNGClient(t, baseConfig).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "request_failed")
	})
	t.Run("malformed JSON", func(t *testing.T) {
		fixture.setResponse(http.StatusOK, `{"results":`)
		_, err := newTestSearXNGClient(t, baseConfig).WebSearch(t.Context(), searXNGWebSearchArgs{Query: "tea"})
		requireSearXNGError(t, err, "invalid_response")
	})
}
