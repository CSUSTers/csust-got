package agentv3

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/schema"
	openai "github.com/meguminnnnnnnnn/go-openai"
	"github.com/stretchr/testify/require"
)

var (
	errAgentV3ContextToolText = errors.New("tool context_length_exceeded")
	errAgentV3ContextLongText = errors.New("maximum context length exceeded")
	errAgentV3ContextCodeText = errors.New("context_length_exceeded")
	errAgentV3UnknownProvider = errors.New("unknown provider")
)

func TestAgentV3ProviderContextErrorSDKCodeAlwaysMatches(t *testing.T) {
	actual := &openai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400, Message: "PRIVATE_TOKEN <html>private error</html>"}
	modelErr := &einoopenai.APIError{Code: "context_length_exceeded", HTTPStatusCode: 400, Message: actual.Message}
	for _, err := range []error{actual, fmt.Errorf("model failed: %w", actual), modelErr, fmt.Errorf("model failed: %w", modelErr)} {
		require.True(t, isAgentV3ProviderContextLimit(err))
		safe := agentV3ContextLimitError{cause: err}
		require.ErrorIs(t, safe, err)
		require.ErrorIs(t, safe, errAgentV3ProviderContextLimit)
		require.True(t, isAgentV3ProviderContextLimit(safe))
		require.NotContains(t, safe.Error(), "PRIVATE_TOKEN")
		require.NotContains(t, safe.Error(), "<html>")
		require.NotContains(t, friendlyAgentErrorMessage(err), "PRIVATE_TOKEN")
		require.NotContains(t, friendlyAgentErrorMessage(err), "<html>")
	}
	for _, err := range []error{
		nil,
		errAgentV3ContextToolText,
		errAgentV3ContextLongText,
		&openai.APIError{Code: "rate_limit_exceeded", Message: "maximum context length exceeded"},
		&openai.APIError{Type: "context_length_exceeded", Message: "context_length_exceeded"},
		&openai.APIError{HTTPStatusCode: 429, Code: "rate_limit_exceeded", Message: "maximum context length exceeded"},
		&einoopenai.APIError{Code: "rate_limit_exceeded", Message: "context_length_exceeded"},
		&einoopenai.APIError{Code: map[string]any{"code": "context_length_exceeded"}},
		&openai.APIError{Code: []string{"context_length_exceeded"}},
		&openai.RequestError{HTTPStatusCode: 500, Body: []byte(`{"code":"context_length_exceeded"}`), Err: errAgentV3ContextCodeText},
		&openai.RequestError{HTTPStatusCode: 400, Body: []byte("invalid json"), Err: errAgentV3UnknownProvider},
		fmt.Errorf("node path: [tools]: %w", errAgentV3ContextCodeText),
	} {
		require.False(t, isAgentV3ProviderContextLimit(err), "%v", err)
	}
}

