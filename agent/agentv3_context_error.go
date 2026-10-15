package agentv3

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"unicode/utf8"

	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

const (
	agentV3ProviderContextLimitCode   = "context_length_exceeded"
	agentV3ProviderErrorTraceMaxChars = 300
)

var errAgentV3ProviderContextLimit = errors.New("provider context limit exceeded")

var agentV3DefaultContextLimitPatterns = []string{
	`maximum context length`,
	`context length`,
	`context_length`,
	`input length`,
	`too many tokens`,
	`exceeds? the (model'?s )?(maximum )?(context|token)`,
	`上下文.*(长度|超)`,
	`超出.*长度`,
	`Range of input length`,
}

type agentV3ContextLimitError struct{ cause error }

func (agentV3ContextLimitError) Error() string   { return errAgentV3ProviderContextLimit.Error() }
func (e agentV3ContextLimitError) Unwrap() error { return e.cause }
func (agentV3ContextLimitError) Is(target error) bool {
	return target == errAgentV3ProviderContextLimit
}

func setAgentV3ContextLimitTraceError(trace *AgentV3Trace, cause error) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.Error = errAgentV3ProviderContextLimit.Error()
	if cause != nil {
		trace.Error += ": " + truncateAgentV3ProviderError(cause.Error())
	}
	for i := range trace.Spans {
		span := &trace.Spans[i]
		if (span.Name == "model_stream" || span.Name == "model_generate") && span.Error != "" {
			span.Error = trace.Error
		}
	}
}

func truncateAgentV3ProviderError(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if utf8.RuneCountInString(text) <= agentV3ProviderErrorTraceMaxChars {
		return text
	}
	runes := []rune(text)
	return string(runes[:agentV3ProviderErrorTraceMaxChars]) + "..."
}

type agentV3ProviderErrorDetails struct {
	code    any
	status  int
	message string
}

func agentV3ProviderError(err error) (agentV3ProviderErrorDetails, bool) {
	var modelErr *einoopenai.APIError
	if errors.As(err, &modelErr) && modelErr != nil {
		return agentV3ProviderErrorDetails{code: modelErr.Code, status: modelErr.HTTPStatusCode, message: modelErr.Message}, true
	}
	var apiErr *openai.APIError
	if errors.As(err, &apiErr) && apiErr != nil {
		return agentV3ProviderErrorDetails{code: apiErr.Code, status: apiErr.HTTPStatusCode, message: apiErr.Message}, true
	}
	var reqErr *openai.RequestError
	if errors.As(err, &reqErr) && reqErr != nil {
		message := string(reqErr.Body)
		if reqErr.Err != nil {
			message = reqErr.Err.Error() + " " + message
		}
		return agentV3ProviderErrorDetails{status: reqErr.HTTPStatusCode, message: message}, true
	}
	return agentV3ProviderErrorDetails{}, false
}

func agentV3ProviderCodeIsBadRequest(code any) bool {
	switch v := code.(type) {
	case int:
		return v == 400
	case int64:
		return v == 400
	case float64:
		return v == 400
	case json.Number:
		return v.String() == "400"
	case string:
		return strings.TrimSpace(v) == "400"
	}
	return false
}

func agentV3ContextLimitPatterns() []*regexp.Regexp {
	var configured []string
	if config.BotConfig != nil && config.BotConfig.AgentV3 != nil {
		configured = config.BotConfig.AgentV3.Session.ContextOverflow.Patterns
	}
	compiled, err := compileAgentV3ContextLimitPatterns(configured)
	if err != nil {
		compiled, _ = compileAgentV3ContextLimitPatterns(nil)
	}
	return compiled
}

func compileAgentV3ContextLimitPatterns(patterns []string) ([]*regexp.Regexp, error) {
	if len(patterns) == 0 {
		patterns = agentV3DefaultContextLimitPatterns
	}
	out := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		re, err := regexp.Compile("(?is)(?:" + pattern + ")")
		if err != nil {
			return nil, err
		}
		out = append(out, re)
	}
	return out, nil
}

func isAgentV3ProviderContextLimit(err error) bool {
	details, ok := agentV3ProviderError(err)
	if !ok {
		return false
	}
	if code, isString := details.code.(string); isString && code == agentV3ProviderContextLimitCode {
		return true
	}
	if details.status != 400 && !agentV3ProviderCodeIsBadRequest(details.code) {
		return false
	}
	if strings.TrimSpace(details.message) == "" {
		return false
	}
	for _, re := range agentV3ContextLimitPatterns() {
		if re.MatchString(details.message) {
			return true
		}
	}
	return false
}
