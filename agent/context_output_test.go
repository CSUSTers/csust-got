package agentv3

import (
	"context"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func contextOutputFixture(t *testing.T, messages ...*tb.Message) context.Context {
	t.Helper()
	oldConfig := config.BotConfig
	cfg := config.NewBotConfig()
	server := miniredis.RunT(t)
	cfg.RedisConfig.RedisAddr = server.Addr()
	cfg.RedisConfig.KeyPrefix = "context-output:"
	config.BotConfig = cfg
	orm.InitRedis()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})
	chat := &tb.Chat{ID: -100}
	for _, message := range messages {
		message.Chat = chat
		require.NoError(t, orm.PushMessageToStream(message))
		require.NoError(t, orm.SetMessage(message))
	}
	return WithTurnContext(t.Context(), &TurnContext{Message: &tb.Message{ID: 10000, Chat: chat}})
}

func TestGetContextOutputByteBudget(t *testing.T) {
	ctx := contextOutputFixture(t, &tb.Message{
		ID: 9999, Text: strings.Repeat("中文😀<&\"", 4000),
		Sender: &tb.User{Username: "user<&\""}, Unixtime: 1792182150,
	})
	for _, args := range []string{`{}`, `{"limit_from":"latest"}`} {
		output, err := (&getContextTool{}).InvokableRun(ctx, args)
		require.NoError(t, err)
		require.LessOrEqual(t, len(output), 12000, "complete UTF-8/XML tool result must fit byte budget")
		require.True(t, utf8.ValidString(output))
		document := decodeContextOutput(t, output)
		require.Len(t, document.Messages, 1)
		require.Equal(t, "user<&\"", document.Messages[0].User)
		require.True(t, strings.HasPrefix(strings.Repeat("中文😀<&\"", 4000), strings.TrimSpace(document.Messages[0].Text)))
		require.Equal(t, 1, document.Metadata.TruncatedBodies)
		require.Equal(t, len(strings.Repeat("中文😀<&\"", 4000))-len(strings.TrimSpace(document.Messages[0].Text)), document.Metadata.OmittedBodyBytes)
	}
}

type decodedContextMessage struct {
	ID      int    `xml:"id,attr"`
	User    string `xml:"username,attr"`
	Type    string `xml:"type,attr"`
	ReplyTo int    `xml:"replyTo,attr"`
	Time    string `xml:"time,attr"`
	Image   *struct {
		FileID string `xml:"file_id,attr"`
	} `xml:"image"`
	Text    string                  `xml:",chardata"`
	Replies []decodedContextMessage `xml:"message"`
}

type decodedContextOutput struct {
	XMLName  xml.Name                `xml:"messages"`
	Metadata contextOutputMetadata   `xml:"metadata"`
	Messages []decodedContextMessage `xml:"message"`
}

func decodeContextOutput(t *testing.T, output string) decodedContextOutput {
	t.Helper()
	require.LessOrEqual(t, len(output), 12000)
	require.True(t, utf8.ValidString(output))
	var document decodedContextOutput
	require.NoError(t, xml.Unmarshal([]byte(output), &document))
	var flattened []decodedContextMessage
	var flatten func([]decodedContextMessage)
	flatten = func(messages []decodedContextMessage) {
		for _, message := range messages {
			flattened = append(flattened, message)
			flatten(message.Replies)
		}
	}
	flatten(document.Messages)
	document.Messages = flattened
	require.Equal(t, len(flattened), document.Metadata.Count)
	if len(flattened) != 0 {
		minID, maxID := flattened[0].ID, flattened[0].ID
		for _, message := range flattened {
			minID, maxID = min(minID, message.ID), max(maxID, message.ID)
		}
		require.Equal(t, minID, document.Metadata.MinID)
		require.Equal(t, maxID, document.Metadata.MaxID)
	}
	require.Equal(t, "unknown", document.Metadata.HistoryComplete)
	require.Equal(t, "not_guaranteed", document.Metadata.Continuity)
	require.EqualValues(t, 1000, document.Metadata.StreamSnapshotScanLimit)
	return document
}

func contextOutputIDs(document decodedContextOutput) []int {
	ids := make([]int, len(document.Messages))
	for i, message := range document.Messages {
		ids[i] = message.ID
	}
	return ids
}

func contextOutputContinuation(t *testing.T, document decodedContextOutput, direction string) (string, getContextArgs) {
	t.Helper()
	for _, next := range document.Metadata.Continuations {
		if next.Direction == direction {
			require.Equal(t, "get_context", next.Tool)
			var args getContextArgs
			require.NoError(t, json.Unmarshal([]byte(next.Arguments), &args))
			return next.Arguments, args
		}
	}
	t.Fatalf("missing %s continuation", direction)
	return "", getContextArgs{}
}

