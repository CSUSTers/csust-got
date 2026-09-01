//go:build !386 && !arm

package agentv3

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
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
	retriesUsed := 0
	stream, err := m.openStreamWithRetry(ctx, input, opts, &retriesUsed)
	if err != nil {
		return nil, err
	}
	out, writer := schema.Pipe[*schema.Message](32)
	go m.forwardStreamWithRetry(ctx, input, opts, stream, retriesUsed, writer)
	return out, nil
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
	for retriesUsed := 0; ; {
		out, err := run()
		if err == nil {
			return out, nil
		}
		if !m.canRetry(ctx, err, retriesUsed) {
			return out, err
		}
		if sleepErr := m.sleepBeforeRetry(ctx, op, retriesUsed, err); sleepErr != nil {
			return zero, sleepErr
		}
		retriesUsed++
	}
}

func (m *retryingChatModel) openStreamWithRetry(ctx context.Context, input []*schema.Message, opts []model.Option, retriesUsed *int) (*schema.StreamReader[*schema.Message], error) {
	for {
		stream, err := m.inner.Stream(ctx, input, opts...)
		if err == nil {
			return stream, nil
		}
		if !m.canRetry(ctx, err, *retriesUsed) {
			return nil, err
		}
		if sleepErr := m.sleepBeforeRetry(ctx, "stream", *retriesUsed, err); sleepErr != nil {
			return nil, sleepErr
		}
		*retriesUsed++
	}
}

func (m *retryingChatModel) forwardStreamWithRetry(ctx context.Context, input []*schema.Message, opts []model.Option, stream *schema.StreamReader[*schema.Message], retriesUsed int, out *schema.StreamWriter[*schema.Message]) {
	defer out.Close()
	current := stream
	streamSent := false
	clearPartial := func() bool {
		if !streamSent {
			return false
		}
		streamSent = false
		return out.Send(newClearStreamOutputMessage(), nil)
	}
	closeCurrent := func() {
		if current != nil {
			current.Close()
			current = nil
		}
	}
	defer closeCurrent()

	for {
		chunk, err := current.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			closeCurrent()
			if !m.canRetry(ctx, err, retriesUsed) {
				if clearPartial() {
					return
				}
				out.Send(nil, err)
				return
			}
			if sleepErr := m.sleepBeforeRetry(ctx, "stream recv", retriesUsed, err); sleepErr != nil {
				if clearPartial() {
					return
				}
				out.Send(nil, sleepErr)
				return
			}
			retriesUsed++

			next, openErr := m.openStreamWithRetry(ctx, input, opts, &retriesUsed)
			if openErr != nil {
				if clearPartial() {
					return
				}
				out.Send(nil, openErr)
				return
			}
			current = next
			if clearPartial() {
				return
			}
			continue
		}
		if closed := out.Send(chunk, nil); closed {
			return
		}
		if chunk != nil {
			streamSent = true
		}
	}
}

func (m *retryingChatModel) canRetry(ctx context.Context, err error, retriesUsed int) bool {
	return retriesUsed < m.retries && isRetryableModelError(err) && ctx.Err() == nil
}

func (m *retryingChatModel) sleepBeforeRetry(ctx context.Context, op string, retriesUsed int, err error) error {
	delay := retryBackoffDelay(m.initialDelay, retriesUsed)
	zap.L().Warn("agentv3/model: retrying transient model error",
		zap.String("op", op),
		zap.Int("attempt", retriesUsed+1),
		zap.Int("max_retries", m.retries),
		zap.Duration("delay", delay),
		zap.Error(err),
	)
	return m.sleep(ctx, delay)
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
	if err == nil || errors.Is(err, context.Canceled) {
		return false
	}
	status, ok := modelErrorHTTPStatus(err)
	if ok {
		return status == http.StatusRequestTimeout || status == http.StatusTooManyRequests || status >= http.StatusInternalServerError && status <= 599
	}
	if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return true
	}
	msg := strings.ToLower(err.Error())
	for _, marker := range []string{
		"connection reset by peer",
		"connection refused",
		"connection aborted",
		"broken pipe",
		"server closed idle connection",
		"unexpected eof",
		"tls handshake timeout",
		"temporary failure",
		"temporarily unavailable",
		"timeout awaiting response headers",
	} {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
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

var statusCodePattern = regexp.MustCompile(`(?i)(?:status(?:\s+code)?|http\s+status|returned)\D+(\d{3})`)

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
