package agentv3

import (
	"context"
	"csust-got/config"
	"csust-got/orm"
	"csust-got/util"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
	"io"
	"net/http"
	"strings"
)

var (
	errUnknownTool   = errors.New("unknown built-in tool")
	errNoTurnContext = errors.New("no turn context available")
	errInvalidMsgID  = errors.New("invalid message_id")
	errNoImageSource = errors.New("either file_id or url must be provided")
	errBadHTTPStatus = errors.New("unexpected HTTP status")

	progressResult = "ok. If your task is done, output a concise final answer now — do not make additional tool calls."
)

const (
	progressStatusSkipped = "skipped"
	progressModeReplace   = "replace"
)

func parseProgressStyle(s string) wholeTextType {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case string(wholeTextTypePlain):
		return wholeTextTypePlain
	case string(wholeTextTypeQuote):
		return wholeTextTypeQuote
	case "", string(wholeTextTypeCollapse):
		return wholeTextTypeCollapse
	default:
		return wholeTextTypeCollapse
	}
}

func updateProgressMessage(ctx context.Context, args updateProgressArgs, content string, style wholeTextType) string {
	tc := GetTurnContext(ctx)
	if tc == nil || (content == "" && !args.hasStructuredProgress()) {
		return progressStatusSkipped
	}
	if tc.finalized.Load() {
		return "skipped (finalized)"
	}
	ps := tc.Config.Format.ProgressSummary
	if ps == nil || !ps.Enable {
		return "skipped (progress disabled)"
	}

	tc.editMu.Lock()
	defer tc.editMu.Unlock()

	content = tc.applyProgressUpdateLocked(args, content)
	if content == "" {
		return progressStatusSkipped
	}

	floor := getEditInterval(&tc.Config.Format)
	if !tc.ShouldAllowEdit(floor) {
		return "rate_limited"
	}

	outputFormat := tc.Config.Format.GetFormat()
	if outputFormat == "" {
		outputFormat = defaultOutputFormat
	}
	var formatted string
	if args.hasStructuredProgress() && len(tc.progressSteps) > 0 {
		formatted = formatProgressSteps(tc.progressSteps, outputFormat, style)
	} else {
		var buf strings.Builder
		formatText(&buf, content, outputFormat, style)
		formatted = buf.String()
	}
	if formatted == "" {
		return progressStatusSkipped
	}

	parseMode := GetParseMode(&tc.Config.Format)
	opts := &tb.SendOptions{ParseMode: parseMode}

	if tc.progressMsg == nil {
		msg, err := util.SendMessageWithError(&tb.Chat{ID: tc.ChatID}, util.RawTgText(formatted), opts)
		if err != nil {
			zap.L().Debug("agentv3: failed to send progress message", zap.Error(err))
			return "send_failed"
		}
		tc.progressMsg = msg
		tc.MarkEdited()
		return "ok"
	}

	_, err := util.EditMessageWithError(tc.progressMsg, util.RawTgText(formatted), opts)
	if err != nil {
		zap.L().Debug("agentv3: failed to edit progress message", zap.Error(err))
	} else {
		tc.MarkEdited()
	}
	return "ok"
}

// modelConfigurable is implemented by tools that support a model override.
// If a tool implements this interface and a matching entry exists in ToolModels,
// BuildBuiltinTools will inject the model config at creation time.
type modelConfigurable interface {
	SetModelConfig(m *config.Model)
}

// ---- Tool Registry ----

// builtinToolFactories maps tool names to factory functions.
// Each factory creates a tool.InvokableTool instance.
var builtinToolFactories = map[string]func() tool.InvokableTool{
	"get_context":     func() tool.InvokableTool { return &getContextTool{} },
	"get_image":       func() tool.InvokableTool { return &getImageTool{} },
	"get_message":     func() tool.InvokableTool { return &getMessageTool{} },
	"analyze_image":   func() tool.InvokableTool { return &analyzeImageTool{} },
	"update_progress": func() tool.InvokableTool { return &updateProgressTool{} },
}

// BuildBuiltinTools creates tool instances from a list of tool names.
// toolModels provides optional per-tool model overrides (may be nil).
func BuildBuiltinTools(names []string, toolModels map[string]*config.Model) ([]tool.BaseTool, error) {
	var tools []tool.BaseTool
	for _, name := range names {
		factory, ok := builtinToolFactories[name]
		if !ok {
			return nil, fmt.Errorf("%w: %s", errUnknownTool, name)
		}
		t := factory()
		// Inject model config if tool supports it and config exists
		if mc, ok := t.(modelConfigurable); ok && toolModels != nil {
			if m, exists := toolModels[name]; exists {
				mc.SetModelConfig(m)
			}
		}
		tools = append(tools, t)
	}
	return tools, nil
}

