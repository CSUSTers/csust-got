package agentv3

import (
	"context"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2/server"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestContextUsesDeliveredFinalInsteadOfLivePlaceholder(t *testing.T) {
	setupReplySessionRedis(t)
	d := newDeliveryTelegram(t)
	user := sessionMessage(10, 7, 0, "request")
	user.ReplyTo = sessionMessage(9, 8, -1, "older ancestor")
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(schema.AssistantMessage("reliable final", nil), nil)
	writer.Close()
	result, err := streamToTelegramWithDelivery(t.Context(), d.bot.NewContext(tb.Update{Message: user}), reader, &config.AgentOutputConfig{}, nil, false)
	require.NoError(t, err)
	require.NotNil(t, result.sent)
	result.sent.Text = result.response
	SaveResponse(result.sent, user)

	stale := *result.sent
	stale.Text = "..."
	stale.ReplyTo = user
	current := sessionMessage(stale.ID+1, 7, 2, "followup")
	current.ReplyTo = &stale
	// The inbound cache stores the embedded stale object without changing it.
	require.NoError(t, orm.SetMessage(current))
	require.NoError(t, orm.PushMessageToStream(current))
	contextMessages, err := GetMessageContext(d.bot, current, 10)
	require.NoError(t, err)
	var answer *ContextMessage
	for _, message := range contextMessages {
		if message.ID == stale.ID {
			answer = message
		}
	}
	require.NotNil(t, answer)
	require.Equal(t, "reliable final", answer.Text)
	require.Contains(t, contextMessageIDs(contextMessages), 9)
	full := loadFullContextMessages(current, []*ContextMessage{answer})
	require.Len(t, full, 1)
	require.Equal(t, "reliable final", full[0].Text)
	require.Equal(t, "...", current.ReplyTo.Text)
}

func TestContextSkipsSuccessfullySentProcessAndKeepsAncestors(t *testing.T) {
	setupReplySessionRedis(t)
	d := newDeliveryTelegram(t)
	user := sessionMessage(10, 7, 0, "request")
	user.ReplyTo = sessionMessage(9, 8, -1, "older ancestor")
	reader, writer := schema.Pipe[*schema.Message](1)
	writer.Send(nil, assert.AnError)
	writer.Close()
	result, err := streamToTelegramWithDelivery(t.Context(), d.bot.NewContext(tb.Update{Message: user}), reader, &config.AgentOutputConfig{}, nil, false)
	require.Error(t, err)
	require.NotNil(t, result.sent)
	process := *result.sent
	process.Text = "arbitrary in-flight text"
	process.ReplyTo = user
	current := sessionMessage(process.ID+1, 7, 2, "followup")
	current.ReplyTo = &process
	require.NoError(t, orm.SetMessage(current))
	require.NoError(t, orm.PushMessageToStream(current))
	contextMessages, err := GetMessageContext(d.bot, current, 10)
	require.NoError(t, err)
	for _, message := range contextMessages {
		require.NotEqual(t, process.ID, message.ID)
	}
	require.Contains(t, contextMessageIDs(contextMessages), user.ID)
	require.Contains(t, contextMessageIDs(contextMessages), 9)
	loaded, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{9}, {user.ID}, {current.ID}}, sessionBlockIDs(loaded))
}

func contextMessageIDs(messages []*ContextMessage) []int {
	ids := make([]int, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, message.ID)
	}
	return ids
}

func TestContextLifecycleSurvivesActiveStreamAndRestart(t *testing.T) {
	for _, final := range []bool{false, true} {
		t.Run(map[bool]string{false: "process", true: "final"}[final], func(t *testing.T) {
			mini := setupReplySessionRedis(t)
			d := newDeliveryTelegram(t)
			parent := sessionMessage(10, 7, 0, "ancestor")
			process, err := d.bot.Send(parent.Chat, "...")
			require.NoError(t, err)
			RecordProcessMessage(d.bot, process, parent)
			if final {
				process.Text = "persisted final"
				require.NoError(t, SaveResponse(process, parent))
			}
			stale := *process
			stale.Text = "old progress"
			stale.ReplyTo = parent
			current := sessionMessage(process.ID+1, 7, 1, "followup")
			current.ReplyTo = &stale
			require.NoError(t, orm.SetMessage(current))
			require.NoError(t, orm.PushMessageToStream(current))
			mini.FastForward(23 * time.Hour)
			require.NoError(t, orm.PushMessageToStream(sessionMessage(current.ID+1, 8, 2, "unrelated")))
			mini.FastForward(2 * time.Hour)
			orm.InitRedis()
			resolved, excluded, err := resolveContextMessage(t.Context(), current.Chat.ID, current.ReplyTo)
			require.NoError(t, err)
			require.Equal(t, !final, excluded)
			if final {
				require.Equal(t, "persisted final", resolved.Text)
				full := loadFullContextMessages(current, []*ContextMessage{{ID: process.ID}})
				require.Equal(t, "persisted final", full[0].Text)
			}
			loaded, err := loadReplySession(t.Context(), current, 10)
			require.NoError(t, err)
			if final {
				require.Equal(t, "persisted final", loaded.Blocks[len(loaded.Blocks)-2].Messages[0].Text)
			} else {
				require.Equal(t, [][]int{{parent.ID}, {current.ID}}, sessionBlockIDs(loaded))
			}
		})
	}
}

