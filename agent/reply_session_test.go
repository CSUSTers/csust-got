package agentv3

import (
	"context"
	"errors"
	"testing"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func sessionMessage(id int, sender int64, sec int64, text string) *tb.Message {
	return &tb.Message{
		ID:       id,
		Chat:     &tb.Chat{ID: -100},
		Sender:   &tb.User{ID: sender},
		Unixtime: 1700000000 + sec,
		Text:     text,
	}
}

func sessionBlockIDs(s replySession) [][]int {
	out := make([][]int, 0, len(s.Blocks))
	for _, block := range s.Blocks {
		ids := make([]int, 0, len(block.Messages))
		for _, message := range block.Messages {
			ids = append(ids, message.ID)
		}
		out = append(out, ids)
	}
	return out
}

func replySessionLookup(records map[int]*tb.Message) func(int64, int) (*tb.Message, error) {
	return func(chatID int64, messageID int) (*tb.Message, error) {
		if message := records[messageID]; message != nil {
			return message, nil
		}
		return nil, redis.Nil
	}
}

func TestReplySessionBlocksAndBranch(t *testing.T) {
	a := sessionMessage(1, 7, 0, "first")
	b := sessionMessage(2, 7, 40, "second")
	c := sessionMessage(3, 7, 80, "third")
	bot := sessionMessage(4, 99, 81, "answer")
	bot.Sender.IsBot = true
	bot.ReplyTo = &tb.Message{ID: 2}
	sibling := sessionMessage(5, 8, 82, "SIBLING_ONLY")
	sibling.ReplyTo = &tb.Message{ID: 4}
	trigger := sessionMessage(6, 7, 83, "CURRENT_ONLY")
	trigger.ReplyTo = &tb.Message{ID: 4}
	future := sessionMessage(7, 7, 84, "FUTURE_ONLY")
	records := map[int]*tb.Message{1: a, 2: b, 3: c, 4: bot, 5: sibling, 6: trigger, 7: future}

	got, err := selectReplySession(t.Context(), trigger, []*tb.Message{a, b, c, bot, sibling, future}, replySessionLookup(records), 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{1, 2, 3}, {4}, {6}}, sessionBlockIDs(got))
	require.True(t, got.Blocks[2].Current)
	require.False(t, got.Incomplete)
}

func TestReplySessionConsecutiveBoundaries(t *testing.T) {
	tests := []struct {
		name   string
		change func(*tb.Message, *tb.Message)
		want   [][]int
	}{
		{"59 seconds", func(a, b *tb.Message) { b.Unixtime = a.Unixtime + 59 }, [][]int{{10, 11}}},
		{"60 seconds", func(a, b *tb.Message) { b.Unixtime = a.Unixtime + 60 }, [][]int{{11}}},
		{"negative gap", func(a, b *tb.Message) { b.Unixtime = a.Unixtime - 1 }, [][]int{{11}}},
		{"other sender", func(a, b *tb.Message) { a.Sender.ID = 8 }, [][]int{{11}}},
		{"bot", func(a, b *tb.Message) { a.Sender.IsBot = true }, [][]int{{11}}},
		{"unknown sender", func(a, b *tb.Message) { a.Sender = nil }, [][]int{{11}}},
		{"empty utterance", func(a, b *tb.Message) { a.Text = "" }, [][]int{{11}}},
		{"missing ID may be service", func(a, b *tb.Message) { a.ID = 9 }, [][]int{{11}}},
		{"branch switch", func(a, b *tb.Message) {
			a.ReplyTo = &tb.Message{ID: 1}
			b.ReplyTo = &tb.Message{ID: 2}
		}, [][]int{{11}}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			a := sessionMessage(10, 7, 0, "earlier")
			b := sessionMessage(11, 7, 40, "current")
			tt.change(a, b)

			got, err := selectReplySession(t.Context(), b, []*tb.Message{a}, replySessionLookup(nil), 10)
			require.NoError(t, err)
			require.Equal(t, tt.want, sessionBlockIDs(got))
		})
	}
}