// ---- get_context Tool ----

type getContextTool struct{}

type getContextArgs struct {
	Limit int `json:"limit,omitempty"` // max messages to retrieve (default: 10)
}

func (t *getContextTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_context",
		Desc: "Retrieve conversation history from the current chat. " +
			"PROACTIVELY call this tool when you need more context to understand a user's question, " +
			"especially for follow-up questions, references to earlier messages, or when the user says " +
			"\"above\", \"earlier\", \"just now\", etc. " +
			"Returns formatted message history with sender info and timestamps.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"limit": {
				Type: "integer",
				Desc: "Maximum number of messages to retrieve. Default: 10, Max: 50",
			},
		}),
	}, nil
}

func (t *getContextTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("get_context: %w", errNoTurnContext)
	}

	var args getContextArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("get_context: invalid arguments: %w", err)
	}

	limit := args.Limit
	if limit <= 0 {
		limit = 10
	}
	if limit > 50 {
		limit = 50
	}

	messages, err := GetMessageContext(tc.Bot, tc.Message, limit)
	if err != nil {
		return "", fmt.Errorf("get_context: failed to get message context: %w", err)
	}

	if len(messages) == 0 {
		return "No conversation context available.", nil
	}

	return FormatContextMessagesWithXml(messages), nil
}

// ---- get_image Tool ----

type getImageTool struct{}

type getImageArgs struct {
	FileID string `json:"file_id"`       // Telegram file ID
	URL    string `json:"url,omitempty"` // Alternative: direct URL
}

func (t *getImageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_image",
		Desc: "Download and retrieve an image from Telegram by file_id. " +
			"Returns the image as base64-encoded data that can be analyzed by vision models. " +
			"Use this when a message references an image that needs to be examined.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_id": {
				Type:     "string",
				Desc:     "Telegram file ID of the image to retrieve",
				Required: true,
			},
			"url": {
				Type: "string",
				Desc: "Alternative: direct URL to download the image from",
			},
		}),
	}, nil
}

func (t *getImageTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("get_image: %w", errNoTurnContext)
	}
	var args getImageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("get_image: invalid arguments: %w", err)
	}

	return downloadImage(tc, args.FileID, args.URL)
}

// ---- analyze_image Tool ----

type analyzeImageTool struct {
	modelCfg *config.Model // optional: override model for vision calls
}

type analyzeImageArgs struct {
	FileID string `json:"file_id"`         // Telegram file ID
	URL    string `json:"url,omitempty"`   // Alternative: direct URL
	Query  string `json:"query,omitempty"` // What to analyze about the image
}

func (t *analyzeImageTool) SetModelConfig(m *config.Model) {
	t.modelCfg = m
}
func (t *analyzeImageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "analyze_image",
		Desc: "Download an image and analyze its content using vision capabilities. " +
			"Returns a text description or answer about the image. Use this when you need to " +
			"understand what an image contains. Provide a specific query to focus the analysis.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"file_id": {
				Type:     "string",
				Desc:     "Telegram file ID of the image to analyze",
				Required: true,
			},
			"url": {
				Type: "string",
				Desc: "Alternative: direct URL of the image to analyze",
			},
			"query": {
				Type: "string",
				Desc: "Specific question about the image, e.g. 'What text is in this image?' Default: describe the image",
			},
		}),
	}, nil
}

func (t *analyzeImageTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("analyze_image: %w", errNoTurnContext)
	}

	var args analyzeImageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "当前图片分析参数无效。请提供 Telegram 图片的 file_id，或提供一个可直接访问的图片 url。", nil
	}

	// Download image
	imageData, err := downloadImage(tc, args.FileID, args.URL)
	if err != nil {
		if msg, ok := recoverableImageToolMessage(err); ok {
			return msg, nil
		}
		return "", fmt.Errorf("analyze_image: %w", err)
	}

	// Use tool-specific model if configured, otherwise fallback to chat's model
	modelCfg := t.modelCfg
	if modelCfg == nil {
		modelCfg = tc.Config.Model
	}
	visionModel, err := buildModel(ctx, modelCfg)
	if err != nil {
		return "", fmt.Errorf("analyze_image: failed to build vision model: %w", err)
	}

	// Prepare query
	query := args.Query
	if query == "" {
		query = "请描述这张图片的内容。"
	}

	// Build multimodal messages and call vision model
	base64Raw := modelCfg.Features.ImageBase64Raw
	messages := BuildMessagesForSubAgent("", query, imageData, base64Raw)
	result, err := visionModel.Generate(ctx, messages)
	if err != nil {
		return "", fmt.Errorf("analyze_image: vision model call failed: %w", err)
	}

	return result.Content, nil
}

