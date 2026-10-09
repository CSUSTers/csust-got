package agentv3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"csust-got/config"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	goopenai "github.com/sashabaranov/go-openai"
	"go.uber.org/zap"
)

var _ model.ToolCallingChatModel = (*retryingChatModel)(nil)

var errModelStreamIdle = errors.New("model stream idle timeout")

type retrySleepFunc func(context.Context, time.Duration) error

type retryingChatModel struct {
	inner        model.ToolCallingChatModel
	retries      int
	initialDelay time.Duration
	idleTimeout  time.Duration
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
		idleTimeout:  cfg.StreamIdleTimeoutDuration(),
		sleep:        sleepWithContext,
	}
}

// streamAttempt is one upstream stream whose context can be cancelled by the idle watchdog.
type streamAttempt struct {
	reader   *schema.StreamReader[*schema.Message]
	cancel   context.CancelFunc
	timer    *time.Timer
	timeout  time.Duration
	idle     atomic.Bool
	mu       sync.Mutex
	deadline time.Time
}

func (a *streamAttempt) armWatchdog(timeout time.Duration) {
	if a == nil || timeout <= 0 {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.timeout = timeout
	a.deadline = time.Now().Add(timeout)
	a.timer = time.AfterFunc(timeout, a.onIdleTimer)
}

// onIdleTimer re-arms instead of cancelling when a chunk moved the deadline after the timer was scheduled.
func (a *streamAttempt) onIdleTimer() {
	a.mu.Lock()
	if remaining := time.Until(a.deadline); remaining > 0 {
		a.timer.Reset(remaining)
		a.mu.Unlock()
		return
	}
	a.idle.Store(true)
	cancel := a.cancel
	a.mu.Unlock()
	cancel()
}

func (a *streamAttempt) touch() {
	if a == nil {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.timer != nil && !a.idle.Load() {
		a.deadline = time.Now().Add(a.timeout)
	}
}

func (a *streamAttempt) close() {
	if a == nil {
		return
	}
	a.mu.Lock()
	if a.timer != nil {
		a.timer.Stop()
	}
	a.mu.Unlock()
	if a.reader != nil {
		a.reader.Close()
	}
	a.cancel()
}

func (a *streamAttempt) classify(err error) error {
	if a != nil && a.idle.Load() {
		return fmt.Errorf("%w: no chunk for %s", errModelStreamIdle, a.timeout)
	}
	return err
}

// Generate and Stream hold one process-wide model-call slot for the whole call, so every model built by
// buildModel (main agents, subagents, vision and progress models) is bounded by max_model_calls.
func (m *retryingChatModel) Generate(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.Message, error) {
	limiter, err := acquireModelCallSlot(ctx)
	if err != nil {
		return nil, err
	}
	defer limiter.release()
	return retryModelCall(ctx, m, "generate", func() (*schema.Message, error) {
		return m.inner.Generate(ctx, input, opts...)
	})
}

// Stream keeps the slot until the forwarding goroutine finishes: upstream EOF, a non-retryable error, or the
// caller closing the returned reader (observed at the next upstream chunk or idle timeout).
func (m *retryingChatModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	limiter, err := acquireModelCallSlot(ctx)
	if err != nil {
		return nil, err
	}
	retriesUsed := 0
	attempt, err := m.openStreamWithRetry(ctx, input, opts, &retriesUsed)
	if err != nil {
		limiter.release()
		return nil, err
	}
	out, writer := schema.Pipe[*schema.Message](32)
	go func() {
		defer limiter.release()
		m.forwardStreamWithRetry(ctx, input, opts, attempt, retriesUsed, writer)
	}()
	return out, nil
}

func acquireModelCallSlot(ctx context.Context) (*slotLimiter, error) {
	limiter := limiters.model()
	if err := limiter.acquire(ctx); err != nil {
		return nil, fmt.Errorf("model call slot: %w", err)
	}
	return limiter, nil
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
		idleTimeout:  m.idleTimeout,
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

func (m *retryingChatModel) openStreamWithRetry(ctx context.Context, input []*schema.Message, opts []model.Option, retriesUsed *int) (*streamAttempt, error) {
	for {
		attempt, err := m.openStreamAttempt(ctx, input, opts)
		if err == nil {
			return attempt, nil
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

func (m *retryingChatModel) openStreamAttempt(ctx context.Context, input []*schema.Message, opts []model.Option) (*streamAttempt, error) {
	attemptCtx, cancel := context.WithCancel(ctx)
	attempt := &streamAttempt{cancel: cancel}
	attempt.armWatchdog(m.idleTimeout)
	reader, err := m.inner.Stream(attemptCtx, input, opts...)
	if err != nil {
		attempt.close()
		return nil, attempt.classify(err)
	}
	attempt.reader = reader
	return attempt, nil
}

func (m *retryingChatModel) forwardStreamWithRetry(ctx context.Context, input []*schema.Message, opts []model.Option, attempt *streamAttempt, retriesUsed int, out *schema.StreamWriter[*schema.Message]) {
	defer out.Close()
	current := attempt
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
			current.close()
			current = nil
		}
	}
	defer closeCurrent()

	for {
		chunk, err := current.reader.Recv()
		if errors.Is(err, io.EOF) {
			return
		}
		if err != nil {
			err = current.classify(err)
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
		current.touch()
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
	if err == nil {
		return false
	}
	if errors.Is(err, errModelStreamIdle) {
		return true
	}
	if errors.Is(err, context.Canceled) {
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
