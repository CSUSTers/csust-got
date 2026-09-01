package agentv3

import (
	"csust-got/config"
	"csust-got/orm"
	"errors"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
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

func TestGetContextToolReturnsStoredPhotoMetadataAndMarkdown(t *testing.T) {
	oldConfig := config.BotConfig
	testConfig := config.NewBotConfig()
	miniRedis := miniredis.RunT(t)
	testConfig.RedisConfig.RedisAddr = miniRedis.Addr()
	testConfig.RedisConfig.KeyPrefix = "get-context-media:"
	config.BotConfig = testConfig
	orm.InitRedis()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})

	chat := &tb.Chat{ID: -100}
	messages := []*tb.Message{
		{
			ID: 109, Chat: chat, Sender: &tb.User{Username: "alice"},
			Photo: &tb.Photo{File: tb.File{FileID: "captionless-photo"}},
		},
		{
			ID: 110, Chat: chat, Sender: &tb.User{Username: "bob"}, Caption: "😀 bold",
			CaptionEntities: []tb.MessageEntity{{Type: tb.EntityBold, Offset: 3, Length: 4}},
			Photo:           &tb.Photo{File: tb.File{FileID: "captioned-photo"}},
		},
	}
	for _, message := range messages {
		require.NoError(t, orm.PushMessageToStream(message))
		require.NoError(t, orm.SetMessage(message))
	}

	tc := &TurnContext{Message: &tb.Message{ID: 111, Chat: chat, Sender: &tb.User{Username: "caller"}}}
	output, err := (&getContextTool{}).InvokableRun(WithTurnContext(t.Context(), tc), `{"limit":10}`)
	require.NoError(t, err)
	assert.Contains(t, output, `type="photo"`)
	assert.Contains(t, output, `<image file_id="captionless-photo" />`)
	assert.Contains(t, output, `<image file_id="captioned-photo" />`)
	assert.Contains(t, output, "😀 **bold**")
}

func TestProgressStepDetailsUpdate(t *testing.T) {
	tc := &TurnContext{}

	got := tc.applyProgressUpdateLocked(updateProgressArgs{
		Mode:   "replace",
		Step:   "搜索信息第一轮",
		Detail: "调用 A 工具查看 A 网站",
	}, "")
	assert.Equal(t, "○ 搜索信息第一轮\n  调用 A 工具查看 A 网站", got)

	got = tc.applyProgressUpdateLocked(updateProgressArgs{
		Step:   "搜索信息第一轮",
		Detail: "调用 B 工具查看 B 网站",
	}, "")
	assert.Equal(t, "○ 搜索信息第一轮\n  调用 B 工具查看 B 网站", got)

	got = tc.applyProgressUpdateLocked(updateProgressArgs{
		Step:   "搜索信息第二轮",
		Detail: "调用 xxx 工具查看 xxx 网站",
	}, "")
	assert.Equal(t, "• 搜索信息第一轮\n○ 搜索信息第二轮\n  调用 xxx 工具查看 xxx 网站", got)
}

func TestProgressReplaceAndFrameworkManagedStepModes(t *testing.T) {
	tc := &TurnContext{}

	_ = tc.applyProgressUpdateLocked(updateProgressArgs{Step: "搜索信息", Detail: "第一轮"}, "")
	got := tc.applyProgressUpdateLocked(updateProgressArgs{Step: "搜索信息", Detail: "第二轮", Mode: "increment"}, "")
	assert.Equal(t, "○ 搜索信息\n  第二轮", got)

	got = tc.applyProgressUpdateLocked(updateProgressArgs{Step: "读取详情", Detail: "打开引用消息"}, "")
	assert.Equal(t, "• 搜索信息\n○ 读取详情\n  打开引用消息", got)

	got = tc.applyProgressUpdateLocked(updateProgressArgs{Step: "重新规划", Details: []string{"确认范围", "准备执行"}, Mode: "replace"}, "")
	assert.Equal(t, "○ 重新规划\n  确认范围\n  准备执行", got)

	got = tc.applyProgressUpdateLocked(updateProgressArgs{Content: "整段覆盖"}, "整段覆盖")
	assert.Equal(t, "整段覆盖", got)
	assert.Empty(t, tc.progressSteps)
}