// ---- Image Download Helper ----

// downloadImage downloads an image from Telegram file ID or URL, returns base64 data URI.
func downloadImage(tc *TurnContext, fileID, url string) (string, error) {
	var data []byte
	var mimeType string
	switch {
	case fileID != "":
		file, err := tc.Bot.FileByID(fileID)
		if err != nil {
			return "", fmt.Errorf("failed to get file info: %w", err)
		}
		reader, err := tc.Bot.File(&file)
		if err != nil {
			return "", fmt.Errorf("failed to download file: %w", err)
		}
		defer func() { _ = reader.Close() }()
		data, err = io.ReadAll(io.LimitReader(reader, 10*1024*1024)) // 10MB limit
		if err != nil {
			return "", fmt.Errorf("failed to read file data: %w", err)
		}
		mimeType = "image/jpeg" // Telegram typically serves JPEG
	case url != "":
		resp, err := http.Get(url) //nolint:gosec
		if err != nil {
			return "", fmt.Errorf("failed to fetch URL: %w", err)
		}
		defer func() { _ = resp.Body.Close() }()
		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("%w: %d", errBadHTTPStatus, resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		if err != nil {
			return "", fmt.Errorf("failed to read URL data: %w", err)
		}
		mimeType = resp.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
	default:
		return "", errNoImageSource
	}
	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
}

func recoverableImageToolMessage(err error) (string, bool) {
	switch {
	case err == nil:
		return "", false
	case errors.Is(err, errNoImageSource):
		return "当前消息里没有可分析的图片。请直接发送图片、回复一张图片，或提供一个可直接访问的图片 URL。", true
	case errors.Is(err, errBadHTTPStatus):
		return "图片 URL 当前不可访问，或者返回的不是可下载的图片内容。请换一个可直接访问的图片链接再试。", true
	case isTelegramImageUnavailableError(err):
		return "当前引用的 Telegram 图片不可用：可能没有真实图片附件，或者 file_id 已失效。请让用户直接发送/回复图片后再试。", true
	default:
		return "", false
	}
}

func isTelegramImageUnavailableError(err error) bool {
	if err == nil {
		return false
	}

	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "wrong file_id") ||
		strings.Contains(msg, "file is temporarily unavailable") ||
		(strings.Contains(msg, "telegram: bad request") && strings.Contains(msg, "file_id"))
}

// ---- update_progress Tool ----

type updateProgressTool struct{}

type updateProgressArgs struct {
	Content string   `json:"content,omitempty"`
	Step    string   `json:"step,omitempty"`
	Detail  string   `json:"detail,omitempty"`
	Details []string `json:"details,omitempty"`
	Mode    string   `json:"mode,omitempty"`
	Style   string   `json:"style,omitempty"`
}

func (a updateProgressArgs) hasStructuredProgress() bool {
	mode := strings.ToLower(strings.TrimSpace(a.Mode))
	return strings.TrimSpace(a.Step) != "" || strings.TrimSpace(a.Detail) != "" ||
		len(a.Details) > 0 || mode == progressModeReplace
}

func (tc *TurnContext) applyProgressUpdateLocked(args updateProgressArgs, content string) string {
	if !args.hasStructuredProgress() {
		tc.progressSteps = nil
		return content
	}

	mode := strings.ToLower(strings.TrimSpace(args.Mode))
	stepTitle := cleanProgressLine(args.Step)
	details := normalizeProgressDetails(args.Detail, args.Details)
	if mode == progressModeReplace && stepTitle == "" {
		tc.progressSteps = nil
		return content
	}
	if stepTitle == "" {
		if len(tc.progressSteps) == 0 || len(details) == 0 {
			return content
		}
		current := &tc.progressSteps[len(tc.progressSteps)-1]
		current.Details = details
		return renderProgressSteps(tc.progressSteps)
	}

	switch {
	case mode == progressModeReplace || len(tc.progressSteps) == 0:
		tc.progressSteps = []progressStep{{Title: stepTitle, Details: details}}
	default:
		current := &tc.progressSteps[len(tc.progressSteps)-1]
		if current.Title == stepTitle {
			if len(details) > 0 {
				current.Details = details
			}
			current.Completed = false
		} else {
			current.Completed = true
			tc.progressSteps = append(tc.progressSteps, progressStep{Title: stepTitle, Details: details})
		}
	}

	return renderProgressSteps(tc.progressSteps)
}