func TestReplySessionReplyBoundaries(t *testing.T) {
	t.Run("no_reply_is_new", func(t *testing.T) {
		oldBot := sessionMessage(18, 99, 0, "old bot")
		oldBot.Sender.IsBot = true
		followup := sessionMessage(20, 7, 2, "followup")
		followup.ReplyTo = &tb.Message{ID: 1}
		current := sessionMessage(21, 7, 3, "current")
		lookupCalls := 0

		got, err := selectReplySession(t.Context(), current, []*tb.Message{oldBot, followup}, func(int64, int) (*tb.Message, error) {
			lookupCalls++
			return nil, errors.New("a new session must not traverse nearby replies")
		}, 10)
		require.NoError(t, err)
		require.Equal(t, [][]int{{20, 21}}, sessionBlockIDs(got))
		require.Zero(t, lookupCalls)
	})

	t.Run("branch_switch_after_unreplied_followup", func(t *testing.T) {
		rootOne := sessionMessage(1, 8, 0, "one")
		rootTwo := sessionMessage(2, 8, 1, "two")
		a := sessionMessage(10, 7, 10, "a")
		a.ReplyTo = &tb.Message{ID: 1}
		b := sessionMessage(11, 7, 11, "b")
		current := sessionMessage(12, 7, 12, "c")
		current.ReplyTo = &tb.Message{ID: 2}

		got, err := selectReplySession(t.Context(), current, []*tb.Message{a, b}, replySessionLookup(map[int]*tb.Message{1: rootOne, 2: rootTwo}), 10)
		require.NoError(t, err)
		require.Equal(t, [][]int{{2}, {12}}, sessionBlockIDs(got))
	})

	t.Run("explicit_reply_inside_block", func(t *testing.T) {
		root := sessionMessage(1, 8, 0, "root")
		a := sessionMessage(10, 7, 10, "a")
		a.ReplyTo = &tb.Message{ID: 1}
		current := sessionMessage(11, 7, 11, "current")
		current.ReplyTo = &tb.Message{ID: 10}

		got, err := selectReplySession(t.Context(), current, []*tb.Message{a}, replySessionLookup(map[int]*tb.Message{1: root, 10: a}), 10)
		require.NoError(t, err)
		require.Equal(t, [][]int{{1}, {10, 11}}, sessionBlockIDs(got))
	})

	t.Run("service_break", func(t *testing.T) {
		a := sessionMessage(10, 7, 0, "before")
		service := &tb.Message{ID: 11, Chat: &tb.Chat{ID: -100}, Unixtime: a.Unixtime + 1}
		current := sessionMessage(12, 7, 2, "after")

		got, err := selectReplySession(t.Context(), current, []*tb.Message{a, service}, replySessionLookup(nil), 10)
		require.NoError(t, err)
		require.Equal(t, [][]int{{12}}, sessionBlockIDs(got))
	})
}

