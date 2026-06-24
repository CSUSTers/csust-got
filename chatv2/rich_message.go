//go:build !386 && !arm

package chatv2

import (
	"encoding/json"
	"errors"
	"regexp"
	"strings"

	"csust-got/config"

	tb "gopkg.in/telebot.v3"
)

const (
	telegramRichEnvelopeStart = "<telegram_rich_message>"
	telegramRichEnvelopeEnd   = "</telegram_rich_message>"

	telegramSendRichMessageMethod = "sendRichMessage"
	telegramEditMessageTextMethod = "editMessageText"

	telegramRichInvalidFallbackText = "I tried to send a rich message, but its payload was invalid. Please try again."
)

var (
	errTelegramRichMissingContent = errors.New("chatv2: telegram rich message content is required")
	errTelegramRichMissingResult  = errors.New("chatv2: telegram api result missing")
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
	MessageID int `json:"message_id"`
}

type telegramSendRichMessagePayload struct {
	ChatID          int64                    `json:"chat_id"`
	RichMessage     inputRichMessage         `json:"rich_message"`
	ReplyParameters *telegramReplyParameters `json:"reply_parameters,omitempty"`
}

type telegramEditRichMessagePayload struct {
	ChatID      int64            `json:"chat_id"`
	MessageID   int              `json:"message_id"`
	RichMessage inputRichMessage `json:"rich_message"`
}

func sendTelegramRichMessage(raw telegramRawCaller, chatID int64, replyToMessageID int, rich inputRichMessage) (*tb.Message, error) {
	payload := telegramSendRichMessagePayload{
		ChatID:      chatID,
		RichMessage: rich,
	}
	if replyToMessageID != 0 {
		payload.ReplyParameters = &telegramReplyParameters{MessageID: replyToMessageID}
	}

	body, err := raw.Raw(telegramSendRichMessageMethod, payload)
	if err != nil {
		return nil, err
	}
	return unwrapTelegramResult(body)
}

func editTelegramRichMessage(raw telegramRawCaller, chatID int64, messageID int, rich inputRichMessage) (*tb.Message, error) {
	body, err := raw.Raw(telegramEditMessageTextMethod, telegramEditRichMessagePayload{
		ChatID:      chatID,
		MessageID:   messageID,
		RichMessage: rich,
	})
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

func resolveTelegramRichDelivery(text string, nativeReason string, format *config.ChatOutputFormatConfig, richEnabled bool) telegramRichDelivery {
	parts := splitOutputWithReason(text, nativeReason, format)
	parsed, ok := parseTelegramRichMessageEnvelope(parts.payload)
	if !ok {
		markdown := strings.TrimSpace(parts.payload)
		if !richEnabled || markdown == "" {
			return telegramRichDelivery{VisibleText: text}
		}
		fallback := deriveTelegramRichFallback(markdown)
		if fallback == "" {
			fallback = markdown
		}
		return telegramRichDelivery{
			ShouldSendRich: true,
			RichMessage:    inputRichMessage{Markdown: markdown},
			VisibleText:    fallback,
			RichCandidate:  true,
		}
	}

	delivery := telegramRichDelivery{
		VisibleText:   parsed.FallbackText,
		Err:           parsed.Err,
		RichCandidate: true,
	}
	if parsed.Err != nil {
		if delivery.VisibleText == "" {
			delivery.VisibleText = telegramRichInvalidFallbackText
		}
		return delivery
	}
	if !richEnabled {
		return delivery
	}

	delivery.ShouldSendRich = true
	delivery.RichMessage = parsed.RichMessage
	return delivery
}

func formatTelegramRichFallbackText(text string, format *config.ChatOutputFormatConfig) string {
	if strings.TrimSpace(text) == "" {
		return ""
	}
	outputFormat := format.GetFormat()
	if outputFormat == "" {
		outputFormat = defaultOutputFormat
	}
	buf := strings.Builder{}
	formatText(&buf, text, outputFormat, wholeTextTypePlain)
	return buf.String()
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
