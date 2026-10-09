package agentv3

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2/server"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestAgentV3SessionChatAlbumRedisUsesCallbackContext(t *testing.T) {
	for _, action := range []string{"cancel", "close", "caller deadline beyond operation budget"} {
		t.Run(action, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "album-cancel", ContextMode: "chat", Session: config.AgentSessionConfig{LoadContext: true}}
			mdl := &scriptedToolModel{}
			compiled := f.compile(t, cfg, mdl)
			seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
			options := session.Options{}
			if action == "caller deadline beyond operation budget" {
				options.OperationTimeout = 50 * time.Millisecond
			}
			repo := installAgentSessionGate(t, f, options)
			started, release := make(chan struct{}, 8), make(chan struct{})
			f.mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
				if command != "MGET" || len(args) == 0 || !strings.Contains(args[0], "message_full:") {
					return false
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
				peer.WriteLen(len(args))
				for range args {
					peer.WriteNull()
				}
				return true
			})
			current := sessionMessage(1100, 8, 60, "current")
			current.AlbumID = "current-album"
			current.Photo = &tb.Photo{File: tb.File{FileID: "current-photo"}}
			tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: current, ChatID: -100, Config: cfg}
			ctx, cancel := context.WithCancel(WithTurnContext(t.Context(), tc))
			if action == "caller deadline beyond operation budget" {
				cancel()
				ctx, cancel = context.WithTimeout(WithTurnContext(t.Context(), tc), 300*time.Millisecond)
			}
			defer cancel()
			if action == "close" {
				// Cancel only after Close aborted the candidate, before prepare can fall back to legacy rendering.
				repo.onRelease = cancel
			}
			setupAgentV3SessionTurn(tc)
			returned := make(chan error, 1)
			finished := make(chan struct{})
			go func() { defer close(finished); _, err := prepareAgentV3Turn(ctx, compiled, tc, nil); returned <- err }()
			defer func() {
				cancel()
				close(release)
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("album prepare worker did not stop during cleanup")
				}
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("candidate rendering did not reach album Redis MGET")
			}
			require.Same(t, ctx, tc.V3.renderCtx, "shared turn keeps caller context; candidate renderer gets op context")
			switch action {
			case "cancel":
				cancel()
			case "close":
				closed := make(chan error, 1)
				go func() { closed <- f.service.Close() }()
				select {
				case err := <-closed:
					require.NoError(t, err)
				case <-time.After(time.Second):
					t.Fatal("Service.Close waited for Redis socket timeout")
				}
				cancel()
			case "caller deadline beyond operation budget":
				select {
				case <-repo.released:
					t.Fatal("short operation budget must not truncate album rendering")
				case <-time.After(100 * time.Millisecond):
				}
				select {
				case <-repo.released:
				case <-time.After(time.Second):
					t.Fatal("caller deadline did not release parent pin")
				}
			}
			select {
			case err := <-returned:
				if action == "caller deadline beyond operation budget" {
					require.ErrorIs(t, err, context.DeadlineExceeded)
				} else {
					require.ErrorIs(t, err, context.Canceled)
				}
			case <-time.After(time.Second):
				t.Fatal("prepare remained blocked in album Redis query")
			}
			require.Zero(t, repo.confirms.Load())
			require.EqualValues(t, 1, repo.releases.Load())
			require.Nil(t, tc.Session.parent)
			require.Empty(t, tc.Session.input)
			require.Empty(t, tc.V3.ImageRefs)
			require.Empty(t, mdl.capturedInputs())
			require.NoError(t, f.client.Ping(t.Context()).Err(), "independent Redis work remains available")
		})
	}
}