func TestGetContextOutputContinuationPreservesFiltersAndEndSelection(t *testing.T) {
	var messages []*tb.Message
	for id := 10; id <= 80; id += 10 {
		messages = append(messages, &tb.Message{ID: id, Text: strconv.Itoa(id), Unixtime: time.Now().Unix()})
	}
	ctx := contextOutputFixture(t, messages...)
	for _, from := range []string{"earliest", "latest"} {
		t.Run(from, func(t *testing.T) {
			output, err := (&getContextTool{}).InvokableRun(ctx, fmt.Sprintf(`{"after_message_id":10,"before_message_id":80,"recent_seconds":300,"limit":2,"limit_from":%q}`, from))
			require.NoError(t, err)
			document := decodeContextOutput(t, output)
			require.Equal(t, from, document.Metadata.Selection)
			require.Equal(t, "filtered_cache_and_reply_chain", document.Metadata.CandidateScope)
			require.Equal(t, 6, document.Metadata.CandidateCount)
			require.Equal(t, 20, document.Metadata.CandidateMinID)
			require.Equal(t, 70, document.Metadata.CandidateMaxID)
			require.Equal(t, "4", document.Metadata.OmittedByLimit)
			require.Equal(t, "moving", document.Metadata.RecentWindow)
			direction := "newer"
			if from == "earliest" {
				require.Equal(t, []int{20, 30}, contextOutputIDs(document))
			} else {
				require.Equal(t, []int{60, 70}, contextOutputIDs(document))
				direction = "older"
			}
			nextJSON, nextArgs := contextOutputContinuation(t, document, direction)
			require.EqualValues(t, 300, *nextArgs.RecentSeconds)
			require.Equal(t, 2, nextArgs.Limit)
			if from == "earliest" {
				require.Equal(t, 30, *nextArgs.AfterMessageID)
				require.Equal(t, 80, *nextArgs.BeforeMessageID)
				require.Equal(t, "earliest", *nextArgs.LimitFrom)
			} else {
				require.Equal(t, 10, *nextArgs.AfterMessageID)
				require.Equal(t, 60, *nextArgs.BeforeMessageID)
				require.Equal(t, "latest", *nextArgs.LimitFrom)
			}
			nextOutput, err := (&getContextTool{}).InvokableRun(ctx, nextJSON)
			require.NoError(t, err)
			require.Equal(t, []int{40, 50}, contextOutputIDs(decodeContextOutput(t, nextOutput)))
		})
	}
}

func TestGetContextOutputBudgetKeepsContiguousCandidateEnd(t *testing.T) {
	ctx := contextOutputFixture(t,
		&tb.Message{ID: 100, Text: "oldest"},
		&tb.Message{ID: 200, Text: strings.Repeat("超大😀<&", 4000)},
		&tb.Message{ID: 300, Text: "newest"},
	)
	for _, from := range []string{"earliest", "latest"} {
		output, err := (&getContextTool{}).InvokableRun(ctx, fmt.Sprintf(`{"limit":50,"limit_from":%q}`, from))
		require.NoError(t, err)
		document := decodeContextOutput(t, output)
		expectedID, direction := 100, "newer"
		if from == "latest" {
			expectedID, direction = 300, "older"
		}
		require.Equal(t, []int{expectedID}, contextOutputIDs(document))
		require.Equal(t, 2, document.Metadata.OmittedByBudget)
		require.Zero(t, document.Metadata.TruncatedBodies)
		next, _ := contextOutputContinuation(t, document, direction)
		output, err = (&getContextTool{}).InvokableRun(ctx, next)
		require.NoError(t, err)
		document = decodeContextOutput(t, output)
		require.Equal(t, []int{200}, contextOutputIDs(document))
		require.Equal(t, 1, document.Metadata.TruncatedBodies)
		next, _ = contextOutputContinuation(t, document, direction)
		output, err = (&getContextTool{}).InvokableRun(ctx, next)
		require.NoError(t, err)
		require.Equal(t, []int{400 - expectedID}, contextOutputIDs(decodeContextOutput(t, output)))
	}
}

func TestGetContextOutputOversizedAttributesHaveProgressCursor(t *testing.T) {
	ctx := contextOutputFixture(t,
		&tb.Message{ID: 9998, Text: "older"},
		&tb.Message{ID: 9999, Text: "cannot fit attributes", Sender: &tb.User{FirstName: strings.Repeat("<&\"😀", 4000)}},
	)
	output, err := (&getContextTool{}).InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	document := decodeContextOutput(t, output)
	require.Empty(t, document.Messages)
	require.NotNil(t, document.Metadata.OmittedMessage)
	require.Equal(t, 9999, document.Metadata.OmittedMessage.ID)
	next, args := contextOutputContinuation(t, document, "older")
	require.Equal(t, 9999, *args.BeforeMessageID)
	output, err = (&getContextTool{}).InvokableRun(ctx, next)
	require.NoError(t, err)
	require.Equal(t, []int{9998}, contextOutputIDs(decodeContextOutput(t, output)))
}

