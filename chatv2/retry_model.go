//go:build !386 && !arm

package chatv2

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strconv"
	"time"

	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
)

var _ model.ToolCallingChatModel = (*retryingChatModel)(nil)

type retrySleepFunc func(context.Context, time.Duration) error

type retryingChatModel struct {
	inner        model.ToolCallingChatModel
	retries      int
	initialDelay time.Duration
	sleep        retrySleepFunc
}

func newRetryingChatModel(inner model.ToolCallingChatModel, cfg *config.Model) model.ToolCallingChatModel {
	if inner == nil {
		return nil
	}
	return &retryingChatModel{
		inner:        inner,
		retries:      cfg.RetryCount(),
		initialDelay: cfg.RetryInitialDelay(),
		sleep:        sleepWithContext,
	}
}

func (m *retryingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	return retryModelCall(ctx, m, "generate", func() (*schema.Message, error) {
		return m.inner.Generate(ctx, input, opts...)
	})
}

func (m *retryingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return retryModelCall(ctx, m, "stream", func() (*schema.StreamReader[*schema.Message], error) {
		return m.inner.Stream(ctx, input, opts...)
	})
}

func (m *retryingChatModel) WithTools(tools []*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	withTools, err := m.inner.WithTools(tools)
	if err != nil {
		return nil, err
	}
	return &retryingChatModel{
		inner:        withTools,
		retries:      m.retries,
		initialDelay: m.initialDelay,
		sleep:        m.sleep,
	}, nil
}

func retryModelCall[T any](ctx context.Context, m *retryingChatModel, op string, run func() (T, error)) (T, error) {
	var zero T
	for attempt := 0; ; attempt++ {
		out, err := run()
		if err == nil {
			return out, nil
		}
		if attempt >= m.retries || !isRetryableModelError(err) || ctx.Err() != nil {
			return out, err
		}
		delay := retryBackoffDelay(m.initialDelay, attempt)
		zap.L().Warn("chatv2/model: retrying transient model error",
			zap.String("op", op),
			zap.Int("attempt", attempt+1),
			zap.Int("max_retries", m.retries),
			zap.Duration("delay", delay),
			zap.Error(err),
		)
		if sleepErr := m.sleep(ctx, delay); sleepErr != nil {
			return zero, sleepErr
		}
	}
}

func retryBackoffDelay(initial time.Duration, retryIndex int) time.Duration {
	if initial <= 0 {
		initial = 500 * time.Millisecond
	}
	if retryIndex <= 0 {
		return initial
	}
	return initial * time.Duration(1<<min(retryIndex, 30))
}

func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return nil
	}
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func isRetryableModelError(err error) bool {
	status, ok := modelErrorHTTPStatus(err)
	return ok && (status == http.StatusTooManyRequests || status >= http.StatusInternalServerError && status <= 599)
}

func modelErrorHTTPStatus(err error) (int, bool) {
	var einoErr *einoopenai.APIError
	if errors.As(err, &einoErr) && einoErr.HTTPStatusCode > 0 {
		return einoErr.HTTPStatusCode, true
	}
	var apiErr *goopenai.APIError
	if errors.As(err, &apiErr) && apiErr.HTTPStatusCode > 0 {
		return apiErr.HTTPStatusCode, true
	}
	var reqErr *goopenai.RequestError
	if errors.As(err, &reqErr) && reqErr.HTTPStatusCode > 0 {
		return reqErr.HTTPStatusCode, true
	}
	return statusFromErrorString(err)
}

var statusCodePattern = regexp.MustCompile(`(?i)(?:status code:|returned)\s*(\d{3})`)

func statusFromErrorString(err error) (int, bool) {
	if err == nil {
		return 0, false
	}
	matches := statusCodePattern.FindStringSubmatch(err.Error())
	if len(matches) < 2 {
		return 0, false
	}
	status, convErr := strconv.Atoi(matches[1])
	if convErr != nil {
		return 0, false
	}
	return status, true
}
