package agentv3

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

const (
	telegramRichEnvelopeStart = "<telegram_rich_message>"
	telegramRichEnvelopeEnd   = "</telegram_rich_message>"

	telegramSendRichMessageMethod = "sendRichMessage"

	telegramRichInvalidFallbackText = "I tried to send a rich message, but its payload was invalid. Please try again."
)

var (
	errTelegramRichMissingContent = errors.New("agentv3: telegram rich message content is required")
	errTelegramRichMissingResult  = errors.New("agentv3: telegram api result missing")
)

var telegramRichMarkdownLinkPattern = regexp.MustCompile(`!?\[([^\]]*)\]\([^)]+\)`)

type inputRichMessage struct {
	Markdown string `json:"markdown,omitempty"`
}

type telegramRichParseResult struct {
	RichMessage  inputRichMessage
	FallbackText string
	Err          error
}

func parseTelegramRichMessageEnvelope(text string) (telegramRichParseResult, bool) {
	body, ok := extractTelegramRichEnvelopeBody(text)
	if !ok {
		return telegramRichParseResult{}, false
	}

	markdown := strings.TrimSpace(body)
	fallback := deriveTelegramRichFallback(markdown)
	result := telegramRichParseResult{FallbackText: fallback}
	if markdown == "" {
		result.FallbackText = telegramRichInvalidFallbackText
		result.Err = errTelegramRichMissingContent
		return result, true
	}
	if result.FallbackText == "" {
		result.FallbackText = telegramRichInvalidFallbackText
	}
	result.RichMessage = inputRichMessage{Markdown: markdown}
	return result, true
}

func shouldSuppressPartialRichEnvelope(text string, _ bool) bool {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false
	}
	if strings.HasPrefix(telegramRichEnvelopeStart, trimmed) {
		return true
	}
	return strings.Contains(trimmed, telegramRichEnvelopeStart)
}

type telegramRawCaller interface {
	Raw(method string, payload any) ([]byte, error)
}

type telegramReplyParameters struct {
	MessageID                int  `json:"message_id"`
	AllowSendingWithoutReply bool `json:"allow_sending_without_reply,omitempty"`
}

type telegramSendRichMessagePayload struct {
	ChatID          int64                    `json:"chat_id"`
	MessageThreadID int64                    `json:"message_thread_id,omitempty"`
	RichMessage     inputRichMessage         `json:"rich_message"`
	ReplyParameters *telegramReplyParameters `json:"reply_parameters,omitempty"`
}

func sendTelegramRichMessage(raw telegramRawCaller, chatID int64, replyToMessageID int, rich inputRichMessage) (*tb.Message, error) {
	return sendTelegramRichMessageWithOptions(raw, chatID, replyToMessageID, rich, telegramRichSendOptions{})
}

type telegramRichSendOptions struct {
	ThreadID          int64
	AllowWithoutReply bool
}

func sendTelegramRichMessageWithOptions(raw telegramRawCaller, chatID int64, replyToMessageID int, rich inputRichMessage, opts telegramRichSendOptions) (*tb.Message, error) {
	payload := telegramSendRichMessagePayload{
		ChatID:          chatID,
		MessageThreadID: opts.ThreadID,
		RichMessage:     rich,
	}
	if replyToMessageID != 0 {
		payload.ReplyParameters = &telegramReplyParameters{MessageID: replyToMessageID, AllowSendingWithoutReply: opts.AllowWithoutReply}
	}

	body, err := raw.Raw(telegramSendRichMessageMethod, payload)
	if err != nil {
		return nil, err
	}
	return unwrapTelegramResult(body)
}