func TestGetContextOutputClippedPhotoPreservesMediaAndEscapesXML(t *testing.T) {
	ctx := contextOutputFixture(t, &tb.Message{
		ID: 9999, Caption: strings.Repeat("中文😀<&\"\x00", 4000),
		Sender: &tb.User{Username: `user\"><metadata count="999">`, FirstName: "<&\""},
		Photo:  &tb.Photo{File: tb.File{FileID: `file<&"`}},
	})
	output, err := (&getContextTool{}).InvokableRun(ctx, `{"limit_from":"earliest"}`)
	require.NoError(t, err)
	document := decodeContextOutput(t, output)
	require.Len(t, document.Messages, 1)
	require.Equal(t, "photo", document.Messages[0].Type)
	require.NotNil(t, document.Messages[0].Image)
	require.Equal(t, `file<&"`, document.Messages[0].Image.FileID)
	require.Equal(t, `user\"><metadata count="999">`, document.Messages[0].User)
	require.Equal(t, 1, document.Metadata.TruncatedBodies)
}

func TestGetContextOutputDefaultRetainsReplyChainPriority(t *testing.T) {
	ctx := contextOutputFixture(t, &tb.Message{ID: 9999, Text: "newer unrelated"})
	GetTurnContext(ctx).Message.ReplyTo = &tb.Message{ID: 42, Text: "replied-to message"}
	output, err := (&getContextTool{}).InvokableRun(ctx, `{"limit":2}`)
	require.NoError(t, err)
	document := decodeContextOutput(t, output)
	require.Equal(t, []int{42}, contextOutputIDs(document))
	require.Equal(t, "reply_chain_first", document.Metadata.Selection)
	require.Equal(t, "default_selection_only", document.Metadata.CandidateScope)
	require.Equal(t, "unknown", document.Metadata.OmittedByLimit)
}

func TestGetContextOutputEmptyAndLimitCap(t *testing.T) {
	var messages []*tb.Message
	for id := 1; id <= 60; id++ {
		messages = append(messages, &tb.Message{ID: id, Text: "short"})
	}
	ctx := contextOutputFixture(t, messages...)
	output, err := (&getContextTool{}).InvokableRun(ctx, `{"limit":500,"limit_from":"earliest"}`)
	require.NoError(t, err)
	document := decodeContextOutput(t, output)
	require.Len(t, document.Messages, 50)
	require.Equal(t, 50, document.Metadata.Limit)
	require.Equal(t, "10", document.Metadata.OmittedByLimit)
	output, err = (&getContextTool{}).InvokableRun(ctx, `{"after_message_id":60}`)
	require.NoError(t, err)
	document = decodeContextOutput(t, output)
	require.Empty(t, document.Messages)
	require.Zero(t, document.Metadata.CandidateCount)
	require.Empty(t, document.Metadata.Continuations)
}

func TestGetContextOutputCountsEachReplyMessageOnce(t *testing.T) {
	ctx := contextOutputFixture(t)
	first := &tb.Message{ID: 9997, Text: "first"}
	second := &tb.Message{ID: 9998, Text: "second", ReplyTo: first}
	first.ReplyTo = second
	GetTurnContext(ctx).Message.ReplyTo = second
	output, err := (&getContextTool{}).InvokableRun(ctx, `{"limit_from":"latest"}`)
	require.NoError(t, err)
	document := decodeContextOutput(t, output)
	require.ElementsMatch(t, []int{9997, 9998}, contextOutputIDs(document))
}

func TestGetContextOutputTimestamp(t *testing.T) {
	stamp := time.Now().Unix()
	ctx := contextOutputFixture(t, &tb.Message{ID: 9999, Text: "timestamp", Unixtime: stamp})
	output, err := (&getContextTool{}).InvokableRun(ctx, `{}`)
	require.NoError(t, err)
	var document struct {
		Messages []struct {
			Time string `xml:"time,attr"`
		} `xml:"message"`
	}
	require.NoError(t, xml.Unmarshal([]byte(output), &document))
	require.Len(t, document.Messages, 1)
	parsed, err := time.Parse(time.RFC3339, document.Messages[0].Time)
	require.NoError(t, err)
	require.Equal(t, stamp, parsed.Unix())
	_, offset := parsed.Zone()
	require.Equal(t, 8*60*60, offset)
}
