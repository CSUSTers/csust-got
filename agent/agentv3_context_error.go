package agentv3

import (
	"errors"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	openai "github.com/meguminnnnnnnnn/go-openai"
)

var errAgentV3ProviderContextLimit = errors.New("provider context limit exceeded")

type agentV3ContextLimitError struct{ cause error }

func (agentV3ContextLimitError) Error() string   { return errAgentV3ProviderContextLimit.Error() }
func (e agentV3ContextLimitError) Unwrap() error { return e.cause }
func (agentV3ContextLimitError) Is(target error) bool {
	return target == errAgentV3ProviderContextLimit
}

func setAgentV3ContextLimitTraceError(trace *AgentV3Trace) {
	trace.mu.Lock()
	defer trace.mu.Unlock()
	trace.Error = errAgentV3ProviderContextLimit.Error()
	for i := range trace.Spans {
		span := &trace.Spans[i]
		if (span.Name == "model_stream" || span.Name == "model_generate") && span.Error != "" {
			span.Error = trace.Error
		}
	}
}

func isAgentV3ProviderContextLimit(err error) bool {
	var modelErr *einoopenai.APIError
	if errors.As(err, &modelErr) && modelErr != nil {
		return modelErr.Code == "context_length_exceeded"
	}
	var apiErr *openai.APIError
	if !errors.As(err, &apiErr) || apiErr == nil {
		return false
	}
	code, ok := apiErr.Code.(string)
	return ok && code == "context_length_exceeded"
}