func unwrapTelegramResult(body []byte) (*tb.Message, error) {
	var response struct {
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	trimmed := strings.TrimSpace(string(response.Result))
	if trimmed == "" || strings.EqualFold(trimmed, "null") {
		return nil, errTelegramRichMissingResult
	}

	var message tb.Message
	if err := json.Unmarshal(response.Result, &message); err != nil {
		return nil, err
	}
	return &message, nil
}

type telegramRichDelivery struct {
	ShouldSendRich bool
	RichMessage    inputRichMessage
	VisibleText    string
	Err            error
	RichCandidate  bool
}

func resolveTelegramRichDelivery(text string, nativeReason string, format *config.AgentOutputConfig, richEnabled bool, richAuthorized bool) telegramRichDelivery {
	parts := splitOutputWithReason(text, nativeReason, format)
	parsed, ok := parseTelegramRichMessageEnvelope(parts.payload)
	if !ok {
		return telegramRichDelivery{VisibleText: text}
	}

	delivery := telegramRichDelivery{
		VisibleText:   text,
		Err:           parsed.Err,
		RichCandidate: true,
	}
	if !richEnabled || !richAuthorized {
		delivery.VisibleText = telegramRichUnauthorizedVisibleText(text, parsed)
		return delivery
	}
	if parsed.Err != nil {
		delivery.VisibleText = parsed.FallbackText
		if delivery.VisibleText == "" {
			delivery.VisibleText = telegramRichInvalidFallbackText
		}
		return delivery
	}

	delivery.VisibleText = parsed.FallbackText
	delivery.ShouldSendRich = true
	delivery.RichMessage = parsed.RichMessage
	return delivery
}

// telegramRichUnauthorizedVisibleText replaces the envelope with its plain fallback text
// so envelope tags never reach Telegram when rich output is disabled or unauthorized.
func telegramRichUnauthorizedVisibleText(text string, parsed telegramRichParseResult) string {
	prefix, rest, ok := strings.Cut(text, telegramRichEnvelopeStart)
	if !ok {
		return text
	}
	inner, suffix, _ := strings.Cut(rest, telegramRichEnvelopeEnd)
	replacement := strings.TrimSpace(inner)
	if parsed.Err == nil && strings.TrimSpace(parsed.FallbackText) != "" {
		replacement = parsed.FallbackText
	}
	return strings.TrimSpace(joinTelegramRichSegments(prefix, replacement, suffix))
}

func joinTelegramRichSegments(segments ...string) string {
	parts := make([]string, 0, len(segments))
	for _, segment := range segments {
		if trimmed := strings.TrimSpace(segment); trimmed != "" {
			parts = append(parts, trimmed)
		}
	}
	return strings.Join(parts, "\n")
}

// restoreAgentV3ReplayedRichSkill re-activates rich output when the replayed history shows
// a successful rich-message load_skill call and the skill is still in the current catalog,
// so a continued session keeps its output format.
// Runtime environment and other permissions are never restored from history.
func restoreAgentV3ReplayedRichSkill(tc *TurnContext, replay []*schema.Message) bool {
	if tc == nil || tc.Config == nil || !tc.Config.IsAgentV3RichEnabled() || tc.V3 == nil {
		return false
	}
	if _, ok := tc.V3.SkillCatalog.ByName[agentV3RichMessageSkillName]; !ok {
		return false
	}
	loaded := make(map[string]bool)
	for _, message := range replay {
		if message != nil && message.Role == schema.Tool && message.ToolCallID != "" {
			loaded[message.ToolCallID] = strings.HasPrefix(strings.TrimSpace(message.Content), `<loaded_skill name="`+agentV3RichMessageSkillName+`"`)
		}
	}
	for _, message := range replay {
		if message == nil || message.Role != schema.Assistant {
			continue
		}
		for _, call := range message.ToolCalls {
			if call.Function.Name == agentV3ToolLoadSkill && loaded[call.ID] && isRichMessageLoadSkillArgs(call.Function.Arguments) {
				tc.markSkillLoaded(agentV3RichMessageSkillName)
				return true
			}
		}
	}
	return false
}

func isRichMessageLoadSkillArgs(argsJSON string) bool {
	var args loadSkillArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return false
	}
	return normalizeAgentV3SkillName(args.Name) == "rich-message"
}

func extractTelegramRichEnvelopeBody(text string) (string, bool) {
	_, rest, ok := strings.Cut(text, telegramRichEnvelopeStart)
	if !ok {
		return "", false
	}

	body, _, ok := strings.Cut(rest, telegramRichEnvelopeEnd)
	if !ok {
		return rest, true
	}
	return body, true
}

func deriveTelegramRichFallback(markdown string) string {
	text := strings.ReplaceAll(markdown, "\r\n", "\n")
	text = telegramRichMarkdownLinkPattern.ReplaceAllString(text, "$1")
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		lines[i] = simplifyTelegramRichMarkdownLine(line)
	}
	text = strings.Join(lines, "\n")
	replacer := strings.NewReplacer(
		"**", "",
		"__", "",
		"~~", "",
		"||", "",
		"`", "",
		"*", "",
		"_", "",
	)
	return strings.TrimSpace(replacer.Replace(text))
}

func simplifyTelegramRichMarkdownLine(line string) string {
	trimmed := strings.TrimSpace(line)
	trimmed = strings.TrimLeft(trimmed, "#")
	trimmed = strings.TrimSpace(trimmed)
	for _, prefix := range []string{"- [ ] ", "- [x] ", "- [X] ", "- ", "* ", "+ ", "> "} {
		trimmed = strings.TrimPrefix(trimmed, prefix)
	}
	return trimmed
}
