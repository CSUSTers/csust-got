package agentv3

import (
	"context"
	"strings"
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
	for _, action := range []string{"cancel", "close", "operation timeout"} {
		t.Run(action, func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "album-cancel", ContextMode: "chat", Session: config.AgentSessionConfig{LoadContext: true}}
			mdl := &scriptedToolModel{}
			compiled := f.compile(t, cfg, mdl)
			seedAgentSession(t, f, cfg, 50, []*schema.Message{schema.UserMessage("old"), schema.AssistantMessage("answer", nil)}, nil)
			options := session.Options{}
			if action == "operation timeout" {
				options.OperationTimeout = 100 * time.Millisecond
			}
			repo := installAgentSessionGate(t, f, options)
			started, release := make(chan struct{}, 8), make(chan struct{})
			f.mini.Server().SetPreHook(func(peer *server.Peer, command string, args ...string) bool {
				if command != "GET" || len(args) == 0 || !strings.Contains(args[0], "message_full:") {
					return false
				}
				select {
				case started <- struct{}{}:
				default:
				}
				<-release
				peer.WriteNull()
				return true
			})
			current := sessionMessage(1100, 8, 60, "current")
			current.AlbumID = "current-album"
			current.Photo = &tb.Photo{File: tb.File{FileID: "current-photo"}}
			tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: current, ChatID: -100, Config: cfg}
			ctx, cancel := context.WithCancel(WithTurnContext(t.Context(), tc))
			defer cancel()
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
				t.Fatal("candidate rendering did not reach album Redis GET")
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
			case "operation timeout":
				select {
				case <-repo.released:
				case <-time.After(time.Second):
					t.Fatal("callback op timeout did not release parent pin")
				}
				cancel()
			}
			select {
			case err := <-returned:
				require.ErrorIs(t, err, context.Canceled)
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
