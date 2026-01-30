package chat

import (
	"context"
	"csust-got/orm"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/sashabaranov/go-openai"
	tb "gopkg.in/telebot.v3"
)

// InternalToolHandler is a function that handles an internal tool call
type InternalToolHandler func(ctx context.Context, args string) (string, error)

// InternalTool represents a built-in tool that doesn't require external HTTP calls
type InternalTool struct {
	Name    string
	Handler InternalToolHandler

	openai.Tool
}

var internalTools map[string]*InternalTool

// InitInternalTools initializes all built-in internal tools
func InitInternalTools() {
	internalTools = make(map[string]*InternalTool)

	// Register the get_instant_view tool
	registerGetInstantViewTool()
}

// GetInternalTool returns an internal tool by name
func GetInternalTool(name string) (*InternalTool, bool) {
	if internalTools == nil {
		return nil, false
	}
	tool, ok := internalTools[name]
	return tool, ok
}

// GetInternalToolDefinitions returns the OpenAI tool definitions for all internal tools
func GetInternalToolDefinitions() []openai.Tool {
	if internalTools == nil {
		return nil
	}
	tools := make([]openai.Tool, 0, len(internalTools))
	for _, tool := range internalTools {
		tools = append(tools, tool.Tool)
	}
	return tools
}

// Call executes the internal tool with the given parameters
func (t *InternalTool) Call(ctx context.Context, args string) (string, error) {
	return t.Handler(ctx, args)
}

// getInstantViewArgs represents the arguments for the get_instant_view tool
type getInstantViewArgs struct {
	ChatID    int64 `json:"chat_id"`
	MessageID int   `json:"message_id"`
}

// getInstantViewResult represents the result of the get_instant_view tool
type getInstantViewResult struct {
	Found       bool                `json:"found"`
	URLs        []instantViewURL    `json:"urls,omitempty"`
	TextLinks   []instantViewURL    `json:"text_links,omitempty"`
	PreviewInfo *previewOptionsInfo `json:"preview_info,omitempty"`
	Error       string              `json:"error,omitempty"`
}

// instantViewURL represents a URL found in the message
type instantViewURL struct {
	Text   string `json:"text,omitempty"`
	URL    string `json:"url"`
	Offset int    `json:"offset"`
	Length int    `json:"length"`
}

// previewOptionsInfo represents the link preview options from the message
type previewOptionsInfo struct {
	Disabled   bool   `json:"disabled,omitempty"`
	URL        string `json:"url,omitempty"`
	SmallMedia bool   `json:"small_media,omitempty"`
	LargeMedia bool   `json:"large_media,omitempty"`
	AboveText  bool   `json:"above_text,omitempty"`
}

func registerGetInstantViewTool() {
	tool := &InternalTool{
		Name:    "get_instant_view",
		Handler: handleGetInstantView,
		Tool: openai.Tool{
			Type: openai.ToolTypeFunction,
			Function: &openai.FunctionDefinition{
				Name:        "get_instant_view",
				Description: "Get instant view (URL preview) information from a message stored in Redis. This tool retrieves URL entities and link preview options from Telegram messages.",
				Parameters: map[string]any{
					"type": "object",
					"properties": map[string]any{
						"chat_id": map[string]any{
							"type":        "integer",
							"description": "The chat ID where the message is located",
						},
						"message_id": map[string]any{
							"type":        "integer",
							"description": "The message ID to retrieve instant view information from",
						},
					},
					"required": []string{"chat_id", "message_id"},
				},
			},
		},
	}

	internalTools[tool.Name] = tool
}

func handleGetInstantView(_ context.Context, args string) (string, error) {
	var params getInstantViewArgs
	if err := json.Unmarshal([]byte(args), &params); err != nil {
		result := getInstantViewResult{
			Found: false,
			Error: fmt.Sprintf("invalid arguments: %v", err),
		}
		return marshalResult(result)
	}

	// Get the message from Redis
	msg, err := orm.GetMessage(params.ChatID, params.MessageID)
	if err != nil {
		result := getInstantViewResult{
			Found: false,
			Error: fmt.Sprintf("message not found or error: %v", err),
		}
		return marshalResult(result)
	}

	// Extract instant view information
	result := extractInstantViewInfo(msg)
	return marshalResult(result)
}

func extractInstantViewInfo(msg *tb.Message) getInstantViewResult {
	result := getInstantViewResult{
		Found: true,
		URLs:  make([]instantViewURL, 0),
	}

	// Get text and entities
	text := msg.Text
	entities := msg.Entities
	if text == "" {
		text = msg.Caption
		entities = msg.CaptionEntities
	}

	// Extract URL entities
	for _, entity := range entities {
		switch entity.Type {
		case tb.EntityURL:
			// Bare URL in the text
			urlText := getTextSubstring(text, entity.Offset, entity.Offset+entity.Length)
			result.URLs = append(result.URLs, instantViewURL{
				Text:   urlText,
				URL:    urlText,
				Offset: entity.Offset,
				Length: entity.Length,
			})
		case tb.EntityTextLink:
			// Text link with hidden URL
			linkText := getTextSubstring(text, entity.Offset, entity.Offset+entity.Length)
			if result.TextLinks == nil {
				result.TextLinks = make([]instantViewURL, 0)
			}
			result.TextLinks = append(result.TextLinks, instantViewURL{
				Text:   linkText,
				URL:    entity.URL,
				Offset: entity.Offset,
				Length: entity.Length,
			})
		default:
			// Other entity types are not relevant for instant view
		}
	}

	// Extract preview options if available
	if msg.PreviewOptions != nil {
		result.PreviewInfo = &previewOptionsInfo{
			Disabled:   msg.PreviewOptions.Disabled,
			URL:        msg.PreviewOptions.URL,
			SmallMedia: msg.PreviewOptions.SmallMedia,
			LargeMedia: msg.PreviewOptions.LargeMedia,
			AboveText:  msg.PreviewOptions.AboveText,
		}
	}

	return result
}

func marshalResult(result getInstantViewResult) (string, error) {
	data, err := json.Marshal(result)
	if err != nil {
		return "", err
	}
	return string(data), nil
}

// FormatInstantViewForDisplay formats the instant view result for human-readable display
func FormatInstantViewForDisplay(result getInstantViewResult) string {
	if !result.Found {
		return "Message not found: " + result.Error
	}

	var sb strings.Builder

	if len(result.URLs) > 0 {
		sb.WriteString("URLs found:\n")
		for i, url := range result.URLs {
			sb.WriteString(fmt.Sprintf("  %d. %s\n", i+1, url.URL))
		}
	}

	if len(result.TextLinks) > 0 {
		sb.WriteString("Text links found:\n")
		for i, link := range result.TextLinks {
			sb.WriteString(fmt.Sprintf("  %d. [%s](%s)\n", i+1, link.Text, link.URL))
		}
	}

	if result.PreviewInfo != nil {
		sb.WriteString("Preview options:\n")
		sb.WriteString(fmt.Sprintf("  Disabled: %s\n", strconv.FormatBool(result.PreviewInfo.Disabled)))
		if result.PreviewInfo.URL != "" {
			sb.WriteString(fmt.Sprintf("  URL: %s\n", result.PreviewInfo.URL))
		}
	}

	if sb.Len() == 0 {
		return "No URLs or links found in the message"
	}

	return sb.String()
}
