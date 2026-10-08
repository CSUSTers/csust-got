package agentv3

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"sync"
	"testing"
	"time"

	einoopenai "github.com/cloudwego/eino-ext/components/model/openai"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	"go.uber.org/zap/zaptest/observer"
)

var errRetryStubUpstream500 = errors.New("upstream returned 500")
var errRetryStubConnectionReset = errors.New("connection reset by peer")
var errRetryStubHTTP408 = errors.New("HTTP status 408: timeout")

func TestRetryingChatModelRetriesGenerate429AndBacksOff(t *testing.T) {
	stub := &retryStubModel{
		generateSteps: []retryGenerateStep{
			{err: &einoopenai.APIError{HTTPStatusCode: http.StatusTooManyRequests, HTTPStatus: "429 Too Many Requests"}},
			{msg: schema.AssistantMessage("ok", nil)},
		},
	}
	var sleeps []time.Duration
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      3,
		initialDelay: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	}

	msg, err := wrapped.Generate(t.Context(), []*schema.Message{schema.UserMessage("hello")})

	require.NoError(t, err)
	assert.Equal(t, "ok", msg.Content)
	assert.Equal(t, 2, stub.generateCalls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, sleeps)
}

func TestRetryingChatModelDefaultBackoffSequence(t *testing.T) {
	stub := &retryStubModel{
		generateSteps: []retryGenerateStep{
			{err: &einoopenai.APIError{HTTPStatusCode: http.StatusInternalServerError}},
			{err: &einoopenai.APIError{HTTPStatusCode: http.StatusBadGateway}},
			{err: &einoopenai.APIError{HTTPStatusCode: http.StatusServiceUnavailable}},
			{msg: schema.AssistantMessage("recovered", nil)},
		},
	}
	var sleeps []time.Duration
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      3,
		initialDelay: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	}

	msg, err := wrapped.Generate(t.Context(), nil)

	require.NoError(t, err)
	assert.Equal(t, "recovered", msg.Content)
	assert.Equal(t, []time.Duration{500 * time.Millisecond, time.Second, 2 * time.Second}, sleeps)
	assert.Equal(t, 4, stub.generateCalls)
}

func TestRetryingChatModelDoesNotRetry400(t *testing.T) {
	errBadRequest := &einoopenai.APIError{HTTPStatusCode: http.StatusBadRequest}
	stub := &retryStubModel{
		generateSteps: []retryGenerateStep{{err: errBadRequest}},
	}
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      3,
		initialDelay: 500 * time.Millisecond,
		sleep: func(context.Context, time.Duration) error {
			t.Fatal("sleep should not be called for non-retryable errors")
			return nil
		},
	}

	msg, err := wrapped.Generate(t.Context(), nil)

	assert.ErrorIs(t, err, errBadRequest)
	assert.Nil(t, msg)
	assert.Equal(t, 1, stub.generateCalls)
}

func TestRetryingChatModelRetriesInitialStreamError(t *testing.T) {
	stub := &retryStubModel{
		streamSteps: []retryStreamStep{
			{err: errRetryStubUpstream500},
			{chunks: []*schema.Message{schema.AssistantMessage("stream ok", nil)}},
		},
	}
	var sleeps []time.Duration
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      3,
		initialDelay: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	}

	stream, err := wrapped.Stream(t.Context(), nil)
	require.NoError(t, err)
	defer stream.Close()
	msg, recvErr := stream.Recv()

	require.NoError(t, recvErr)
	assert.Equal(t, "stream ok", msg.Content)
	assert.Equal(t, 2, stub.streamCalls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, sleeps)
}

func TestRetryingChatModelRetriesStreamRecvErrorAndClearsPartial(t *testing.T) {
	stub := &retryStubModel{
		streamSteps: []retryStreamStep{
			{
				chunks:  []*schema.Message{schema.AssistantMessage("partial", nil)},
				recvErr: errRetryStubUpstream500,
			},
			{chunks: []*schema.Message{schema.AssistantMessage("final", nil)}},
		},
	}
	var sleeps []time.Duration
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      3,
		initialDelay: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	}

	stream, err := wrapped.Stream(t.Context(), nil)
	require.NoError(t, err)
	defer stream.Close()

	first, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	assert.Equal(t, "partial", first.Content)

	clearMsg, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	assert.True(t, isClearStreamOutputMessage(clearMsg))

	final, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	assert.Equal(t, "final", final.Content)

	_, recvErr = stream.Recv()
	assert.ErrorIs(t, recvErr, io.EOF)
	assert.Equal(t, 2, stub.streamCalls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, sleeps)
}