func TestReplySessionCanonicalizesStreamAncestorsAfterFullExpiry(t *testing.T) {
	mini := setupReplySessionRedis(t)
	d := newDeliveryTelegram(t)
	ancestor := sessionMessage(10, 7, 0, "trusted ancestor")
	ancestor.ReplyTo = sessionMessage(9, 8, -1, "older ancestor")
	process, err := d.bot.Send(ancestor.Chat, "...")
	require.NoError(t, err)
	RecordProcessMessage(d.bot, process, ancestor)
	stale := *process
	stale.Text = "transient progress"
	stale.ReplyTo = nil
	user := sessionMessage(process.ID+1, 8, 1, "intervening user")
	user.ReplyTo = &stale
	require.NoError(t, orm.SetMessage(user))
	require.NoError(t, orm.PushMessageToStream(user))
	mini.FastForward(23 * time.Hour)
	require.NoError(t, orm.PushMessageToStream(sessionMessage(user.ID+1, 9, 2, "unrelated")))
	mini.FastForward(2 * time.Hour)
	current := sessionMessage(user.ID+2, 7, 3, "followup")
	current.ReplyTo = &tb.Message{ID: user.ID}
	loaded, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{9}, {ancestor.ID}, {user.ID}, {current.ID}}, sessionBlockIDs(loaded))
}

func TestReplySessionReusesOneReaderAndBatchesBotIdentities(t *testing.T) {
	mini := setupReplySessionRedis(t)
	d := newDeliveryTelegram(t)
	parent := sessionMessage(10, 7, 0, "ancestor")
	for range 12 {
		message, err := d.bot.Send(parent.Chat, "answer")
		require.NoError(t, err)
		message.Text = "delivered answer"
		require.NoError(t, SaveResponse(message, parent))
		parent = message
	}
	current := sessionMessage(parent.ID+1, 7, 1, "followup")
	current.ReplyTo = &tb.Message{ID: parent.ID}
	var batches atomic.Int32
	var mu sync.Mutex
	peers := make(map[*server.Peer]bool)
	mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
		mu.Lock()
		peers[peer] = true
		mu.Unlock()
		if command == "MGET" && len(args) != 0 && strings.Contains(args[0], "message_bot_lifecycle:") {
			batches.Add(1)
		}
		return false
	})
	loaded, err := loadReplySession(t.Context(), current, 30)
	require.NoError(t, err)
	require.Len(t, loaded.Blocks, 14)
	mu.Lock()
	connections := len(peers)
	mu.Unlock()
	require.Equal(t, 1, connections)
	require.EqualValues(t, 1, batches.Load())
	t.Logf("12 bot ancestors: query connections=%d, lifecycle batches=%d", connections, batches.Load())
}

