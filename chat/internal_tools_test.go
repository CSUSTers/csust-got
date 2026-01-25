package chat

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestInitInternalTools(t *testing.T) {
	// Ensure we start with a clean state
	internalTools = nil

	// Initialize internal tools
	InitInternalTools()

	// Verify the get_instant_view tool is registered
	tool, ok := GetInternalTool("get_instant_view")
	assert.True(t, ok, "get_instant_view tool should be registered")
	assert.NotNil(t, tool, "tool should not be nil")
	assert.Equal(t, "get_instant_view", tool.Name)
	assert.NotNil(t, tool.Handler)
}

func TestGetInternalTool(t *testing.T) {
	// Initialize internal tools
	InitInternalTools()

	tests := []struct {
		name     string
		toolName string
		wantOk   bool
	}{
		{
			name:     "existing tool",
			toolName: "get_instant_view",
			wantOk:   true,
		},
		{
			name:     "non-existing tool",
			toolName: "non_existing_tool",
			wantOk:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tool, ok := GetInternalTool(tt.toolName)
			assert.Equal(t, tt.wantOk, ok)
			if tt.wantOk {
				assert.NotNil(t, tool)
			} else {
				assert.Nil(t, tool)
			}
		})
	}
}

func TestGetInternalToolDefinitions(t *testing.T) {
	// Initialize internal tools
	InitInternalTools()

	definitions := GetInternalToolDefinitions()
	require.NotNil(t, definitions)
	require.Len(t, definitions, 1) // We have 1 internal tool

	// Verify the tool definition
	def := definitions[0]
	assert.Equal(t, "function", string(def.Type))
	assert.Equal(t, "get_instant_view", def.Function.Name)
	assert.NotEmpty(t, def.Function.Description)
	assert.NotNil(t, def.Function.Parameters)
}

func TestHandleGetInstantView_InvalidArgs(t *testing.T) {
	InitInternalTools()

	// Test with invalid JSON
	result, err := handleGetInstantView(t.Context(), "invalid json")
	require.NoError(t, err) // The handler returns error in JSON, not as error

	var res getInstantViewResult
	err = json.Unmarshal([]byte(result), &res)
	require.NoError(t, err)
	assert.False(t, res.Found)
	assert.Contains(t, res.Error, "invalid arguments")
}

func TestExtractInstantViewInfo(t *testing.T) {
	tests := []struct {
		name    string
		msg     *tb.Message
		wantURL int
		wantTL  int
	}{
		{
			name: "message with bare URL",
			msg: &tb.Message{
				Text: "Check out https://example.com for more info",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityURL,
						Offset: 10,
						Length: 19,
					},
				},
			},
			wantURL: 1,
			wantTL:  0,
		},
		{
			name: "message with text link",
			msg: &tb.Message{
				Text: "Click here for more info",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 6,
						Length: 4,
						URL:    "https://example.com",
					},
				},
			},
			wantURL: 0,
			wantTL:  1,
		},
		{
			name: "message with multiple URLs",
			msg: &tb.Message{
				Text: "Visit https://example.com and https://test.com",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityURL,
						Offset: 6,
						Length: 19,
					},
					{
						Type:   tb.EntityURL,
						Offset: 30,
						Length: 16,
					},
				},
			},
			wantURL: 2,
			wantTL:  0,
		},
		{
			name: "message with no URLs",
			msg: &tb.Message{
				Text:     "Hello world",
				Entities: []tb.MessageEntity{},
			},
			wantURL: 0,
			wantTL:  0,
		},
		{
			name: "message with preview options",
			msg: &tb.Message{
				Text: "Check this out",
				PreviewOptions: &tb.PreviewOptions{
					Disabled:   false,
					URL:        "https://preview.com",
					LargeMedia: true,
				},
			},
			wantURL: 0,
			wantTL:  0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := extractInstantViewInfo(tt.msg)

			assert.True(t, result.Found)
			assert.Len(t, result.URLs, tt.wantURL)
			if tt.wantTL > 0 {
				assert.Len(t, result.TextLinks, tt.wantTL)
			}

			// Check preview options if present
			if tt.msg.PreviewOptions != nil {
				require.NotNil(t, result.PreviewInfo)
				assert.Equal(t, tt.msg.PreviewOptions.URL, result.PreviewInfo.URL)
				assert.Equal(t, tt.msg.PreviewOptions.LargeMedia, result.PreviewInfo.LargeMedia)
			}
		})
	}
}

func TestFormatInstantViewForDisplay(t *testing.T) {
	tests := []struct {
		name   string
		result getInstantViewResult
		want   string
	}{
		{
			name: "not found",
			result: getInstantViewResult{
				Found: false,
				Error: "message not found",
			},
			want: "Message not found: message not found",
		},
		{
			name: "with URLs",
			result: getInstantViewResult{
				Found: true,
				URLs: []instantViewURL{
					{URL: "https://example.com", Offset: 0, Length: 19},
				},
			},
			want: "URLs found:\n  1. https://example.com\n",
		},
		{
			name: "no URLs found",
			result: getInstantViewResult{
				Found: true,
				URLs:  []instantViewURL{},
			},
			want: "No URLs or links found in the message",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := FormatInstantViewForDisplay(tt.result)
			assert.Equal(t, tt.want, got)
		})
	}
}
