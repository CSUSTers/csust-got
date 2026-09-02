package agentv3

import (
	"csust-got/util"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestGetMessageTextWithEntities(t *testing.T) {
	tests := []struct {
		name       string
		msg        *tb.Message
		htmlFormat bool
		expected   string
	}{
		{
			name:       "nil message",
			msg:        nil,
			htmlFormat: false,
			expected:   "",
		},
		{
			name: "empty message",
			msg: &tb.Message{
				Text:     "",
				Entities: nil,
			},
			htmlFormat: false,
			expected:   "",
		},
		{
			name: "plain text without entities",
			msg: &tb.Message{
				Text:     "Hello world",
				Entities: nil,
			},
			htmlFormat: false,
			expected:   "Hello world",
		},
		{
			name: "text with text link entity - markdown format",
			msg: &tb.Message{
				Text: "Check out Google",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 10,
						Length: 6,
						URL:    "https://google.com",
					},
				},
			},
			htmlFormat: false,
			expected:   "Check out [Google](https://google.com)",
		},
		{
			name: "text with text link entity - HTML format",
			msg: &tb.Message{
				Text: "Check out Google",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 10,
						Length: 6,
						URL:    "https://google.com",
					},
				},
			},
			htmlFormat: true,
			expected:   `Check out <a href="https://google.com">Google</a>`,
		},
		{
			name: "text with bare URL entity",
			msg: &tb.Message{
				Text: "Visit https://example.com today",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityURL,
						Offset: 6,
						Length: 19,
					},
				},
			},
			htmlFormat: false,
			expected:   "Visit https://example.com today",
		},
		{
			name: "text with bold entity - markdown format",
			msg: &tb.Message{
				Text: "This is bold text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityBold,
						Offset: 8,
						Length: 4,
					},
				},
			},
			htmlFormat: false,
			expected:   "This is **bold** text",
		},
		{
			name: "text with bold entity - HTML format",
			msg: &tb.Message{
				Text: "This is bold text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityBold,
						Offset: 8,
						Length: 4,
					},
				},
			},
			htmlFormat: true,
			expected:   "This is <b>bold</b> text",
		},
		{
			name: "text with italic entity - markdown format",
			msg: &tb.Message{
				Text: "This is italic text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityItalic,
						Offset: 8,
						Length: 6,
					},
				},
			},
			htmlFormat: false,
			expected:   "This is *italic* text",
		},
		{
			name: "text with italic entity - HTML format",
			msg: &tb.Message{
				Text: "This is italic text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityItalic,
						Offset: 8,
						Length: 6,
					},
				},
			},
			htmlFormat: true,
			expected:   "This is <i>italic</i> text",
		},
		{
			name: "text with code entity - markdown format",
			msg: &tb.Message{
				Text: "Run the command ls -la now",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityCode,
						Offset: 16,
						Length: 6, // "ls -la" is 6 characters
					},
				},
			},
			htmlFormat: false,
			expected:   "Run the command `ls -la` now",
		},
		{
			name: "text with code entity - HTML format",
			msg: &tb.Message{
				Text: "Run the command ls -la now",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityCode,
						Offset: 16,
						Length: 6, // "ls -la" is 6 characters
					},
				},
			},
			htmlFormat: true,
			expected:   "Run the command <code>ls -la</code> now",
		},
		{
			name: "text with mention entity - markdown format",
			msg: &tb.Message{
				Text: "Hello @username how are you?",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityMention,
						Offset: 6,
						Length: 9,
					},
				},
			},
			htmlFormat: false,
			expected:   "Hello [@username](tg:username) how are you?",
		},
		{
			name: "text with mention entity - HTML format",
			msg: &tb.Message{
				Text: "Hello @username how are you?",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityMention,
						Offset: 6,
						Length: 9,
					},
				},
			},
			htmlFormat: true,
			expected:   `Hello <a href="tg:username">@username</a> how are you?`,
		},
		{
			name: "text with hashtag entity",
			msg: &tb.Message{
				Text: "Check out this #golang tip",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityHashtag,
						Offset: 15,
						Length: 7,
					},
				},
			},
			htmlFormat: false,
			expected:   "Check out this #golang tip",
		},
		{
			name: "text with multiple entities",
			msg: &tb.Message{
				Text: "Visit Google and check out @golang for bold tips",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 6,
						Length: 6,
						URL:    "https://google.com",
					},
					{
						Type:   tb.EntityMention,
						Offset: 27,
						Length: 7,
					},
					{
						Type:   tb.EntityBold,
						Offset: 39,
						Length: 4,
					},
				},
			},
			htmlFormat: false,
			expected:   "Visit [Google](https://google.com) and check out [@golang](tg:golang) for **bold** tips",
		},
		{
			name: "text with multiple entities - HTML format",
			msg: &tb.Message{
				Text: "Visit Google and check out @golang for bold tips",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 6,
						Length: 6,
						URL:    "https://google.com",
					},
					{
						Type:   tb.EntityMention,
						Offset: 27,
						Length: 7,
					},
					{
						Type:   tb.EntityBold,
						Offset: 39,
						Length: 4,
					},
				},
			},
			htmlFormat: true,
			expected:   `Visit <a href="https://google.com">Google</a> and check out <a href="tg:golang">@golang</a> for <b>bold</b> tips`,
		},
		{
			name: "markdown entity after emoji uses UTF-16 offsets",
			msg: &tb.Message{
				Text: "😀 bold",
				Entities: []tb.MessageEntity{
					{Type: tb.EntityBold, Offset: 3, Length: 4},
				},
			},
			htmlFormat: false,
			expected:   "😀 **bold**",
		},
		{
			name: "nested markdown entities",
			msg: &tb.Message{
				Text: "bold italic",
				Entities: []tb.MessageEntity{
					{Type: tb.EntityBold, Offset: 0, Length: 11},
					{Type: tb.EntityItalic, Offset: 5, Length: 6},
				},
			},
			htmlFormat: false,
			expected:   "**bold *italic***",
		},
		{
			name: "caption with entities",
			msg: &tb.Message{
				Text:    "",
				Caption: "Photo caption with Google link",
				CaptionEntities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 19,
						Length: 6,
						URL:    "https://google.com",
					},
				},
			},
			htmlFormat: false,
			expected:   "Photo caption with [Google](https://google.com) link",
		},
		{
			name: "HTML escaping in HTML format",
			msg: &tb.Message{
				Text: "Link to script tag here",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTextLink,
						Offset: 8,
						Length: 10, // "script tag" is 10 characters
						URL:    "https://example.com?test=<script>",
					},
				},
			},
			htmlFormat: true,
			expected:   `Link to <a href="https://example.com?test=&lt;script&gt;">script tag</a> here`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getMessageTextWithEntities(tt.msg, tt.htmlFormat)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestGetTextSubstring(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		start    int
		end      int
		expected string
	}{
		{
			name:     "normal substring",
			text:     "hello world",
			start:    0,
			end:      5,
			expected: "hello",
		},
		{
			name:     "substring from middle",
			text:     "hello world",
			start:    6,
			end:      11,
			expected: "world",
		},
		{
			name:     "unicode text",
			text:     "你好世界",
			start:    0,
			end:      2,
			expected: "你好",
		},
		{
			name:     "negative start",
			text:     "hello",
			start:    -1,
			end:      3,
			expected: "hel",
		},
		{
			name:     "end beyond text length",
			text:     "hello",
			start:    2,
			end:      10,
			expected: "llo",
		},
		{
			name:     "start equals end",
			text:     "hello",
			start:    2,
			end:      2,
			expected: "",
		},
		{
			name:     "start greater than end",
			text:     "hello",
			start:    5,
			end:      2,
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getTextSubstring(tt.text, tt.start, tt.end)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test additional entity types according to Telegram API documentation
func TestGetMessageTextWithAdditionalEntities(t *testing.T) {
	tests := []struct {
		name       string
		msg        *tb.Message
		htmlFormat bool
		expected   string
	}{
		{
			name: "underline entity - markdown format",
			msg: &tb.Message{
				Text: "This is underlined text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityUnderline,
						Offset: 8,
						Length: 10,
					},
				},
			},
			htmlFormat: false,
			expected:   "This is __underlined__ text",
		},
		{
			name: "underline entity - HTML format",
			msg: &tb.Message{
				Text: "This is underlined text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityUnderline,
						Offset: 8,
						Length: 10,
					},
				},
			},
			htmlFormat: true,
			expected:   "This is <u>underlined</u> text",
		},
		{
			name: "strikethrough entity - markdown format",
			msg: &tb.Message{
				Text: "This is strikethrough text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityStrikethrough,
						Offset: 8,
						Length: 13,
					},
				},
			},
			htmlFormat: false,
			expected:   "This is ~~strikethrough~~ text",
		},
		{
			name: "spoiler entity - markdown format",
			msg: &tb.Message{
				Text: "This is spoiler text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntitySpoiler,
						Offset: 8,
						Length: 7,
					},
				},
			},
			htmlFormat: false,
			expected:   "This is ||spoiler|| text",
		},
		{
			name: "spoiler entity - HTML format",
			msg: &tb.Message{
				Text: "This is spoiler text",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntitySpoiler,
						Offset: 8,
						Length: 7,
					},
				},
			},
			htmlFormat: true,
			expected:   `This is <span class="tg-spoiler">spoiler</span> text`,
		},
		{
			name: "code block entity with language - markdown format",
			msg: &tb.Message{
				Text: "Here is some Go code:\nfunc main() {\n    fmt.Println(\"Hello\")\n}",
				Entities: []tb.MessageEntity{
					{
						Type:     tb.EntityCodeBlock,
						Offset:   22,
						Length:   40,
						Language: "go",
					},
				},
			},
			htmlFormat: false,
			expected:   "Here is some Go code:\n```go\nfunc main() {\n    fmt.Println(\"Hello\")\n}\n```",
		},
		{
			name: "code block entity with language - HTML format",
			msg: &tb.Message{
				Text: "Here is some Go code:\nfunc main() {\n    fmt.Println(\"Hello\")\n}",
				Entities: []tb.MessageEntity{
					{
						Type:     tb.EntityCodeBlock,
						Offset:   22,
						Length:   40,
						Language: "go",
					},
				},
			},
			htmlFormat: true,
			expected: `Here is some Go code:
<pre><code class="language-go">func main() {
    fmt.Println(&#34;Hello&#34;)
}</code></pre>`,
		},
		{
			name: "blockquote entity - HTML format",
			msg: &tb.Message{
				Text: "This is a quote: Important message here",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityBlockquote,
						Offset: 17,
						Length: 22,
					},
				},
			},
			htmlFormat: true,
			expected:   "This is a quote: <blockquote>Important message here</blockquote>",
		},
		{
			name: "text mention entity - markdown format",
			msg: &tb.Message{
				Text: "Hello John Doe",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTMention,
						Offset: 6,
						Length: 8,
						User: &tb.User{
							ID:        12345,
							FirstName: "John",
							LastName:  "Doe",
						},
					},
				},
			},
			htmlFormat: false,
			expected:   "Hello [John Doe](tg:user?id=12345)",
		},
		{
			name: "text mention entity - HTML format",
			msg: &tb.Message{
				Text: "Hello John Doe",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityTMention,
						Offset: 6,
						Length: 8,
						User: &tb.User{
							ID:        12345,
							FirstName: "John",
							LastName:  "Doe",
						},
					},
				},
			},
			htmlFormat: true,
			expected:   `Hello <a href="tg:user?id=12345">John Doe</a>`,
		},
		{
			name: "cashtag entity",
			msg: &tb.Message{
				Text: "Buy $AAPL stock now",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityCashtag,
						Offset: 4,
						Length: 5,
					},
				},
			},
			htmlFormat: false,
			expected:   "Buy $AAPL stock now",
		},
		{
			name: "bot command entity",
			msg: &tb.Message{
				Text: "Use /start to begin",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityCommand,
						Offset: 4,
						Length: 6,
					},
				},
			},
			htmlFormat: false,
			expected:   "Use /start to begin",
		},
		{
			name: "phone number entity",
			msg: &tb.Message{
				Text: "Call me at +1-212-555-0123",
				Entities: []tb.MessageEntity{
					{
						Type:   tb.EntityPhone,
						Offset: 11,
						Length: 15,
					},
				},
			},
			htmlFormat: false,
			expected:   "Call me at +1-212-555-0123",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := getMessageTextWithEntities(tt.msg, tt.htmlFormat)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// Test the integration with context message creation
func TestContextMessageWithEntities(t *testing.T) {
	// Create a mock message with entities
	mockMsg := &tb.Message{
		ID:       123,
		Unixtime: time.Now().Unix(),
		Text:     "Check out Google for more info",
		Entities: []tb.MessageEntity{
			{
				Type:   tb.EntityTextLink,
				Offset: 10,
				Length: 6,
				URL:    "https://google.com",
			},
		},
		Sender: &tb.User{
			ID:        1,
			Username:  "testuser",
			FirstName: "Test",
			LastName:  "User",
		},
	}

	// Test that the context message creation uses the formatted text.
	contextMsg := contextMessageFromTelegram(mockMsg)
	require.NotNil(t, contextMsg)

	assert.Equal(t, "Check out [Google](https://google.com) for more info", contextMsg.Text)
	assert.Equal(t, 123, contextMsg.ID)
	assert.Equal(t, "testuser", contextMsg.User)
	assert.Equal(t, mockMsg.Unixtime, contextMsg.Unixtime)
}

func TestGetReplyChainIncludesMessageTypeAndMediaMetadata(t *testing.T) {
	tests := []struct {
		name     string
		message  *tb.Message
		contains []string
	}{
		{
			name: "text",
			message: &tb.Message{
				ID: 1, Text: "hello", Sender: &tb.User{Username: "alice"},
			},
			contains: []string{`type="text"`, "hello"},
		},
		{
			name: "captioned photo",
			message: &tb.Message{
				ID: 2, Caption: "diagram", Photo: &tb.Photo{File: tb.File{FileID: "photo-captioned"}}, Sender: &tb.User{Username: "alice"},
			},
			contains: []string{`type="photo"`, `<image file_id="photo-captioned" />`, "diagram"},
		},
		{
			name: "captionless photo",
			message: &tb.Message{
				ID: 3, Photo: &tb.Photo{File: tb.File{FileID: "photo-captionless"}}, Sender: &tb.User{Username: "alice"},
			},
			contains: []string{`type="photo"`, `<image file_id="photo-captionless" />`},
		},
		{
			name: "sticker",
			message: &tb.Message{
				ID: 4, Sticker: &tb.Sticker{File: tb.File{FileID: "sticker-file"}, Emoji: "🙂"}, Sender: &tb.User{Username: "alice"},
			},
			contains: []string{`type="sticker"`, "🙂"},
		},
		{
			name: "document",
			message: &tb.Message{
				ID: 5, Document: &tb.Document{File: tb.File{FileID: "document-file"}, FileName: "notes.pdf"}, Sender: &tb.User{Username: "alice"},
			},
			contains: []string{`type="document"`, "notes.pdf"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			messages, err := getReplyChain(nil, tt.message, 3)
			assert.NoError(t, err)
			output := FormatContextMessagesWithXml(messages)
			for _, expected := range tt.contains {
				assert.Contains(t, output, expected)
			}
		})
	}
}

// Test the new nested XML format functionality
func TestFormatContextMessagesWithXml(t *testing.T) {
	tests := []struct {
		name     string
		messages []*ContextMessage
		expected string
	}{
		{
			name:     "empty messages",
			messages: []*ContextMessage{},
			expected: "",
		},
		{
			name: "single message without reply",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Hello world",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    Hello world
  </message>
</messages>
`,
		},
		{
			name: "simple reply chain",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Original message",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
				{
					ID:      2,
					Text:    "Reply to original",
					User:    "user2",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Jane",
						Last:  "Smith",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    Original message
    <message id="2" username="user2" showname="Jane Smith" type="text" replyTo="1">
      Reply to original
    </message>
  </message>
</messages>
`,
		},
		{
			name: "multiple level nesting",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Root message",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
				{
					ID:      2,
					Text:    "Reply to root",
					User:    "user2",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Jane",
						Last:  "Smith",
					},
				},
				{
					ID:      3,
					Text:    "Reply to reply",
					User:    "user3",
					ReplyTo: new(2),
					UserNames: userNames{
						First: "Bob",
						Last:  "Johnson",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    Root message
    <message id="2" username="user2" showname="Jane Smith" type="text" replyTo="1">
      Reply to root
      <message id="3" username="user3" showname="Bob Johnson" type="text" replyTo="2">
        Reply to reply
      </message>
    </message>
  </message>
</messages>
`,
		},
		{
			name: "multiple replies to same message",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Original message",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
				{
					ID:      2,
					Text:    "First reply",
					User:    "user2",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Jane",
						Last:  "Smith",
					},
				},
				{
					ID:      3,
					Text:    "Second reply",
					User:    "user3",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Bob",
						Last:  "Johnson",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    Original message
    <message id="2" username="user2" showname="Jane Smith" type="text" replyTo="1">
      First reply
    </message>
    <message id="3" username="user3" showname="Bob Johnson" type="text" replyTo="1">
      Second reply
    </message>
  </message>
</messages>
`,
		},
		{
			name: "multiple root messages",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "First root",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
				{
					ID:   2,
					Text: "Second root",
					User: "user2",
					UserNames: userNames{
						First: "Jane",
						Last:  "Smith",
					},
				},
				{
					ID:      3,
					Text:    "Reply to first root",
					User:    "user3",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Bob",
						Last:  "Johnson",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    First root
    <message id="3" username="user3" showname="Bob Johnson" type="text" replyTo="1">
      Reply to first root
    </message>
  </message>
  <message id="2" username="user2" showname="Jane Smith" type="text">
    Second root
  </message>
</messages>
`,
		},
		{
			name: "HTML escaping in nested format",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Message with <script>alert('xss')</script> & other HTML",
					User: "user1",
					UserNames: userNames{
						First: "John",
						Last:  "Doe",
					},
				},
				{
					ID:      2,
					Text:    "Reply with & more <tags>",
					User:    "user2",
					ReplyTo: new(1),
					UserNames: userNames{
						First: "Jane",
						Last:  "Smith",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="John Doe" type="text">
    Message with &lt;script&gt;alert(&#39;xss&#39;)&lt;/script&gt; &amp; other HTML
    <message id="2" username="user2" showname="Jane Smith" type="text" replyTo="1">
      Reply with &amp; more &lt;tags&gt;
    </message>
  </message>
</messages>
`,
		},
		{
			name: "empty usernames",
			messages: []*ContextMessage{
				{
					ID:   1,
					Text: "Message from user with no name",
					User: "user1",
					UserNames: userNames{
						First: "",
						Last:  "",
					},
				},
			},
			expected: `<messages>
  <message id="1" username="user1" showname="" type="text">
    Message from user with no name
  </message>
</messages>
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatContextMessagesWithXml(tt.messages)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatContextMessageTime(t *testing.T) {
	now := time.Date(2026, time.September, 1, 10, 30, 0, 0, util.TimeZoneCST)

	assert.Equal(t, "08:15:42", formatContextMessageTime(time.Date(2026, time.September, 1, 8, 15, 42, 0, util.TimeZoneCST).Unix(), now))
	assert.Equal(t, "2026-08-31 23:59:59", formatContextMessageTime(time.Date(2026, time.August, 31, 23, 59, 59, 0, util.TimeZoneCST).Unix(), now))
	assert.Empty(t, formatContextMessageTime(0, now))
}

func TestContextMessageFormatIncludesTime(t *testing.T) {
	messageTime := time.Now().Unix()
	expectedTime := time.Unix(messageTime, 0).In(util.TimeZoneCST).Format("15:04:05")
	messages := []*ContextMessage{{ID: 1, Unixtime: messageTime, Text: "hello"}}

	assert.Contains(t, FormatContextMessages(messages), expectedTime)
	assert.Contains(t, FormatContextMessagesWithXml(messages), `time="`+expectedTime+`"`)
	assert.NotContains(t, FormatContextMessagesWithXml([]*ContextMessage{{ID: 2, Text: "no time"}}), "time=")
}

func TestFormatContextMessagesWithXmlRendersReplyWhoseParentIsAbsent(t *testing.T) {
	parentID := 1
	output := FormatContextMessagesWithXml([]*ContextMessage{{ID: 2, ReplyTo: &parentID, Text: "orphaned reply"}})

	assert.Contains(t, output, `<message id="2"`)
	assert.Contains(t, output, `replyTo="1"`)
	assert.Contains(t, output, "orphaned reply")
}