func TestAgentV3ProviderContextLimitBadRequestPatterns(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = nil
	t.Cleanup(func() { config.BotConfig = old })
	vllm := `{"object":"error","type":"BadRequestError","code":400,"message":"This model's maximum context length is 262144 tokens. However, you requested 300000 tokens."}`
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{"vLLM body without error wrapper", &openai.RequestError{HTTPStatusCode: 400, Body: []byte(vllm)}, true},
		{"vLLM numeric code in APIError", &openai.APIError{Code: 400, HTTPStatusCode: 400, Type: "BadRequestError", Message: "This model's maximum context length is 262144 tokens."}, true},
		{"vLLM numeric code without HTTP status", &einoopenai.APIError{Code: 400, Message: "This model's maximum context length is 262144 tokens."}, true},
		{"DeepSeek invalid_request_error", &openai.APIError{Type: "invalid_request_error", HTTPStatusCode: 400, Message: "This model's maximum context length is 131072 tokens. However, you requested 140000 tokens (139000 in the messages, 1000 in the completion)."}, true},
		{"eino converted DeepSeek error", fmt.Errorf("model: %w", &einoopenai.APIError{Type: "invalid_request_error", HTTPStatusCode: 400, Message: "maximum context length exceeded"}), true},
		{"input length range", &openai.APIError{HTTPStatusCode: 400, Code: "invalid_parameter_error", Message: "Range of input length should be [1, 30720]"}, true},
		{"too many tokens", &openai.APIError{HTTPStatusCode: 400, Message: "Too many tokens in the request"}, true},
		{"exceeds the model's maximum token", &openai.APIError{HTTPStatusCode: 400, Message: "Your prompt exceeds the model's maximum token limit"}, true},
		{"Chinese context length", &openai.APIError{HTTPStatusCode: 400, Message: "请求的上下文长度超出模型上限"}, true},
		{"Chinese exceeds length", &openai.APIError{HTTPStatusCode: 400, Message: "输入超出最大长度"}, true},
		{"generic 400 invalid json", &openai.RequestError{HTTPStatusCode: 400, Body: []byte("invalid json")}, false},
		{"generic 400 parameter error", &openai.APIError{HTTPStatusCode: 400, Code: "invalid_parameter_error", Message: "temperature must be between 0 and 2"}, false},
		{"matching message with 500", &openai.APIError{HTTPStatusCode: 500, Message: "maximum context length exceeded"}, false},
		{"matching message with 429 and non-400 code", &openai.APIError{HTTPStatusCode: 429, Code: 429, Message: "maximum context length exceeded"}, false},
		{"empty message", &openai.APIError{HTTPStatusCode: 400}, false},
		{"plain error", errAgentV3ContextLongText, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, isAgentV3ProviderContextLimit(tt.err))
		})
	}
}

func TestAgentV3ProviderContextLimitConfiguredPatterns(t *testing.T) {
	old := config.BotConfig
	t.Cleanup(func() { config.BotConfig = old })
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Session: config.AgentV3SessionConfig{ContextOverflow: config.AgentV3SessionContextOverflowConfig{Patterns: []string{`prompt is too long`}}}}}
	custom := &openai.APIError{HTTPStatusCode: 400, Message: "prompt is too long: 250000 tokens > 200000 maximum"}
	defaultOnly := &openai.APIError{HTTPStatusCode: 400, Message: "This model's maximum context length is 8192 tokens"}
	require.True(t, isAgentV3ProviderContextLimit(custom))
	require.False(t, isAgentV3ProviderContextLimit(defaultOnly), "configured patterns replace the defaults")
	config.BotConfig.AgentV3.Session.ContextOverflow.Patterns = []string{`(unclosed`}
	require.True(t, isAgentV3ProviderContextLimit(defaultOnly), "an uncompilable runtime list falls back to the defaults")
	config.BotConfig.AgentV3.Session.ContextOverflow.Patterns = nil
	require.True(t, isAgentV3ProviderContextLimit(defaultOnly))
	require.False(t, isAgentV3ProviderContextLimit(custom))
	_, err := compileAgentV3ContextLimitPatterns([]string{`(unclosed`})
	require.Error(t, err)
	compiled, err := compileAgentV3ContextLimitPatterns(nil)
	require.NoError(t, err)
	require.Len(t, compiled, len(agentV3DefaultContextLimitPatterns))
}

func TestAgentV3ProviderContextTraceKeepsTruncatedProviderError(t *testing.T) {
	trace := NewAgentV3Trace("run", 1, 1)
	trace.Spans = []AgentV3TraceSpan{{Name: "model_stream", Error: "raw"}, {Name: "context_build", Error: "other"}}
	body := strings.Repeat("x", 500)
	setAgentV3ContextLimitTraceError(trace, &openai.APIError{HTTPStatusCode: 400, Message: body})
	require.True(t, strings.HasPrefix(trace.Error, errAgentV3ProviderContextLimit.Error()+": "))
	require.Contains(t, trace.Error, "status code: 400")
	require.LessOrEqual(t, len(trace.Error), len(errAgentV3ProviderContextLimit.Error())+2+agentV3ProviderErrorTraceMaxChars+3)
	require.True(t, strings.HasSuffix(trace.Error, "..."))
	require.Equal(t, trace.Error, trace.Spans[0].Error)
	require.Equal(t, "other", trace.Spans[1].Error)
	short := NewAgentV3Trace("run", 1, 1)
	setAgentV3ContextLimitTraceError(short, nil)
	require.Equal(t, errAgentV3ProviderContextLimit.Error(), short.Error)
}