func TestAgentV3AlbumContextPreservesOrderDedupAndDirectReplyOnly(t *testing.T) {
	setupReplySessionRedis(t)
	oldWait := currentAlbumCompletionWait
	currentAlbumCompletionWait = 0
	t.Cleanup(func() { currentAlbumCompletionWait = oldWait })
	photo := func(id int, album, file string) *tb.Message {
		message := sessionMessage(id, 7, 0, "")
		message.AlbumID = album
		message.Photo = &tb.Photo{File: tb.File{FileID: file}}
		return message
	}
	for _, message := range []*tb.Message{photo(99, "current", "first"), photo(101, "current", "current-file"), photo(102, "other", "wrong-album"), photo(201, "reply", "reply-sibling")} {
		require.NoError(t, orm.SetMessage(message))
	}
	current := photo(100, "current", "current-file")
	current.ReplyTo = photo(200, "reply", "direct-reply")
	tc := &TurnContext{Message: current, V3: &AgentV3TurnState{renderCtx: t.Context()}}
	refs := collectAgentV3ImageRefs(tc, nil, nil)
	require.Equal(t, []orm.AgentV3ImageRef{{MessageID: 99, FileID: "first"}, {MessageID: 100, FileID: "current-file"}, {MessageID: 200, FileID: "direct-reply"}}, refs)
	require.Equal(t, "reply", current.ReplyTo.AlbumID)
}

func TestCurrentAlbumPollingBatchesAndReusesOneRedisConnection(t *testing.T) {
	mini := setupReplySessionRedis(t)
	oldWait, oldPoll, oldWindow := currentAlbumCompletionWait, currentAlbumCompletionPollInterval, currentAlbumSiblingWindow
	currentAlbumCompletionWait, currentAlbumCompletionPollInterval, currentAlbumSiblingWindow = 500*time.Millisecond, 100*time.Millisecond, 16
	t.Cleanup(func() {
		currentAlbumCompletionWait, currentAlbumCompletionPollInterval, currentAlbumSiblingWindow = oldWait, oldPoll, oldWindow
	})
	message := func(id int, album string) *tb.Message {
		return &tb.Message{ID: id, Chat: &tb.Chat{ID: -100}, AlbumID: album, Photo: &tb.Photo{File: tb.File{FileID: strconv.Itoa(id)}}}
	}
	require.NoError(t, orm.SetMessage(message(99, "current")))
	require.NoError(t, orm.SetMessage(message(102, "other")))
	lateData, err := json.Marshal(message(101, "current"))
	require.NoError(t, err)
	key := func(id int) string {
		return config.BotConfig.RedisConfig.KeyPrefix + "message_full:c-100:u" + strconv.Itoa(id)
	}
	var mu sync.Mutex
	var batches [][]string
	var hellos, gets int
	peers := make(map[*server.Peer]struct{})
	mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
		mu.Lock()
		defer mu.Unlock()
		switch command {
		case "HELLO":
			hellos++
		case "GET":
			gets++
		case "MGET":
			peers[peer] = struct{}{}
			batches = append(batches, append([]string(nil), args...))
			if len(batches) == 2 {
				mini.Set(key(101), string(lateData))
			}
		}
		return false
	})
	refs := collectAgentV3ImageRefs(&TurnContext{
		Message: message(100, "current"), V3: &AgentV3TurnState{renderCtx: t.Context()},
	}, nil, nil)
	require.Equal(t, []orm.AgentV3ImageRef{{MessageID: 99, FileID: "99"}, {MessageID: 100, FileID: "100"}, {MessageID: 101, FileID: "101"}}, refs)
	mu.Lock()
	defer mu.Unlock()
	require.Zero(t, gets, "album polling never uses per-ID GET")
	require.Equal(t, 1, hellos, "the entire operation creates one initialized connection")
	require.Len(t, peers, 1, "all polling rounds use the same socket")
	require.GreaterOrEqual(t, len(batches), 2)
	require.LessOrEqual(t, len(batches), 6, "500ms/100ms polling is bounded by rounds, not 32 IDs per round")
	require.Len(t, batches[0], 32)
	require.Contains(t, batches[0], key(101))
	require.Contains(t, batches[1], key(101), "misses must be checked again to receive late album messages")
	for i, batch := range batches {
		require.NotContains(t, batch, key(100))
		if i > 0 {
			require.NotContains(t, batch, key(99), "known sibling cached across polls")
			require.NotContains(t, batch, key(102), "known non-album message cached across polls")
		}
		if i > 1 {
			require.NotContains(t, batch, key(101), "late sibling is cached once received")
		}
	}
	t.Logf("500ms/100ms +/-16 album: connections=%d, MGET=%d, GET=%d; late miss retried and known IDs excluded", hellos, len(batches), gets)
}
