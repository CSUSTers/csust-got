package agentv3

import (
	"fmt"
	"strings"

	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

const (
	replySessionOmittedMarker    = "[earlier content omitted]"
	replySessionIncompleteMarker = "Reply session is incomplete; unavailable earlier messages were not reconstructed."
)

func buildReplySessionMessages(_ *CompiledAgent, tc *TurnContext, session replySession, maxChars int) ([]*schema.Message, error) {
	if tc == nil || tc.Message == nil {
		return nil, errReplyChainNoCurrentMessage
	}
	blocks := replySessionLocalBlocks(session, tc.Message)
	blocks, budgetOmitted, err := limitReplySessionBlocksByText(blocks, tc, session.Incomplete, session.Truncated, maxChars)
	if err != nil {
		return nil, err
	}
	currentIndex := replySessionCurrentBlockIndex(blocks, tc.Message.ID)
	if currentIndex < 0 {
		return nil, errReplyChainNoCurrentBlock
	}

	if tc.V3 != nil {
		tc.V3.ImageRefs = replySessionImageRefs(blocks)
	}
	messages := make([]*schema.Message, 0, len(blocks))
	for index, block := range blocks {
		text := replySessionRenderedBlockText(block, tc, session.Incomplete && index == currentIndex, (session.Truncated || budgetOmitted) && index == currentIndex)
		messages = append(messages, replySessionSchemaMessage(block, text, tc))
	}
	return messages, nil
}

func replySessionRenderedBlockText(block replySessionBlock, tc *TurnContext, incomplete, truncated bool) string {
	var builder strings.Builder
	builder.WriteString("<reply_session_block>\n")
	if incomplete {
		builder.WriteString(replySessionIncompleteMarker)
		builder.WriteByte('\n')
	}
	if truncated {
		builder.WriteString(replySessionOmittedMarker)
		builder.WriteByte('\n')
	}
	for _, message := range block.Messages {
		if message != nil {
			builder.WriteString(replySessionRenderedMessageText(message))
			builder.WriteByte('\n')
		}
	}
	if multimodalImageContextEnabled(tc) && !replySessionBlockIsAssistant(block, tc) {
		if manifest := replySessionImageManifest(block.Messages); manifest != "" {
			builder.WriteString(manifest)
			builder.WriteByte('\n')
		}
	}
	builder.WriteString("</reply_session_block>")
	return builder.String()
}

func replySessionRenderedMessageText(message *tb.Message) string {
	senderID := int64(0)
	senderIsBot := false
	if message.Sender != nil {
		senderID = message.Sender.ID
		senderIsBot = message.Sender.IsBot
	}
	replyToID := 0
	if message.ReplyTo != nil {
		replyToID = message.ReplyTo.ID
	}

	var builder strings.Builder
	fmt.Fprintf(&builder, "<reply_session_message message_id=\"%d\" sender_id=\"%d\" sender_is_bot=\"%t\" reply_to_id=\"%d\">", message.ID, senderID, senderIsBot, replyToID)
	sections := []string{
		getMessageTextWithEntities(message, false),
		strings.Join(collectImageToolHints(replySessionMessageContext(message)), "\n"),
		strings.Join(collectDocumentHints(replySessionMessageContext(message)), "\n"),
		replySessionStickerHint(message),
		agentV3ImageRefsContext(replySessionMessageImageRefs(message)),
	}
	if content := joinUserMessageSections(sections...); content != "" {
		builder.WriteByte('\n')
		builder.WriteString(content)
		builder.WriteByte('\n')
	}
	builder.WriteString("</reply_session_message>")
	return builder.String()
}

func replySessionMessageContext(message *tb.Message) *TurnContext {
	return &TurnContext{Message: message}
}

func replySessionStickerHint(message *tb.Message) string {
	if message == nil || message.Sticker == nil {
		return ""
	}
	return fmt.Sprintf("[This message has an attached sticker: emoji: %s, file_id: %s].", message.Sticker.Emoji, message.Sticker.FileID)
}

func replySessionMessageImageRefs(message *tb.Message) []orm.AgentV3ImageRef {
	if message == nil || message.Photo == nil {
		return nil
	}
	return normalizeAgentV3ImageRefs([]orm.AgentV3ImageRef{{MessageID: message.ID, FileID: message.Photo.FileID}})
}

func replySessionImageManifest(messages []*tb.Message) string {
	entries := make([]imageContextEntry, 0, len(messages))
	for _, message := range messages {
		if message != nil && message.Photo != nil {
			entries = append(entries, imageContextEntry{Source: imageContextSourceHistory, MessageID: message.ID})
		}
	}
	return buildImageContextManifest(entries)
}

func replySessionBlockIsAssistant(block replySessionBlock, tc *TurnContext) bool {
	if tc == nil || tc.BotUser == nil || tc.BotUser.ID == 0 || len(block.Messages) == 0 {
		return false
	}
	for _, message := range block.Messages {
		if message == nil || message.Sender == nil || message.Sender.ID != tc.BotUser.ID {
			return false
		}
	}
	return true
}

func replySessionSchemaMessage(block replySessionBlock, text string, tc *TurnContext) *schema.Message {
	if replySessionBlockIsAssistant(block, tc) {
		return schema.AssistantMessage(text, nil)
	}
	if !multimodalImageContextEnabled(tc) {
		return schema.UserMessage(text)
	}

	parts := []schema.MessageInputPart{{Type: schema.ChatMessagePartTypeText, Text: text}}
	entries := collectImageEntriesFromMessages(replySessionEncodingContext(tc, block.Messages), block.Messages, imageContextSourceHistory, make(map[int]struct{}))
	for _, entry := range entries {
		url := entry.DataURL
		if imageBase64RawEnabled(tc) {
			url = stripDataURIPrefix(url)
		}
		parts = append(parts, schema.MessageInputPart{Type: schema.ChatMessagePartTypeImageURL, Image: &schema.MessageInputImage{
			MessagePartCommon: schema.MessagePartCommon{URL: &url},
		}})
	}
	return &schema.Message{Role: schema.User, UserInputMultiContent: parts}
}

func replySessionEncodingContext(tc *TurnContext, messages []*tb.Message) *TurnContext {
	local := &TurnContext{}
	if tc != nil {
		local.Bot, local.Config, local.BotUser, local.ChatID = tc.Bot, tc.Config, tc.BotUser, tc.ChatID
	}
	if len(messages) > 0 {
		local.Message = messages[0]
	}
	return local
}

func replySessionImageRefs(blocks []replySessionBlock) []orm.AgentV3ImageRef {
	refs := make([]orm.AgentV3ImageRef, 0)
	for _, block := range blocks {
		for _, message := range block.Messages {
			refs = append(refs, replySessionMessageImageRefs(message)...)
		}
	}
	return normalizeAgentV3ImageRefs(refs)
}