func TestRetryingChatModelClearsPartialBeforeTerminalStreamError(t *testing.T) {
	stub := &retryStubModel{
		streamSteps: []retryStreamStep{
			{
				chunks:  []*schema.Message{schema.AssistantMessage("partial", nil)},
				recvErr: errRetryStubUpstream500,
			},
			{err: errRetryStubUpstream500},
		},
	}
	var sleeps []time.Duration
	wrapped := &retryingChatModel{
		inner:        stub,
		retries:      1,
		initialDelay: 500 * time.Millisecond,
		sleep: func(_ context.Context, d time.Duration) error {
			sleeps = append(sleeps, d)
			return nil
		},
	}

	stream, err := wrapped.Stream(t.Context(), nil)
	require.NoError(t, err)
	defer stream.Close()

	first, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	assert.Equal(t, "partial", first.Content)

	clearMsg, recvErr := stream.Recv()
	require.NoError(t, recvErr)
	assert.True(t, isClearStreamOutputMessage(clearMsg))

	_, recvErr = stream.Recv()
	assert.ErrorIs(t, recvErr, errRetryStubUpstream500)
	assert.Equal(t, 2, stub.streamCalls)
	assert.Equal(t, []time.Duration{500 * time.Millisecond}, sleeps)
}

func TestRetryableModelErrorsIncludeTransientTransportErrors(t *testing.T) {
	assert.True(t, isRetryableModelError(context.DeadlineExceeded))
	assert.True(t, isRetryableModelError(io.ErrUnexpectedEOF))
	assert.True(t, isRetryableModelError(&temporaryNetError{}))
	assert.True(t, isRetryableModelError(errRetryStubConnectionReset))
	assert.True(t, isRetryableModelError(errRetryStubHTTP408))

	assert.False(t, isRetryableModelError(context.Canceled))
	assert.False(t, isRetryableModelError(&einoopenai.APIError{HTTPStatusCode: http.StatusUnauthorized}))
}

type retryGenerateStep struct {
	msg *schema.Message
	err error
}

type retryStreamStep struct {
	chunks  []*schema.Message
	err     error
	recvErr error
}

type temporaryNetError struct{}

func (e *temporaryNetError) Error() string   { return "temporary network error" }
func (e *temporaryNetError) Timeout() bool   { return true }
func (e *temporaryNetError) Temporary() bool { return false }

var _ net.Error = (*temporaryNetError)(nil)

type retryStubModel struct {
	mu            sync.Mutex
	generateSteps []retryGenerateStep
	streamSteps   []retryStreamStep
	generateCalls int
	streamCalls   int
}

func (m *retryStubModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generateCalls++
	idx := m.generateCalls - 1
	if idx >= len(m.generateSteps) {
		return schema.AssistantMessage("", nil), nil
	}
	step := m.generateSteps[idx]
	return step.msg, step.err
}

func (m *retryStubModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.streamCalls++
	idx := m.streamCalls - 1
	if idx >= len(m.streamSteps) {
		return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage("", nil)}), nil
	}
	step := m.streamSteps[idx]
	if step.err != nil {
		return nil, step.err
	}
	if step.recvErr != nil {
		sr, sw := schema.Pipe[*schema.Message](len(step.chunks) + 1)
		go func() {
			defer sw.Close()
			for _, chunk := range step.chunks {
				sw.Send(chunk, nil)
			}
			sw.Send(nil, step.recvErr)
		}()
		return sr, nil
	}
	return schema.StreamReaderFromArray(step.chunks), nil
}

