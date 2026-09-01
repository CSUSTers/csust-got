//go:build !386 && !arm

package agentv3

import (
	"csust-got/config"
	"fmt"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	tb "gopkg.in/telebot.v3"
)

var beijingFallbackLocation = time.FixedZone("CST", 8*60*60)

// buildPromptData creates the template rendering data from the current turn context.
func buildPromptData(tc *TurnContext, contextMsgs []*ContextMessage) PromptData {
	now := beijingNow()
	pd := PromptData{
		DateTime:        now.Format("2006-01-02 15:04:05"),
		CurrentDateCN:   now.Format("2006年01月02日"),
		Input:           extractInput(tc.Message, tc.Trigger),
		ContextMessages: contextMsgs,
		ContextText:     FormatContextMessages(contextMsgs),
		ContextXml:      FormatContextMessagesWithXml(contextMsgs),
		BotUsername:     "",
	}

	if tc.BotUser != nil {
		pd.BotUsername = tc.BotUser.Username
	}

	// Format the replied-to message as XML
	if tc.Message.ReplyTo != nil {
		pd.ReplyToXml = FormatSingleTbMessage(tc.Message.ReplyTo, "reply_to_message")
	}

	return pd
}

func beijingNow() time.Time {
	loc, err := time.LoadLocation("Asia/Shanghai")
	if err != nil {
		loc = beijingFallbackLocation
	}
	return time.Now().In(loc)
}

// extractInput gets the user's text input from the Telegram message.
// When a trigger with a command is provided, it uses msg.Payload directly
// (which telebot already parses as the argument after the command).
func extractInput(msg *tb.Message, trigger ...*config.AgentTrigger) string {
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

// contextToSchemaMessages converts ContextMessage slice to eino schema.Message slice.
func contextToSchemaMessages(msgs []*ContextMessage, tc *TurnContext) []*schema.Message {
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

// BuildMessagesForSubAgent creates a minimal message set for a subagent invocation.
// Used when the subagent needs to process specific content (e.g., image analysis).
func BuildMessagesForSubAgent(systemPrompt, userInput string, imageData string, base64Raw bool) []*schema.Message {
	var messages []*schema.Message

	if systemPrompt != "" {
		messages = append(messages, schema.SystemMessage(systemPrompt))
	}

	if imageData != "" {
		urlStr := imageData
		if base64Raw {
			urlStr = stripDataURIPrefix(urlStr)
		}
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
