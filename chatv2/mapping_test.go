//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"testing"
	"text/template"
	"time"

	"csust-got/chat"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	tb "gopkg.in/telebot.v3"
)

func TestExtractInput(t *testing.T) {
	tests := []struct {
		name    string
		msg     *tb.Message
		trigger []*config.ChatTrigger
		want    string
	}{
		{
			name: "plain text message",
			msg:  &tb.Message{Text: "hello world"},
			want: "hello world",
		},
		{
			name: "caption when text empty",
			msg:  &tb.Message{Caption: "image caption"},
			want: "image caption",
		},
		{
			name:    "command trigger uses payload",
			msg:     &tb.Message{Text: "/ask how are you", Payload: "how are you"},
			trigger: []*config.ChatTrigger{{Command: "ask"}},
			want:    "how are you",
		},
		{
			name:    "command trigger with empty payload",
			msg:     &tb.Message{Text: "/ask", Payload: ""},
			trigger: []*config.ChatTrigger{{Command: "ask"}},
			want:    "",
		},
		{
			name: "slash prefix stripped for non-command trigger",
			msg:  &tb.Message{Text: "/unknown hello world"},
			want: "hello world",
		},
		{
			name: "slash only returns empty",
			msg:  &tb.Message{Text: "/justcommand"},
			want: "",
		},
		{
			name:    "nil trigger falls through",
			msg:     &tb.Message{Text: "hello world"},
			trigger: []*config.ChatTrigger{nil},
			want:    "hello world",
		},
		{
			name:    "regex trigger no command",
			msg:     &tb.Message{Text: "hello world"},
			trigger: []*config.ChatTrigger{{Regex: "hello"}},
			want:    "hello world",
		},
		{
			name: "whitespace trimmed",
			msg:  &tb.Message{Text: "  hello  "},
			want: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractInput(tt.msg, tt.trigger...)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestBuildPromptDataIncludesCurrentDateCN(t *testing.T) {
	tc := &TurnContext{
		Message: &tb.Message{Text: "hello"},
	}

	pd := buildPromptData(tc, nil)

	assert.Equal(t, beijingNow().Format("2006年01月02日"), pd.CurrentDateCN)
	_, err := time.ParseInLocation("2006年01月02日", pd.CurrentDateCN, beijingFallbackLocation)
	assert.NoError(t, err)
}

func TestPromptTemplateCanAccessCurrentDateCN(t *testing.T) {
	tc := &TurnContext{
		Message: &tb.Message{Text: "hello"},
	}

	pd := buildPromptData(tc, nil)
	tpl := template.Must(template.New("system").Parse("现在是北京时间{{ .CurrentDateCN }}"))

	var buf bytes.Buffer
	err := tpl.Execute(&buf, pd)

	assert.NoError(t, err)
	assert.Contains(t, buf.String(), pd.CurrentDateCN)
}

func TestContextToSchemaMessages(t *testing.T) {
	tests := []struct {
		name        string
		msgs        []*chat.ContextMessage
		botUsername string
		wantLen     int
		wantRoles   []schema.RoleType
	}{
		{
			name:    "nil messages returns nil",
			msgs:    nil,
			wantLen: 0,
		},
		{
			name: "user messages with sender info",
			msgs: []*chat.ContextMessage{
				{ID: 1, User: "12345", Text: "hello"},
				{ID: 2, User: "67890", Text: "world"},
			},
			wantLen:   2,
			wantRoles: []schema.RoleType{schema.User, schema.User},
		},
		{
			name:        "empty bot username never matches",
			botUsername: "",
			msgs: []*chat.ContextMessage{
				{ID: 1, Text: "hello"},
			},
			wantLen:   1,
			wantRoles: []schema.RoleType{schema.User},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tc := &TurnContext{
				BotUser: &tb.User{Username: tt.botUsername},
			}
			result := contextToSchemaMessages(tt.msgs, tc)
			assert.Len(t, result, tt.wantLen)
			for i, role := range tt.wantRoles {
				assert.Equal(t, role, result[i].Role)
			}
		})
	}
}

func TestBuildUserMessage(t *testing.T) {
	tests := []struct {
		name         string
		text         string
		replyPhoto   *tb.Photo
		wantContains string
		wantRole     schema.RoleType
	}{
		{
			name:         "plain text message",
			text:         "hello",
			wantContains: "hello",
			wantRole:     schema.User,
		},
		{
			name:         "with reply photo adds hint",
			text:         "describe this",
			replyPhoto:   &tb.Photo{File: tb.File{FileID: "abc123"}},
			wantContains: "abc123",
			wantRole:     schema.User,
		},
		{
			name:         "no reply no photo hint",
			text:         "hello",
			wantContains: "hello",
			wantRole:     schema.User,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := &tb.Message{}
			if tt.replyPhoto != nil {
				msg.ReplyTo = &tb.Message{Photo: tt.replyPhoto}
			}
			tc := &TurnContext{Message: msg}
			result := buildUserMessage(tt.text, tc, nil)
			assert.Equal(t, tt.wantRole, result.Role)
			assert.Contains(t, result.Content, tt.wantContains)
		})
	}
}

func TestBuildMessagesForSubAgent(t *testing.T) {
	tests := []struct {
		name          string
		systemPrompt  string
		userInput     string
		imageData     string
		wantLen       int
		wantFirstRole schema.RoleType
	}{
		{
			name:          "text only no system",
			userInput:     "hello",
			wantLen:       1,
			wantFirstRole: schema.User,
		},
		{
			name:          "with system prompt",
			systemPrompt:  "you are helpful",
			userInput:     "hello",
			wantLen:       2,
			wantFirstRole: schema.System,
		},
		{
			name:          "with image data",
			systemPrompt:  "analyze",
			userInput:     "describe",
			imageData:     "data:image/png;base64,abc",
			wantLen:       2,
			wantFirstRole: schema.System,
		},
		{
			name:          "image without system",
			userInput:     "describe",
			imageData:     "data:image/png;base64,abc",
			wantLen:       1,
			wantFirstRole: schema.User,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := BuildMessagesForSubAgent(tt.systemPrompt, tt.userInput, tt.imageData)
			assert.Len(t, result, tt.wantLen)
			assert.Equal(t, tt.wantFirstRole, result[0].Role)

			// Check multimodal content when image is present
			if tt.imageData != "" {
				lastMsg := result[len(result)-1]
				assert.NotEmpty(t, lastMsg.UserInputMultiContent)
				assert.Equal(t, schema.ChatMessagePartTypeImageURL, lastMsg.UserInputMultiContent[1].Type)
			}
		})
	}
}
