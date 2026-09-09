package agentv3

import (
	"csust-got/config"
	"csust-got/log"
	"csust-got/orm"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func setupReplySessionRedis(t *testing.T) *miniredis.Miniredis {
	t.Helper()
	oldConfig := config.BotConfig
	miniRedis := miniredis.RunT(t)
	testConfig := config.NewBotConfig()
	testConfig.RedisConfig.RedisAddr = miniRedis.Addr()
	testConfig.RedisConfig.KeyPrefix = "reply-session-test:"
	config.BotConfig = testConfig
	orm.InitRedis()
	log.InitLogger()
	t.Cleanup(func() {
		config.BotConfig = oldConfig
		if oldConfig != nil && oldConfig.RedisConfig != nil {
			orm.InitRedis()
		}
	})
	return miniRedis
}

func TestSaveResponsePreservesKnownReplyWithoutMutation(t *testing.T) {
	setupReplySessionRedis(t)
	user := sessionMessage(10, 7, 0, "request")
	bot := sessionMessage(11, 99, 1, "answer")
	bot.Sender.IsBot = true

	SaveResponse(bot, user)
	require.Nil(t, bot.ReplyTo)

	stored, err := orm.GetMessage(-100, 11)
	require.NoError(t, err)
	require.NotNil(t, stored.ReplyTo)
	require.Equal(t, 10, stored.ReplyTo.ID)
	stream, err := orm.GetMessagesFromStream(-100, "11", "11", 1, false)
	require.NoError(t, err)
	require.Len(t, stream, 1)
	require.NotNil(t, stream[0].ReplyTo)
	require.Equal(t, 10, stream[0].ReplyTo.ID)
}

func TestSaveResponseKeepsExistingParentAndRejectsCrossChat(t *testing.T) {
	t.Run("existing_parent_wins", func(t *testing.T) {
		setupReplySessionRedis(t)
		user := sessionMessage(10, 7, 0, "request")
		bot := sessionMessage(11, 99, 1, "answer")
		bot.ReplyTo = &tb.Message{ID: 9, Chat: bot.Chat}

		SaveResponse(bot, user)
		stored, err := orm.GetMessage(-100, 11)
		require.NoError(t, err)
		require.Equal(t, 9, stored.ReplyTo.ID)
		require.Equal(t, 9, bot.ReplyTo.ID)
	})

	t.Run("cross_chat_not_attached", func(t *testing.T) {
		setupReplySessionRedis(t)
		user := sessionMessage(10, 7, 0, "request")
		user.Chat = &tb.Chat{ID: -200}
		bot := sessionMessage(11, 99, 1, "answer")

		SaveResponse(bot, user)
		stored, err := orm.GetMessage(-100, 11)
		require.NoError(t, err)
		require.Nil(t, stored.ReplyTo)
		require.Nil(t, bot.ReplyTo)
	})
}

func TestReplySessionLoadRecoversStoredParentAndExpiry(t *testing.T) {
	miniRedis := setupReplySessionRedis(t)
	user := sessionMessage(10, 7, 0, "request")
	require.NoError(t, orm.SetMessage(user))
	require.NoError(t, orm.PushMessageToStream(user))
	bot := sessionMessage(11, 99, 1, "answer")
	bot.Sender.IsBot = true
	SaveResponse(bot, user)
	current := sessionMessage(12, 7, 2, "followup")
	current.ReplyTo = &tb.Message{ID: 11}

	loaded, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{10}, {11}, {12}}, sessionBlockIDs(loaded))
	require.False(t, loaded.Incomplete)

	miniRedis.FastForward(25 * time.Hour)
	expired, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{12}}, sessionBlockIDs(expired))
	require.True(t, expired.Incomplete)
}

func TestReplySessionRejectsUnsupportedStoredParents(t *testing.T) {
	newService := func() *tb.Message {
		return &tb.Message{
			ID:       3,
			Chat:     &tb.Chat{ID: -100},
			Sender:   &tb.User{ID: 8},
			Unixtime: 1700000000,
		}
	}

	t.Run("stored_service", func(t *testing.T) {
		setupReplySessionRedis(t)
		service := newService()
		require.NoError(t, orm.SetMessage(service))
		current := sessionMessage(4, 7, 1, "current")
		current.ReplyTo = &tb.Message{ID: service.ID}

		loaded, err := loadReplySession(t.Context(), current, 10)
		require.NoError(t, err)
		require.True(t, loaded.Incomplete)
		require.Equal(t, [][]int{{4}}, sessionBlockIDs(loaded))
	})

	t.Run("nearby_service", func(t *testing.T) {
		setupReplySessionRedis(t)
		service := newService()
		require.NoError(t, orm.PushMessageToStream(service))
		current := sessionMessage(4, 7, 1, "current")
		current.ReplyTo = &tb.Message{ID: service.ID}

		loaded, err := loadReplySession(t.Context(), current, 10)
		require.NoError(t, err)
		require.True(t, loaded.Incomplete)
		require.Equal(t, [][]int{{4}}, sessionBlockIDs(loaded))
	})
}