func (m *retryStubModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

// stallingStreamModel stalls a configured number of attempts (at open or mid-stream) until the attempt context is cancelled.
type stallingStreamModel struct {
	mu          sync.Mutex
	stallOpens  int
	stallChunks int
	calls       int
	final       string
}

func (m *stallingStreamModel) Generate(context.Context, []*schema.Message, ...model.Option) (*schema.Message, error) {
	return schema.AssistantMessage(m.final, nil), nil
}

func (m *stallingStreamModel) Stream(ctx context.Context, _ []*schema.Message, _ ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	m.mu.Lock()
	m.calls++
	call := m.calls
	m.mu.Unlock()
	if call <= m.stallOpens {
		<-ctx.Done()
		return nil, ctx.Err()
	}
	if call <= m.stallOpens+m.stallChunks {
		sr, sw := schema.Pipe[*schema.Message](2)
		go func() {
			defer sw.Close()
			sw.Send(schema.AssistantMessage("partial ", nil), nil)
			<-ctx.Done()
			sw.Send(nil, ctx.Err())
		}()
		return sr, nil
	}
	return schema.StreamReaderFromArray([]*schema.Message{schema.AssistantMessage(m.final, nil)}), nil
}

func (m *stallingStreamModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func (m *stallingStreamModel) streamCalls() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

func TestRetryingChatModelIdleWatchdogRetriesStalledStream(t *testing.T) {
	tests := []struct {
		name        string
		stallOpens  int
		stallChunks int
		wantCalls   int
		wantClear   bool
	}{
		{"stalled mid stream", 0, 1, 2, true},
		{"stalled before first chunk", 1, 0, 2, false},
		{"stalled twice within budget", 1, 1, 3, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			stub := &stallingStreamModel{stallOpens: tt.stallOpens, stallChunks: tt.stallChunks, final: "recovered"}
			wrapped := &retryingChatModel{
				inner:        stub,
				retries:      3,
				initialDelay: time.Millisecond,
				idleTimeout:  40 * time.Millisecond,
				sleep:        func(context.Context, time.Duration) error { return nil },
			}
			retried := zapRetryErrors(t, func() {
				stream, err := wrapped.Stream(t.Context(), []*schema.Message{schema.UserMessage("hello")})
				require.NoError(t, err)
				defer stream.Close()
				var visible string
				sawClear := false
				for {
					chunk, recvErr := stream.Recv()
					if errors.Is(recvErr, io.EOF) {
						break
					}
					require.NoError(t, recvErr)
					if isClearStreamOutputMessage(chunk) {
						sawClear = true
						visible = ""
						continue
					}
					visible += chunk.Content
				}
				assert.Equal(t, "recovered", visible)
				assert.Equal(t, tt.wantClear, sawClear)
			})
			assert.Equal(t, tt.wantCalls, stub.streamCalls())
			require.NotEmpty(t, retried)
			for _, err := range retried {
				assert.ErrorIs(t, err, errModelStreamIdle)
			}
		})
	}
}

func TestRetryingChatModelIdleWatchdogIsDisabledByZero(t *testing.T) {
	stub := &stallingStreamModel{stallChunks: 1, final: "never"}
	wrapped := &retryingChatModel{inner: stub, retries: 3, initialDelay: time.Millisecond, sleep: func(context.Context, time.Duration) error { return nil }}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	stream, err := wrapped.Stream(ctx, nil)
	require.NoError(t, err)
	defer stream.Close()
	first, err := stream.Recv()
	require.NoError(t, err)
	assert.Equal(t, "partial ", first.Content)
	for err == nil {
		_, err = stream.Recv()
	}
	require.ErrorIs(t, err, context.DeadlineExceeded, "without a watchdog only the caller deadline ends the stall")
	assert.Equal(t, 1, stub.streamCalls())
}

func TestIdleWatchdogErrorIsRetryable(t *testing.T) {
	assert.True(t, isRetryableModelError(errModelStreamIdle))
	assert.True(t, isRetryableModelError(fmt.Errorf("wrapped: %w", errModelStreamIdle)))
	assert.False(t, isRetryableModelError(context.Canceled))
}

// zapRetryErrors runs fn and returns the errors attached to retry warnings logged meanwhile.
func zapRetryErrors(t *testing.T, fn func()) []error {
	t.Helper()
	core, logs := observer.New(zapcore.WarnLevel)
	restore := zap.ReplaceGlobals(zap.New(core))
	defer restore()
	fn()
	var errs []error
	for _, entry := range logs.All() {
		if entry.Message != "agentv3/model: retrying transient model error" {
			continue
		}
		for _, field := range entry.Context {
			if err, ok := field.Interface.(error); ok {
				errs = append(errs, err)
			}
		}
	}
	return errs
}
