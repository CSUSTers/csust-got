package agentv3

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
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

func TestAgentV3ProviderContextErrorRequiresSDKCode(t *testing.T) {
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
		&openai.APIError{Code: 400, Message: "context_length_exceeded"},
		&einoopenai.APIError{Code: 400, Message: "context_length_exceeded"},
		&einoopenai.APIError{Code: "rate_limit_exceeded", Message: "context_length_exceeded"},
		&einoopenai.APIError{Code: map[string]any{"code": "context_length_exceeded"}},
		&openai.APIError{Code: []string{"context_length_exceeded"}},
		&openai.RequestError{HTTPStatusCode: 400, Body: []byte(`{"code":"context_length_exceeded"}`), Err: errAgentV3ContextCodeText},
		fmt.Errorf("node path: [tools]: %w", errAgentV3ContextCodeText),
	} {
		require.False(t, isAgentV3ProviderContextLimit(err))
	}
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
	for _, structured := range []bool{false, true} {
		t.Run(strconv.FormatBool(structured), func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusBadRequest)
				if structured {
					_, _ = w.Write([]byte(`{"error":{"code":"context_length_exceeded","type":"invalid_request_error","message":"PRIVATE_TOKEN <html>private error</html>"}}`))
				} else {
					_, _ = w.Write([]byte(`<html>context_length_exceeded PRIVATE_TOKEN</html>`))
				}
			}))
			defer server.Close()
			mdl, err := buildModel(t.Context(), &config.Model{Model: "fixture", BaseUrl: server.URL, ApiKey: "PRIVATE_TOKEN"})
			require.NoError(t, err)
			input := []*schema.Message{schema.UserMessage("request")}
			_, generateErr := mdl.Generate(t.Context(), input)
			require.Error(t, generateErr)
			if structured {
				var apiErr *einoopenai.APIError
				require.ErrorAs(t, generateErr, &apiErr)
				require.Equal(t, "context_length_exceeded", apiErr.Code, "code=%#v type=%s", apiErr.Code, apiErr.Type)
			}
			require.Equal(t, structured, isAgentV3ProviderContextLimit(generateErr), "SDK error: %T %v", generateErr, generateErr)
			_, streamErr := mdl.Stream(t.Context(), input)
			require.Error(t, streamErr)
			require.Equal(t, structured, isAgentV3ProviderContextLimit(streamErr))
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