func TestAgentV3ProviderContextStreamReaderSDKError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"error\":{\"code\":\"context_length_exceeded\",\"message\":\"PRIVATE_TOKEN <html>private</html>\"}}\n\n"))
	}))
	defer server.Close()
	mdl, err := buildModel(t.Context(), &config.Model{Model: "fixture", BaseUrl: server.URL, ApiKey: "PRIVATE_TOKEN"})
	require.NoError(t, err)
	reader, err := mdl.Stream(t.Context(), []*schema.Message{schema.UserMessage("request")})
	require.NoError(t, err)
	defer reader.Close()
	_, err = reader.Recv()
	require.Error(t, err)
	require.True(t, isAgentV3ProviderContextLimit(err))
	var apiErr *openai.APIError
	require.ErrorAs(t, err, &apiErr, "stream chunk errors retain the low-level SDK identity")
}

func TestAgentV3ProviderContextClassificationUsesActualModelSDK(t *testing.T) {
	for _, body := range []string{"structured", "vllm", "unrelated"} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				switch body {
				case "structured":
					_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"PRIVATE_TOKEN <html>private error</html>"}}`))
				case "vllm":
					_, _ = w.Write([]byte(`{"object":"error","type":"BadRequestError","code":400,"message":"This model's maximum context length is 262144 tokens. However, you requested 300000 tokens."}`))
				default:
					_, _ = w.Write([]byte(`<html>bad request PRIVATE_TOKEN</html>`))
				}
			}))
			defer server.Close()
			mdl, err := buildModel(t.Context(), &config.Model{Model: "fixture", BaseUrl: server.URL, ApiKey: "PRIVATE_TOKEN"})
			require.NoError(t, err)
			input := []*schema.Message{schema.UserMessage("request")}
			_, generateErr := mdl.Generate(t.Context(), input)
			require.Error(t, generateErr)
			if body == "structured" {
				var apiErr *einoopenai.APIError
				require.ErrorAs(t, generateErr, &apiErr)
				require.Equal(t, "context_length_exceeded", apiErr.Code, "code=%#v type=%s", apiErr.Code, apiErr.Type)
			}
			want := body != "unrelated"
			require.Equal(t, want, isAgentV3ProviderContextLimit(generateErr), "SDK error: %T %v", generateErr, generateErr)
			_, streamErr := mdl.Stream(t.Context(), input)
			require.Error(t, streamErr)
			require.Equal(t, want, isAgentV3ProviderContextLimit(streamErr))
		})
	}
}

func TestAgentV3SessionContextIdentityIsHashedAndIsolated(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = nil
	t.Cleanup(func() { config.BotConfig = old })
	base := &config.AgentConfig{Name: "first", Model: &config.Model{Name: "provider", Model: "model-a", BaseUrl: "https://provider.invalid/private-key", ApiKey: "SECRET"}}
	key := agentV3SessionContextKey(base)
	require.Len(t, key, 64)
	require.NotContains(t, key, "SECRET")
	require.NotContains(t, key, "private-key")
	for _, change := range []func(*config.AgentConfig){
		func(c *config.AgentConfig) { c.Name = "second" },
		func(c *config.AgentConfig) { c.Model.Model = "model-b" },
		func(c *config.AgentConfig) { c.Model.Name = "other-provider" },
		func(c *config.AgentConfig) { c.Model.BaseUrl = "https://other.invalid" },
	} {
		other, model := *base, *base.Model
		other.Model = &model
		change(&other)
		require.NotEqual(t, key, agentV3SessionContextKey(&other))
	}
	base.Model.ApiKey = "ROTATED_SECRET"
	require.Equal(t, key, agentV3SessionContextKey(base), "credentials are not context identity")
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Model: &config.Model{Model: "global-override"}}}
	require.NotEqual(t, key, agentV3SessionContextKey(base))
}
