package chatv2

import (
	"context"
	"csust-got/chat"
	"csust-got/orm"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

// ---- Tool Registry ----

// builtinToolFactories maps tool names to factory functions.
// Each factory creates a tool.InvokableTool instance.
var builtinToolFactories = map[string]func() tool.InvokableTool{
	"get_context": func() tool.InvokableTool { return &getContextTool{} },
	"get_image":   func() tool.InvokableTool { return &getImageTool{} },
	"get_message": func() tool.InvokableTool { return &getMessageTool{} },
}

// BuildBuiltinTools creates tool instances from a list of tool names.
func BuildBuiltinTools(names []string) ([]tool.BaseTool, error) {
	var tools []tool.BaseTool
	for _, name := range names {
		factory, ok := builtinToolFactories[name]
		if !ok {
			return nil, fmt.Errorf("unknown built-in tool: %s", name)
		}
		tools = append(tools, factory())
	}
	return tools, nil
}

// ---- get_context Tool ----

type getContextTool struct{}

type getContextArgs struct {
	Limit int    `json:"limit,omitempty"` // max messages to retrieve (default: 10)
	Scope string `json:"scope,omitempty"` // "recent" (default) or "reply_chain"
}

func (t *getContextTool) Info(_ context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{
		Name: "get_context",
		Desc: "Retrieve conversation history from the current chat. " +
			"Use 'recent' scope for recent messages, or 'reply_chain' for the reply chain of the current message. " +
			"Returns formatted message history with sender info and timestamps.",
		ParamsOneOf: schema.NewParamsOneOfByParams(map[string]*schema.ParameterInfo{
			"limit": {
				Type: "integer",
				Desc: "Maximum number of messages to retrieve. Default: 10, Max: 50",
			},
			"scope": {
				Type: "string",
				Desc: "Context scope: 'recent' for recent messages, 'reply_chain' for reply chain. Default: 'recent'",
				Enum: []string{"recent", "reply_chain"},
			},
		}),
	}, nil
}

func (t *getContextTool) InvokableRun(ctx context.Context, argsJSON string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc == nil {
		return "", errors.New("get_context: no turn context available")
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

	messages, err := chat.GetMessageContext(tc.Bot, tc.Message, limit)
	if err != nil {
		return "", fmt.Errorf("get_context: failed to get message context: %w", err)
	}

	if len(messages) == 0 {
		return "No conversation context available.", nil
	}

	return chat.FormatContextMessagesWithXml(messages), nil
}

// ---- get_image Tool ----

type getImageTool struct{}

type getImageArgs struct {
	FileID string `json:"file_id"`          // Telegram file ID
	URL    string `json:"url,omitempty"`    // Alternative: direct URL
	Format string `json:"format,omitempty"` // "base64" (default) or "description"
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
		return "", errors.New("get_image: no turn context available")
	}

	var args getImageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("get_image: invalid arguments: %w", err)
	}

	var data []byte
	var mimeType string

	if args.FileID != "" {
		// Download from Telegram
		file, err := tc.Bot.FileByID(args.FileID)
		if err != nil {
			return "", fmt.Errorf("get_image: failed to get file info: %w", err)
		}

		reader, err := tc.Bot.File(&file)
		if err != nil {
			return "", fmt.Errorf("get_image: failed to download file: %w", err)
		}
		defer reader.Close()

		data, err = io.ReadAll(io.LimitReader(reader, 10*1024*1024)) // 10MB limit
		if err != nil {
			return "", fmt.Errorf("get_image: failed to read file data: %w", err)
		}
		mimeType = "image/jpeg" // Telegram typically serves JPEG
	} else if args.URL != "" {
		// Download from URL with timeout and scheme restriction
		if !strings.HasPrefix(args.URL, "http://") && !strings.HasPrefix(args.URL, "https://") {
			return "", errors.New("get_image: only http and https URLs are allowed")
		}
		client := &http.Client{Timeout: 30 * time.Second}
		resp, err := client.Get(args.URL) //nolint:gosec
		if err != nil {
			return "", fmt.Errorf("get_image: failed to fetch URL: %w", err)
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			return "", fmt.Errorf("get_image: URL returned status %d", resp.StatusCode)
		}

		data, err = io.ReadAll(io.LimitReader(resp.Body, 10*1024*1024))
		if err != nil {
			return "", fmt.Errorf("get_image: failed to read URL data: %w", err)
		}
		mimeType = resp.Header.Get("Content-Type")
		if mimeType == "" {
			mimeType = "image/jpeg"
		}
	} else {
		return "", errors.New("get_image: either file_id or url must be provided")
	}

	encoded := base64.StdEncoding.EncodeToString(data)
	return fmt.Sprintf("data:%s;base64,%s", mimeType, encoded), nil
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
		return "", errors.New("get_message: no turn context available")
	}

	var args getMessageArgs
	if err := json.Unmarshal([]byte(argsJSON), &args); err != nil {
		return "", fmt.Errorf("get_message: invalid arguments: %w", err)
	}

	if args.MessageID <= 0 {
		return "", errors.New("get_message: invalid message_id")
	}

	// Try direct Redis lookup first
	msg, err := orm.GetMessage(tc.ChatID, args.MessageID)
	if err == nil && msg != nil {
		return chat.FormatSingleTbMessage(msg, "message"), nil
	}

	// Fallback: search recent context for the message
	contextMsgs, err := chat.GetMessageContext(tc.Bot, tc.Message, 50)
	if err != nil {
		zap.L().Warn("get_message: failed to get context", zap.Error(err))
		return fmt.Sprintf("Could not retrieve message %d.", args.MessageID), nil
	}

	for _, m := range contextMsgs {
		if m.ID == args.MessageID {
			return chat.FormatContextMessagesWithXml([]*chat.ContextMessage{m}), nil
		}
	}
	return fmt.Sprintf("Message %d not found in recent context.", args.MessageID), nil
}