func TestReplySessionLoadSkipsUnrelatedMalformedStreamRecord(t *testing.T) {
	setupReplySessionRedis(t)
	bad := sessionMessage(3, 8, 0, "unrelated")
	bad.Poll = &tb.Poll{ID: "poll", Type: tb.PollType("unsupported"), Question: "question"}
	require.NoError(t, orm.PushMessageToStream(bad))
	current := sessionMessage(4, 7, 1, "current")

	loaded, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.Equal(t, [][]int{{4}}, sessionBlockIDs(loaded))
}

func TestReplySessionLoadRecoversNestedCachedPoll(t *testing.T) {
	for _, ancestor := range []bool{false, true} {
		t.Run(map[bool]string{false: "unrelated", true: "ancestor"}[ancestor], func(t *testing.T) {
			setupReplySessionRedis(t)
			parent := sessionMessage(3, 8, 0, "parent")
			parent.ReplyTo = &tb.Message{ID: 2, Poll: &tb.Poll{Type: tb.PollRegular}}
			require.NoError(t, orm.SetMessage(parent))
			require.NoError(t, orm.PushMessageToStream(parent))
			current := sessionMessage(4, 7, 1, "current")
			want := [][]int{{4}}
			if ancestor {
				current.ReplyTo = &tb.Message{ID: 3}
				want = [][]int{{3}, {4}}
			}

			loaded, err := loadReplySession(t.Context(), current, 10)
			require.NoError(t, err)
			require.Equal(t, want, sessionBlockIDs(loaded))
			require.Equal(t, ancestor, loaded.Incomplete)
		})
	}
}

func TestReplySessionLoadFallsBackFromCorruptStoredParent(t *testing.T) {
	tests := []struct {
		name         string
		nearby       bool
		embedded     bool
		wantBlockIDs [][]int
	}{
		{name: "nearby", nearby: true, wantBlockIDs: [][]int{{3}, {4}}},
		{name: "embedded", embedded: true, wantBlockIDs: [][]int{{3}, {4}}},
		{name: "unrecoverable", wantBlockIDs: [][]int{{4}}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			miniRedis := setupReplySessionRedis(t)
			parent := sessionMessage(3, 8, 0, "parent")
			if test.nearby {
				require.NoError(t, orm.PushMessageToStream(parent))
			}
			miniRedis.Set("reply-session-test:message_full:c-100:u3", `{"message_id":3`)
			current := sessionMessage(4, 7, 1, "current")
			current.ReplyTo = &tb.Message{ID: parent.ID}
			if test.embedded {
				current.ReplyTo = parent
			}

			loaded, err := loadReplySession(t.Context(), current, 10)
			require.NoError(t, err)
			require.True(t, loaded.Incomplete)
			require.Equal(t, test.wantBlockIDs, sessionBlockIDs(loaded))
		})
	}
}

func TestReplySessionLoadUsesScannedCountForStreamLimit(t *testing.T) {
	setupReplySessionRedis(t)
	for id := 1; id <= replySessionAncestorLimit; id++ {
		message := sessionMessage(id, 8, int64(id), "unrelated")
		if id == 1 {
			message.Poll = &tb.Poll{ID: "bad", Type: tb.PollType("unsupported"), Question: "question"}
		}
		require.NoError(t, orm.PushMessageToStream(message))
	}
	current := sessionMessage(replySessionAncestorLimit+1, 7, replySessionAncestorLimit+1, "current")

	loaded, err := loadReplySession(t.Context(), current, 10)
	require.NoError(t, err)
	require.True(t, loaded.Incomplete)
	require.Equal(t, [][]int{{replySessionAncestorLimit + 1}}, sessionBlockIDs(loaded))
}

func TestReplySessionLoadPropagatesStreamRedisError(t *testing.T) {
	miniRedis := setupReplySessionRedis(t)
	miniRedis.Close()

	_, err := loadReplySession(t.Context(), sessionMessage(4, 7, 0, "current"), 10)
	require.Error(t, err)
	require.NotErrorIs(t, err, orm.ErrInvalidCachedMessage)
}