func TestSaveResponseCacheFailuresDoNotResendOrExposeOldProcess(t *testing.T) {
	for _, failedCache := range []string{"message_bot_lifecycle", "message_full", "message_stream"} {
		t.Run(failedCache, func(t *testing.T) {
			mini := setupReplySessionRedis(t)
			d := newDeliveryTelegram(t)
			parent := sessionMessage(10, 7, 0, "ancestor")
			process, err := d.bot.Send(parent.Chat, "...")
			require.NoError(t, err)
			RecordProcessMessage(d.bot, process, parent)
			stale := *process
			stale.Text = "outdated progress"
			stale.ReplyTo = parent
			result, err := nonStreamResponseWithDelivery(t.Context(), d.bot, d.bot.NewContext(tb.Update{Message: parent}), "accepted final", "", &config.AgentOutputConfig{}, process, false, false)
			require.NoError(t, err)
			require.NotNil(t, result.sent)
			result.sent.Text = result.response
			calls := len(d.finalCalls())
			mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
				if (command == "EVAL" || command == "EVALSHA") && len(args) > 2 && strings.Contains(args[2], failedCache+":") {
					peer.WriteError("ERR injected cache failure")
					return true
				}
				return false
			})
			require.Error(t, SaveResponse(result.sent, parent))
			mini.Server().SetPreHook(nil)
			require.Len(t, d.finalCalls(), calls)
			resolved, excluded, err := resolveContextMessage(t.Context(), parent.Chat.ID, &stale)
			require.NoError(t, err)
			if failedCache == "message_bot_lifecycle" {
				require.True(t, excluded)
			} else {
				require.False(t, excluded)
				require.Equal(t, "accepted final", resolved.Text)
			}
		})
	}
}

func TestContextLifecycleDoesNotInferUnknownOrMismatchedIdentities(t *testing.T) {
	setupReplySessionRedis(t)
	d := newDeliveryTelegram(t)
	parent := sessionMessage(10, 7, 0, "ancestor")
	process, err := d.bot.Send(parent.Chat, "...")
	require.NoError(t, err)
	RecordProcessMessage(d.bot, process, parent)
	for _, identity := range []string{"unknown_bot", "different_bot", "user", "other_chat"} {
		t.Run(identity, func(t *testing.T) {
			candidate := *process
			sender := *process.Sender
			candidate.Sender = &sender
			candidate.Text = "..."
			switch identity {
			case "unknown_bot":
				candidate.ID++
			case "different_bot":
				candidate.Sender.ID++
			case "user":
				candidate.Sender.IsBot = false
			case "other_chat":
				candidate.Chat = &tb.Chat{ID: -200}
			}
			resolved, excluded, err := resolveContextMessage(t.Context(), -100, &candidate)
			require.NoError(t, err)
			require.False(t, excluded)
			require.Same(t, &candidate, resolved)
		})
	}
}

func TestAgentV3SessionFreshQuoteUsesTrustedLifecycle(t *testing.T) {
	for _, mode := range []string{"chat", "reply_chain"} {
		for _, final := range []bool{false, true} {
			t.Run(mode+map[bool]string{false: "/process", true: "/final"}[final], func(t *testing.T) {
				setupReplySessionRedis(t)
				d := newDeliveryTelegram(t)
				parent := sessionMessage(10, 7, 0, "ancestor")
				process, err := d.bot.Send(parent.Chat, "...")
				require.NoError(t, err)
				RecordProcessMessage(d.bot, process, parent)
				if final {
					process.Text = "reliable final quote"
					require.NoError(t, SaveResponse(process, parent))
				}
				stale := *process
				stale.Text = "STALE_PROCESS_BODY"
				stale.ReplyTo = parent
				current := sessionMessage(process.ID+1, 7, 1, "followup")
				current.ReplyTo = &stale
				cfg := &config.AgentConfig{ContextMode: mode, Model: &config.Model{}}
				tc := &TurnContext{Bot: d.bot, BotUser: d.bot.Me, Message: current, ChatID: current.Chat.ID, Config: cfg, V3: &AgentV3TurnState{}}
				messages, err := buildAgentV3SessionInput(&CompiledAgent{Config: cfg}, tc, nil)
				require.NoError(t, err)
				text := replySessionSchemaText(messages)
				require.NotContains(t, text, "STALE_PROCESS_BODY")
				if final {
					require.Contains(t, text, "reliable final quote")
				} else {
					require.Contains(t, text, "ancestor")
				}
				require.Equal(t, "STALE_PROCESS_BODY", current.ReplyTo.Text)
			})
		}
	}
}

func TestAgentV3SessionFreshQuoteHonorsRenderCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	tc := &TurnContext{ChatID: -100, Message: &tb.Message{ID: 20, Chat: &tb.Chat{ID: -100}, ReplyTo: &tb.Message{ID: 19}}, V3: &AgentV3TurnState{renderCtx: ctx}}
	messages, err := buildAgentV3SessionInput(&CompiledAgent{}, tc, nil)
	require.ErrorIs(t, err, context.Canceled)
	require.Nil(t, messages)
}