func normalizeProgressDetails(detail string, details []string) []string {
	out := make([]string, 0, len(details)+1)
	if line := cleanProgressLine(detail); line != "" {
		out = append(out, line)
	}
	for _, d := range details {
		if line := cleanProgressLine(d); line != "" {
			out = append(out, line)
		}
	}
	return out
}

func cleanProgressLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func renderProgressSteps(steps []progressStep) string {
	var buf strings.Builder
	for _, step := range steps {
		if step.Title == "" {
			continue
		}
		if buf.Len() > 0 {
			buf.WriteByte('\n')
		}
		if step.Completed {
			buf.WriteString("• ")
			buf.WriteString(step.Title)
			continue
		}
		buf.WriteString("○ ")
		buf.WriteString(step.Title)
		for _, detail := range step.Details {
			buf.WriteString("\n  ")
			buf.WriteString(detail)
		}
	}
	return buf.String()
}

func formatProgressSteps(steps []progressStep, format string, style wholeTextType) string {
	switch format {
	case outputFormatHTML:
		return formatHTMLProgressSteps(steps, style)
	default:
		return formatMarkdownProgressSteps(steps, style)
	}
}

func formatMarkdownProgressSteps(steps []progressStep, style wholeTextType) string {
	lines := make([]string, 0, len(steps)*2)
	for _, step := range steps {
		if step.Title == "" {
			continue
		}
		if step.Completed {
			lines = append(lines, "• "+util.EscapeTgMDv2ReservedChars(step.Title))
			continue
		}
		lines = append(lines, "○ *"+util.EscapeTgMDv2ReservedChars(step.Title)+"*")
		for _, detail := range step.Details {
			lines = append(lines, "  `"+escapeMarkdownCode(detail)+"`")
		}
	}
	return wrapFormattedProgressLines(lines, style, "markdown")
}

func formatHTMLProgressSteps(steps []progressStep, style wholeTextType) string {
	lines := make([]string, 0, len(steps)*2)
	for _, step := range steps {
		if step.Title == "" {
			continue
		}
		if step.Completed {
			lines = append(lines, "• "+util.EscapeTgHTMLReservedChars(step.Title))
			continue
		}
		lines = append(lines, "○ <b>"+util.EscapeTgHTMLReservedChars(step.Title)+"</b>")
		for _, detail := range step.Details {
			lines = append(lines, "  <code>"+util.EscapeTgHTMLReservedChars(detail)+"</code>")
		}
	}
	return wrapFormattedProgressLines(lines, style, "html")
}

func wrapFormattedProgressLines(lines []string, style wholeTextType, format string) string {
	if len(lines) == 0 {
		return ""
	}
	body := strings.Join(lines, "\n")
	switch format {
	case outputFormatHTML:
		switch style {
		case wholeTextTypeCollapse:
			return "<blockquote expandable>" + body + "</blockquote>"
		case wholeTextTypeQuote:
			return "<blockquote>" + body + "</blockquote>"
		default:
			return body
		}
	default:
		switch style {
		case wholeTextTypeCollapse:
			return "**>" + strings.Join(lines, "\n>") + "\n>||\n"
		case wholeTextTypeQuote:
			return ">" + strings.Join(lines, "\n>") + "\n"
		default:
			return body
		}
	}
}

func escapeMarkdownCode(s string) string {
	return strings.NewReplacer("\\", "\\\\", "`", "\\`").Replace(s)
}

