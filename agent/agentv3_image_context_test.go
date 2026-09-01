//go:build !386 && !arm

package agentv3

import (
	"strings"
	"testing"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3ImageRefsPersistInPromptContext(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(_ *TurnContext, _ *tb.Photo) (string, error) {
		t.Fatal("agent-v3 should not encode images when multimodal input is disabled")
		return "", nil
	}
	t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })

	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Text:  "current input",
			Photo: &tb.Photo{File: tb.File{FileID: "current-file"}},
			ReplyTo: &tb.Message{
				ID:     20,
				Sender: &tb.User{Username: "alice"},
				Photo:  &tb.Photo{File: tb.File{FileID: "reply-file"}},
			},
		},
		V3: &AgentV3TurnState{},
	}
	history := &RichHistory{FullMessages: []*tb.Message{{
		ID:    10,
		Photo: &tb.Photo{File: tb.File{FileID: "history-file"}},
	}}}

	msg, err := buildAgentV3UserMessage(&CompiledAgent{}, tc, history, nil)
	require.NoError(t, err)
	require.Len(t, tc.V3.ImageRefs, 3)
	assert.Contains(t, msg.Content, "file_id: current-file")
	assert.Contains(t, msg.Content, "file_id: reply-file")
	assert.Contains(t, msg.Content, "file_id: history-file")
	assert.Equal(t, 1, strings.Count(msg.Content, "<telegram_image_refs>"))
	assert.NotContains(t, msg.Content, "This message contains an attached image")

	turns := agentV3TurnsToMessages([]orm.AgentV3Turn{
		{Role: string(schema.User), Content: "remember this image", ImageRefs: tc.V3.ImageRefs},
		{Role: string(schema.Assistant), Content: "acknowledged"},
	})
	require.Len(t, turns, 2)
	assert.Contains(t, turns[0].Content, "get_image or analyze_image")
	assert.Contains(t, turns[0].Content, "file_id: current-file")
	assert.Contains(t, turns[0].Content, "file_id: reply-file")
	assert.Contains(t, turns[0].Content, "file_id: history-file")
	assert.Equal(t, "acknowledged", turns[1].Content)

	summary := summarizeAgentV3Turns([]orm.AgentV3Turn{{
		Role:      string(schema.User),
		Content:   "remember this image",
		ImageRefs: tc.V3.ImageRefs,
	}}, 100)
	assert.Contains(t, summary, "file_id=current-file")
	assert.Contains(t, summary, "file_id=reply-file")
	assert.Contains(t, summary, "file_id=history-file")
}

func TestBuildAgentV3UserMessageSkipsRawHistoryImageRefs(t *testing.T) {
	const fileID = "raw-history-file"
	rawTurns := []orm.AgentV3Turn{{
		Role:      string(schema.User),
		Content:   "earlier image",
		ImageRefs: []orm.AgentV3ImageRef{{MessageID: 10, FileID: fileID}},
	}}
	tc := &TurnContext{
		Message: &tb.Message{ID: 30, Text: "current input"},
		V3:      &AgentV3TurnState{},
	}
	history := &RichHistory{FullMessages: []*tb.Message{{
		ID:    10,
		Photo: &tb.Photo{File: tb.File{FileID: fileID}},
	}}}

	userMsg, err := buildAgentV3UserMessage(&CompiledAgent{}, tc, history, rawTurns)
	require.NoError(t, err)
	assert.Empty(t, tc.V3.ImageRefs)
	messages := buildAgentV3TurnMessages("prefix", "", "", nil, rawTurns, userMsg)
	contents := make([]string, 0, len(messages))
	for _, message := range messages {
		contents = append(contents, message.Content)
	}
	assert.Equal(t, 1, strings.Count(strings.Join(contents, "\n"), "file_id: "+fileID))

	for name, message := range map[string]*tb.Message{
		"current": {
			ID:    31,
			Text:  "current image",
			Photo: &tb.Photo{File: tb.File{FileID: fileID}},
		},
		"reply": {
			ID:   32,
			Text: "reply image",
			ReplyTo: &tb.Message{
				ID:     20,
				Sender: &tb.User{Username: "alice"},
				Photo:  &tb.Photo{File: tb.File{FileID: fileID}},
			},
		},
	} {
		t.Run(name, func(t *testing.T) {
			tc := &TurnContext{Message: message, V3: &AgentV3TurnState{}}
			_, err := buildAgentV3UserMessage(&CompiledAgent{}, tc, nil, rawTurns)
			require.NoError(t, err)
			require.Equal(t, []orm.AgentV3ImageRef{{MessageID: map[string]int{"current": 31, "reply": 20}[name], FileID: fileID}}, tc.V3.ImageRefs)
		})
	}
}

