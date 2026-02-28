//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"csust-got/chat"
	"csust-got/config"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

// buildPromptData creates the template rendering data from the current turn context.
func buildPromptData(tc *TurnContext, contextMsgs []*chat.ContextMessage) promptData {
	pd := promptData{
		DateTime:        time.Now().Format("2006-01-02 15:04:05"),
		Input:           extractInput(tc.Message, tc.Trigger),
		ContextMessages: contextMsgs,
		ContextText:     chat.FormatContextMessages(contextMsgs),
		ContextXml:      chat.FormatContextMessagesWithXml(contextMsgs),
		BotUsername:     "",
	}

	if tc.BotUser != nil {
		pd.BotUsername = tc.BotUser.Username
	}

	// Format the replied-to message as XML
	if tc.Message.ReplyTo != nil {
		pd.ReplyToXml = chat.FormatSingleTbMessage(tc.Message.ReplyTo, "reply_to_message")
	}

	return pd
}

// extractInput gets the user's text input from the Telegram message.
// When a trigger with a command is provided, it uses msg.Payload directly
// (which telebot already parses as the argument after the command).
func extractInput(msg *tb.Message, trigger ...*config.ChatTrigger) string {
	text := msg.Text
	if text == "" {
		text = msg.Caption
	}
	// If a command trigger is provided, use Payload (telebot's parsed argument)
	if len(trigger) > 0 && trigger[0] != nil && trigger[0].Command != "" {
		return strings.TrimSpace(msg.Payload)
	}
	// Strip command prefix if present (regex trigger fallback)
	if strings.HasPrefix(text, "/") {
		parts := strings.SplitN(text, " ", 2)
		if len(parts) > 1 {
			text = parts[1]
		} else {
			text = ""
		}
	}
	return strings.TrimSpace(text)
}

// BuildMessages converts the turn context into eino schema.Message slice
// for passing to the agent. Returns [system, ...history, user].
func BuildMessages(cc *CompiledChat, tc *TurnContext, contextMsgs []*chat.ContextMessage) ([]*schema.Message, error) {
	pd := buildPromptData(tc, contextMsgs)
	var messages []*schema.Message

	// 1. System message from rendered template
	if cc.SystemTemplate != nil {
		var buf bytes.Buffer
		if err := cc.SystemTemplate.Execute(&buf, pd); err != nil {
			return nil, fmt.Errorf("failed to render system prompt: %w", err)
		}
		if sysText := strings.TrimSpace(buf.String()); sysText != "" {
			messages = append(messages, schema.SystemMessage(sysText))
		}
	}

	// 2. History messages (from Redis context)
	historyMsgs := contextToSchemaMessages(contextMsgs, tc)
	messages = append(messages, historyMsgs...)

	// 3. User message from rendered prompt template
	var userText string
	if cc.PromptTemplate != nil {
		var buf bytes.Buffer
		if err := cc.PromptTemplate.Execute(&buf, pd); err != nil {
			return nil, fmt.Errorf("failed to render prompt template: %w", err)
		}
		userText = strings.TrimSpace(buf.String())
	}
	if userText == "" {
		userText = pd.Input
	}

	// Build user message - add image hints if reply contains images
	userMsg := buildUserMessage(userText, tc)
	messages = append(messages, userMsg)

	return messages, nil
}

// contextToSchemaMessages converts chat.ContextMessage slice to eino schema.Message slice.
func contextToSchemaMessages(msgs []*chat.ContextMessage, tc *TurnContext) []*schema.Message {
	var result []*schema.Message
	botUsername := ""
	if tc.BotUser != nil {
		botUsername = tc.BotUser.Username
	}

	for _, msg := range msgs {
		// Determine role based on whether message is from the bot
		isBot := msg.User == botUsername && botUsername != ""
		if isBot {
			result = append(result, &schema.Message{
				Role:    schema.Assistant,
				Content: msg.Text,
			})
		} else {
			senderInfo := msg.UserNames.ShowName()
			if senderInfo == "" {
				senderInfo = msg.User
			}
			result = append(result, &schema.Message{
				Role:    schema.User,
				Content: fmt.Sprintf("[%s]: %s", senderInfo, msg.Text),
			})
		}
	}
	return result
}

// buildUserMessage creates the user message, adding hints about available
// media attachments so the agent can decide whether to fetch them via tools.
func buildUserMessage(text string, tc *TurnContext) *schema.Message {
	var mediaHints []string

	// Current message attachments
	if tc.Message.Photo != nil {
		fileID := tc.Message.Photo.FileID
		mediaHints = append(mediaHints, fmt.Sprintf(
			"[This message contains an attached image (file_id: %s). "+
				"Use the analyze_image tool to view and analyze it if needed.]", fileID))
	}
	if tc.Message.Document != nil {
		doc := tc.Message.Document
		mediaHints = append(mediaHints, fmt.Sprintf(
			"[This message has an attached file: %s (file_id: %s, mime: %s).]",
			doc.FileName, doc.FileID, doc.MIME))
	}

	// Reply-to message attachments
	if tc.Message.ReplyTo != nil {
		if tc.Message.ReplyTo.Photo != nil {
			fileID := tc.Message.ReplyTo.Photo.FileID
			mediaHints = append(mediaHints, fmt.Sprintf(
				"[Referenced message contains an image (file_id: %s). "+
					"Use the analyze_image tool to view and analyze it if needed.]", fileID))
		}
		if tc.Message.ReplyTo.Document != nil {
			doc := tc.Message.ReplyTo.Document
			mediaHints = append(mediaHints, fmt.Sprintf(
				"[Referenced message has an attached file: %s (file_id: %s, mime: %s).]",
				doc.FileName, doc.FileID, doc.MIME))
		}
	}

	if len(mediaHints) > 0 {
		text = text + "\n\n" + strings.Join(mediaHints, "\n")
	}
	return &schema.Message{
		Role:    schema.User,
		Content: text,
	}
}

// BuildMessagesForSubAgent creates a minimal message set for a subagent invocation.
// Used when the subagent needs to process specific content (e.g., image analysis).
func BuildMessagesForSubAgent(systemPrompt, userInput string, imageData string) []*schema.Message {
	var messages []*schema.Message

	if systemPrompt != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}

	if imageData != "" {
		// Multimodal message with image
		urlStr := imageData
		messages = append(messages, &schema.Message{
			Role: schema.User,
			UserInputMultiContent: []schema.MessageInputPart{
				{
					Type: schema.ChatMessagePartTypeText,
					Text: userInput,
				},
				{
					Type: schema.ChatMessagePartTypeImageURL,
					Image: &schema.MessageInputImage{
						MessagePartCommon: schema.MessagePartCommon{
							URL: &urlStr,
						},
					},
				},
			},
		})
	} else {
		messages = append(messages, schema.UserMessage(userInput))
	}

	return messages
}