func (t *updateProgressTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "update_progress",
		Desc: "Send or update an INTERMEDIATE progress/status note to the user during multi-step tasks " +
			"using a step -> details structure. Keep the big step stable and update only detail lines while working; " +
			"start a new step by changing the step title when moving to the next phase. " +
			"Only use mode='replace' when you must overwrite all displayed progress. " +
			"The note appears in a dedicated collapsed quote message, separate from your final answer. " +
			"Do NOT use this to deliver final results — just output your final answer as plain text when done. " +
			"Legacy content-only calls still replace the current progress note.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"content": {
				Type: "string",
				Desc: "Legacy full progress text. If used without step/detail fields, it replaces the whole progress note.",
			},
			"step": {
				Type: "string",
				Desc: "Current big step title, e.g. '搜索信息第二轮'. Same step updates details; a different step appends a new step and marks the previous one completed.",
			},
			"detail": {
				Type: "string",
				Desc: "Current detail under the step, e.g. '调用 xxx 工具查看 xxx 网站'. Replaces the current step detail by default.",
			},
			"details": {
				Type:     "array",
				ElemInfo: &schema.ParameterInfo{Type: "string"},
				Desc:     "Multiple detail lines under the current step. Replaces current details by default.",
			},
			"mode": {
				Type: "string",
				Desc: "Optional. Only 'replace' is supported for model use and resets all steps; omit otherwise. The framework decides same-step updates vs new-step appends from the step title.",
			},
			"style": {
				Type: "string",
				Desc: "Display style. One of: 'collapse' (expandable quote, default), 'quote' (plain quote), 'plain' (no wrapping).",
			},
		}),
	}, nil
}

func (t *updateProgressTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("update_progress: %w", errNoTurnContext)
	}

	if tc.finalized.Load() {
		return "skipped (finalized)", nil
	}

	var args updateProgressArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("update_progress: invalid arguments: %w", err)
	}
	if args.Content == "" && !args.hasStructuredProgress() {
		return "skipped (empty)", nil
	}

	displayText := args.Content
	if displayText != "" && !args.hasStructuredProgress() {
		if psCfg := tc.Config.Format.ProgressSummary; psCfg != nil && psCfg.Model != nil {
			summaryModel, err := tc.GetOrBuildProgressModel(ctx)
			if err != nil {
				zap.L().Warn("update_progress: failed to build progress model, using raw content", zap.Error(err))
			} else if summaryModel != nil {
				sysPrompt := string(psCfg.Prompt)
				if sysPrompt == "" {
					sysPrompt = "你是一个进度总结助手。根据以下内容，生成简洁的进度摘要。"
				}
				messages := []*schema.Message{
					{Role: schema.System, Content: sysPrompt},
					{Role: schema.User, Content: args.Content},
				}
				result, err := summaryModel.Generate(ctx, messages)
				if err != nil {
					zap.L().Warn("update_progress: summary model call failed, using raw content", zap.Error(err))
				} else {
					displayText = result.Content
				}
			}
		}
	}

	style := parseProgressStyle(args.Style)
	status := updateProgressMessage(ctx, args, displayText, style)
	switch status {
	case "rate_limited":
		return "rate_limited: progress message not shown this time. Continue your work; do not call update_progress again until you have substantive new progress.", nil
	case "ok":
		return progressResult, nil
	default:
		return status, nil
	}
}

// ---- get_message Tool ----

type getMessageTool struct{}

type getMessageArgs struct {
	MessageID int `json:"message_id"` // Telegram message ID
}

func (t *getMessageTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_message",
		Desc: "Retrieve the full content of a specific message by its ID in the current chat. " +
			"Useful for getting details about a referenced or replied-to message.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"message_id": {
				Type:     "integer",
				Desc:     "The Telegram message ID to retrieve",
				Required: true,
			},
		}),
	}, nil
}

func (t *getMessageTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", fmt.Errorf("get_message: %w", errNoTurnContext)
	}

	var args getMessageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("get_message: invalid arguments: %w", err)
	}

	if args.MessageID <= 0 {
		return "", fmt.Errorf("get_message: %w", errInvalidMsgID)
	}

	// Try direct Redis lookup first
	msg, err := orm.GetMessage(tc.ChatID, args.MessageID)
	if err == nil && msg != nil {
		return FormatSingleTbMessage(msg, "message"), nil
	}

	// Fallback: search recent context for the message
	contextMsgs, err := GetMessageContext(tc.Bot, tc.Message, 50)
	if err != nil {
		zap.L().Warn("get_message: failed to get context", zap.Error(err))
		return fmt.Sprintf("Could not retrieve message %d.", args.MessageID), nil
	}

	for _, m := range contextMsgs {
		if m.ID == args.MessageID {
			return FormatContextMessagesWithXml([]*ContextMessage{m}), nil
		}
	}
	return fmt.Sprintf("Message %d not found in recent context.", args.MessageID), nil
}