func TestBuildAgentV3UserMessageKeepsFailedImageAsRefWithoutImagePart(t *testing.T) {
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(_ *TurnContext, photo *tb.Photo) (string, error) {
		if photo.FileID == "reply-file" {
			return "", errTestImageContextBoom
		}
		return "data:image/jpeg;base64," + photo.FileID, nil
	}
	t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })

	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Text:  "current input",
			Photo: &tb.Photo{File: tb.File{FileID: "current-file"}},
			ReplyTo: &tb.Message{
				ID:     20,
				Sender: &tb.User{Username: "alice"},
				Photo:  &tb.Photo{File: tb.File{FileID: "reply-file"}},
			},
		},
		Config: &config.AgentConfig{
			Model:    &config.Model{Features: config.ModelFeatures{Image: true}},
			Features: config.FeatureSetting{Image: true},
		},
		V3: &AgentV3TurnState{},
	}

	msg, err := buildAgentV3UserMessage(&CompiledAgent{}, tc, nil, nil)
	require.NoError(t, err)
	require.Len(t, tc.V3.ImageRefs, 2)
	require.Len(t, msg.UserInputMultiContent, 2)
	assert.Equal(t, "data:image/jpeg;base64,current-file", *msg.UserInputMultiContent[1].Image.URL)
	assert.Contains(t, msg.UserInputMultiContent[0].Text, "file_id: current-file")
	assert.Contains(t, msg.UserInputMultiContent[0].Text, "file_id: reply-file")
	assert.NotContains(t, msg.UserInputMultiContent[0].Text, "(file_id: ")
}

func TestBuildAgentV3UserMessageDropsEmptyImageFileID(t *testing.T) {
	tc := &TurnContext{
		Message: &tb.Message{
			ID:    30,
			Text:  "current input",
			Photo: &tb.Photo{},
		},
		V3: &AgentV3TurnState{},
	}

	msg, err := buildAgentV3UserMessage(&CompiledAgent{}, tc, nil, nil)
	require.NoError(t, err)
	assert.Empty(t, tc.V3.ImageRefs)
	assert.NotContains(t, msg.Content, "file_id:")
	assert.NotContains(t, msg.Content, "<telegram_image_refs>")
}

func TestAgentV3ImageRefsAreDeduplicatedAndBudgeted(t *testing.T) {
	turn := orm.AgentV3Turn{
		Role:    string(schema.User),
		Content: "image question",
		ImageRefs: []orm.AgentV3ImageRef{
			{MessageID: 1, FileID: "duplicate-file"},
			{MessageID: 2, FileID: "duplicate-file"},
			{MessageID: 3, FileID: ""},
		},
	}
	messages := agentV3TurnsToMessages([]orm.AgentV3Turn{turn})
	require.Len(t, messages, 1)
	assert.Equal(t, 1, strings.Count(messages[0].Content, "file_id: duplicate-file"))
	assert.NotContains(t, messages[0].Content, "file_id: \n")

	summary := summarizeAgentV3Turns([]orm.AgentV3Turn{turn, {
		Role:      string(schema.User),
		Content:   "same image again",
		ImageRefs: []orm.AgentV3ImageRef{{MessageID: 4, FileID: "duplicate-file"}},
	}}, 100)
	assert.Equal(t, 1, strings.Count(summary, "file_id=duplicate-file"))

	shortSummary := summarizeAgentV3Turns([]orm.AgentV3Turn{{
		Role:      string(schema.User),
		Content:   "question",
		ImageRefs: []orm.AgentV3ImageRef{{MessageID: 1, FileID: "complete-file-id"}},
	}}, 10)
	assert.NotContains(t, shortSummary, "complete-file-id")
	assert.NotContains(t, shortSummary, "complete-file")

	longSummary := summarizeAgentV3Turns([]orm.AgentV3Turn{{
		Role:      string(schema.User),
		Content:   "question",
		ImageRefs: []orm.AgentV3ImageRef{{MessageID: 1, FileID: "complete-file-id"}},
	}}, 100)
	assert.Contains(t, longSummary, "file_id=complete-file-id")

	trimmed := trimAgentV3TurnsByMaxChars([]orm.AgentV3Turn{
		turn,
		{Role: string(schema.Assistant), Content: "0123456789"},
	}, len("image question")+len("0123456789"))
	require.Len(t, trimmed, 1)
	assert.Equal(t, string(schema.Assistant), trimmed[0].Role)
}

func TestAgentV3TurnsWithoutImageRefsKeepExistingText(t *testing.T) {
	turns := agentV3TurnsToMessages([]orm.AgentV3Turn{
		{Role: string(schema.User), Content: "plain user"},
		{Role: string(schema.Assistant), Content: "plain assistant"},
	})
	require.Len(t, turns, 2)
	assert.Equal(t, "plain user", turns[0].Content)
	assert.Equal(t, "plain assistant", turns[1].Content)
	assert.Equal(t, "- user: plain user\n- assistant: plain assistant", summarizeAgentV3Turns([]orm.AgentV3Turn{
		{Role: string(schema.User), Content: "plain user"},
		{Role: string(schema.Assistant), Content: "plain assistant"},
	}, 100))
}
