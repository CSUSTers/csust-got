//go:build !386 && !arm

package chatv2

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errTestBadTelegramFile = errors.New("failed to get file info: telegram: Bad Request: wrong file_id or the file is temporarily unavailable (400)")
	errTestVisionOffline   = errors.New("vision model offline")
)

func TestRecoverableImageToolMessage(t *testing.T) {
	t.Run("missing image source is recoverable", func(t *testing.T) {
		msg, ok := recoverableImageToolMessage(errNoImageSource)
		require.True(t, ok)
		assert.Contains(t, msg, "没有可分析的图片")
	})

	t.Run("bad telegram file id is recoverable", func(t *testing.T) {
		msg, ok := recoverableImageToolMessage(errTestBadTelegramFile)
		require.True(t, ok)
		assert.Contains(t, msg, "Telegram 图片不可用")
	})

	t.Run("unrelated errors stay non-recoverable", func(t *testing.T) {
		msg, ok := recoverableImageToolMessage(errTestVisionOffline)
		assert.False(t, ok)
		assert.Empty(t, msg)
	})
}

func TestAnalyzeImageToolSoftFailures(t *testing.T) {
	ctx := WithTurnContext(t.Context(), &TurnContext{})
	tool := &analyzeImageTool{}

	t.Run("invalid arguments do not abort the agent", func(t *testing.T) {
		out, err := tool.InvokableRun(ctx, "{")
		require.NoError(t, err)
		assert.Contains(t, out, "参数无效")
	})

	t.Run("missing file_id and url do not abort the agent", func(t *testing.T) {
		out, err := tool.InvokableRun(ctx, `{}`)
		require.NoError(t, err)
		assert.Contains(t, out, "没有可分析的图片")
	})
}