func TestReplySessionChainSafety(t *testing.T) {
	t.Run("deduplicate_incoming_priority", func(t *testing.T) {
		parent := sessionMessage(2, 8, 0, "full parent")
		current := sessionMessage(3, 7, 1, "incoming current")
		current.ReplyTo = &tb.Message{ID: 2, Text: "embedded parent"}
		nearbyCurrent := sessionMessage(3, 7, 1, "nearby current")
		nearbyParent := sessionMessage(2, 8, 0, "nearby parent")

		got, err := selectReplySession(t.Context(), current, []*tb.Message{nearbyCurrent, nearbyParent}, replySessionLookup(map[int]*tb.Message{2: parent}), 10)
		require.NoError(t, err)
		require.Equal(t, [][]int{{2}, {3}}, sessionBlockIDs(got))
		require.Equal(t, "incoming current", got.Blocks[1].Messages[0].Text)
	})

	t.Run("missing_parent", func(t *testing.T) {
		current := sessionMessage(4, 7, 0, "current")
		current.ReplyTo = &tb.Message{ID: 3}
		unrelated := sessionMessage(1, 8, 0, "unrelated")

		got, err := selectReplySession(t.Context(), current, []*tb.Message{unrelated}, replySessionLookup(nil), 10)
		require.NoError(t, err)
		require.True(t, got.Incomplete)
		require.Equal(t, [][]int{{4}}, sessionBlockIDs(got))
	})

	t.Run("unsupported_parent_sources", func(t *testing.T) {
		service := &tb.Message{
			ID:       3,
			Chat:     &tb.Chat{ID: -100},
			Sender:   &tb.User{ID: 8},
			Unixtime: 1700000000,
		}
		tests := []struct {
			name   string
			parent *tb.Message
			nearby []*tb.Message
			lookup func(int64, int) (*tb.Message, error)
		}{
			{
				name:   "embedded_sender_without_content",
				parent: &tb.Message{ID: 3, Sender: &tb.User{ID: 8}},
				lookup: replySessionLookup(nil),
			},
			{
				name:   "stored_service",
				parent: &tb.Message{ID: 3},
				lookup: replySessionLookup(map[int]*tb.Message{3: service}),
			},
			{
				name:   "nearby_service",
				parent: &tb.Message{ID: 3},
				nearby: []*tb.Message{service},
				lookup: replySessionLookup(nil),
			},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				current := sessionMessage(4, 7, 1, "current")
				current.ReplyTo = tt.parent

				got, err := selectReplySession(t.Context(), current, tt.nearby, tt.lookup, 10)
				require.NoError(t, err)
				require.True(t, got.Incomplete)
				require.Equal(t, [][]int{{4}}, sessionBlockIDs(got))
			})
		}
	})

	t.Run("cycle", func(t *testing.T) {
		current := sessionMessage(4, 7, 0, "current")
		current.ReplyTo = &tb.Message{ID: 3}
		three := sessionMessage(3, 8, -1, "three")
		three.ReplyTo = &tb.Message{ID: 2}
		two := sessionMessage(2, 8, -2, "two")
		two.ReplyTo = &tb.Message{ID: 3}

		got, err := selectReplySession(t.Context(), current, nil, replySessionLookup(map[int]*tb.Message{2: two, 3: three}), 10)
		require.NoError(t, err)
		require.True(t, got.Incomplete)
		require.Equal(t, [][]int{{2, 3}, {4}}, sessionBlockIDs(got))
	})

	t.Run("cross_chat", func(t *testing.T) {
		current := sessionMessage(4, 7, 0, "current")
		current.ReplyTo = sessionMessage(3, 8, -1, "other chat")
		current.ReplyTo.Chat.ID = -200

		got, err := selectReplySession(t.Context(), current, nil, replySessionLookup(nil), 10)
		require.NoError(t, err)
		require.True(t, got.Incomplete)
		require.Equal(t, [][]int{{4}}, sessionBlockIDs(got))
	})

	t.Run("lookup_error", func(t *testing.T) {
		current := sessionMessage(4, 7, 0, "current")
		current.ReplyTo = &tb.Message{ID: 3}
		want := errors.New("redis unavailable")

		_, err := selectReplySession(t.Context(), current, nil, func(int64, int) (*tb.Message, error) {
			return nil, want
		}, 10)
		require.ErrorIs(t, err, want)
	})

	t.Run("resource_limit", func(t *testing.T) {
		current := sessionMessage(2001, 7, 2001, "current")
		current.ReplyTo = &tb.Message{ID: 2000}
		lookupCalls := 0

		got, err := selectReplySession(t.Context(), current, nil, func(_ int64, id int) (*tb.Message, error) {
			lookupCalls++
			message := sessionMessage(id, 8, int64(id), "ancestor")
			if id > 1 {
				message.ReplyTo = &tb.Message{ID: id - 1}
			}
			return message, nil
		}, 10)
		require.NoError(t, err)
		require.True(t, got.Incomplete)
		require.LessOrEqual(t, lookupCalls, 1000)
	})

	t.Run("cancelled", func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		current := sessionMessage(4, 7, 0, "current")

		_, err := selectReplySession(ctx, current, nil, replySessionLookup(nil), 10)
		require.ErrorIs(t, err, context.Canceled)
	})
}

func TestReplySessionSoftLimitCountsMessages(t *testing.T) {
	oldBot := sessionMessage(1, 99, 0, "old bot")
	oldBot.Sender.IsBot = true
	first := sessionMessage(2, 7, 1, "first")
	first.ReplyTo = &tb.Message{ID: 1}
	second := sessionMessage(3, 7, 2, "second")
	third := sessionMessage(4, 7, 3, "third")
	third.ReplyTo = &tb.Message{ID: 1}
	current := sessionMessage(5, 8, 4, "current")
	current.ReplyTo = &tb.Message{ID: 4}

	got, err := selectReplySession(t.Context(), current, []*tb.Message{oldBot, first, second, third}, replySessionLookup(map[int]*tb.Message{
		1: oldBot,
		4: third,
	}), 2)
	require.NoError(t, err)
	require.Equal(t, [][]int{{2, 3, 4}, {5}}, sessionBlockIDs(got))
	require.True(t, got.Truncated)
	require.True(t, got.Blocks[1].Current)
	assert.False(t, got.Incomplete)

	defaulted, err := selectReplySession(t.Context(), current, []*tb.Message{oldBot, first, second, third}, replySessionLookup(map[int]*tb.Message{
		1: oldBot,
		4: third,
	}), 0)
	require.NoError(t, err)
	require.Equal(t, [][]int{{1}, {2, 3, 4}, {5}}, sessionBlockIDs(defaulted))
	assert.False(t, defaulted.Truncated)
}