func TestFormatProgressStepsStyles(t *testing.T) {
	steps := []progressStep{
		{Title: "搜索信息第一轮", Completed: true},
		{Title: "搜索信息第二轮", Details: []string{"调用 xxx 工具查看 xxx 网站"}},
	}

	md := formatProgressSteps(steps, "markdown", wholeTextTypePlain)
	assert.Equal(t, "• 搜索信息第一轮\n○ *搜索信息第二轮*\n  `调用 xxx 工具查看 xxx 网站`", md)

	html := formatProgressSteps(steps, "html", wholeTextTypePlain)
	assert.Equal(t, "• 搜索信息第一轮\n○ <b>搜索信息第二轮</b>\n  <code>调用 xxx 工具查看 xxx 网站</code>", html)
}

func TestFormatProgressStepsMarkdownWrappersEscapeContent(t *testing.T) {
	steps := []progressStep{
		{Title: "搜索_信息(第一轮)", Completed: true},
		{Title: "读取*详情*", Details: []string{"调用 `tool` 路径 C:\\tmp\\x"}},
	}

	quote := formatProgressSteps(steps, "markdown", wholeTextTypeQuote)
	assert.Equal(t, ">• 搜索\\_信息\\(第一轮\\)\n>○ *读取\\*详情\\**\n>  `调用 \\`tool\\` 路径 C:\\\\tmp\\\\x`\n", quote)

	collapse := formatProgressSteps(steps, "markdown", wholeTextTypeCollapse)
	assert.Equal(t, "**>• 搜索\\_信息\\(第一轮\\)\n>○ *读取\\*详情\\**\n>  `调用 \\`tool\\` 路径 C:\\\\tmp\\\\x`\n>||\n", collapse)
}

func TestProgressContentReplaceClearsStructuredState(t *testing.T) {
	tc := &TurnContext{}

	_ = tc.applyProgressUpdateLocked(updateProgressArgs{Step: "搜索信息", Detail: "第一轮"}, "")
	got := tc.applyProgressUpdateLocked(updateProgressArgs{Content: "自动工具进度", Mode: "replace"}, "自动工具进度")

	assert.Equal(t, "自动工具进度", got)
	assert.Empty(t, tc.progressSteps)
}

func TestProgressStateUpdatesBeforeRateLimit(t *testing.T) {
	cfg := configWithProgressSummary()
	tc := &TurnContext{Config: cfg}
	ctx := WithTurnContext(t.Context(), tc)

	_ = tc.applyProgressUpdateLocked(updateProgressArgs{Step: "搜索信息第一轮", Detail: "调用 A 工具"}, "")
	require.Len(t, tc.progressSteps, 1)
	tc.MarkEdited()

	status := updateProgressMessage(ctx, updateProgressArgs{Step: "搜索信息第二轮", Detail: "调用 B 工具"}, "", wholeTextTypePlain)
	assert.Equal(t, "rate_limited", status)
	require.Len(t, tc.progressSteps, 2)
	assert.True(t, tc.progressSteps[0].Completed)
	assert.Equal(t, "搜索信息第二轮", tc.progressSteps[1].Title)

	status = updateProgressMessage(ctx, updateProgressArgs{Detail: "调用 C 工具"}, "", wholeTextTypePlain)
	assert.Equal(t, "rate_limited", status)
	assert.Equal(t, []string{"调用 C 工具"}, tc.progressSteps[1].Details)
}

func configWithProgressSummary() *config.AgentConfig {
	return &config.AgentConfig{
		Format: config.AgentOutputConfig{
			ProgressSummary: &config.ProgressSummaryConfig{Enable: true},
			EditInterval:    time.Second.String(),
		},
	}
}
