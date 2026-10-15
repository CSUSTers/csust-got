package agentv3

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func isolateAgentV3ClosePhotoResources(t *testing.T) {
	t.Helper()
	registry := &telegramPhotoDownloaders
	registry.mu.Lock()
	quiescent := len(registry.retired) == 0
	for _, entry := range registry.entries {
		quiescent = quiescent && entry.inUse == 0
	}
	if !quiescent {
		registry.mu.Unlock()
		t.Fatal("Close fixture requires no pre-existing in-flight photo operations")
	}
	oldEntries, oldRetired := registry.entries, registry.retired
	registry.entries, registry.retired = nil, nil
	registry.mu.Unlock()
	oldManager, oldCron := mcpManager, cronService.Load()
	mcpManager = nil
	cronService.Store(nil)
	t.Cleanup(func() {
		closeTelegramPhotoDownloaders()
		registry.mu.Lock()
		registry.entries, registry.retired = oldEntries, oldRetired
		registry.mu.Unlock()
		mcpManager = oldManager
		cronService.Store(oldCron)
	})
}

func TestAgentV3CloseRetiresPhotoTransportsAfterSessionTermination(t *testing.T) {
	for _, inflight := range []bool{false, true} {
		t.Run("inflight="+strconv.FormatBool(inflight), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			isolateAgentV3ClosePhotoResources(t)
			imageData := photoDownloaderImage(t)
			started, release := make(chan struct{}), make(chan struct{})
			var releaseOnce sync.Once
			var states photoLifecycleConnectionStates
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				owned := strings.Contains(r.URL.Path, "botclose-photo-token/")
				states.identify(r, owned)
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					close(started)
					if inflight {
						select {
						case <-release:
						case <-r.Context().Done():
							return
						}
					}
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`))
					return
				}
				_, _ = w.Write(imageData)
			}))
			server.Config.ConnContext, server.Config.ConnState = states.context, states.record
			server.Start()
			t.Cleanup(server.Close)
			sharedTransport := http.DefaultTransport.(*http.Transport).Clone()
			t.Cleanup(sharedTransport.CloseIdleConnections)
			sharedClient := &http.Client{Transport: sharedTransport}
			bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "close-photo-token", URL: server.URL, Client: sharedClient})
			require.NoError(t, err)
			sharedResponse, err := sharedClient.Get(server.URL + "/shared")
			require.NoError(t, err)
			_, err = io.Copy(io.Discard, sharedResponse.Body)
			require.NoError(t, err)
			require.NoError(t, sharedResponse.Body.Close())
			require.Eventually(t, func() bool { _, idle, _ := states.counts(false); return idle == 1 }, time.Second, time.Millisecond)
			ctx, cancel := context.WithCancel(t.Context())
			returned, finished := make(chan error, 1), make(chan struct{})
			var closeDone chan struct{}
			go func() {
				defer close(finished)
				_, photoErr := encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: ctx}}, &tb.Photo{File: tb.File{FileID: "photo"}})
				returned <- photoErr
			}()
			t.Cleanup(func() {
				cancel()
				releaseOnce.Do(func() { close(release) })
				select {
				case <-finished:
				case <-time.After(3 * time.Second):
					t.Error("photo operation did not exit")
				}
				if closeDone != nil {
					select {
					case <-closeDone:
					case <-time.After(3 * time.Second):
						t.Error("Close did not exit")
					}
				}
			})
			select {
			case <-started:
			case <-time.After(3 * time.Second):
				t.Fatal("photo lookup did not start")
			}
			if !inflight {
				select {
				case photoErr := <-returned:
					require.NoError(t, photoErr)
				case <-time.After(3 * time.Second):
					t.Fatal("idle photo did not finish")
				}
				require.Eventually(t, func() bool { _, idle, _ := states.counts(true); return idle == 1 }, time.Second, time.Millisecond)
			}
			closeDone = make(chan struct{})
			go func() { Close(); close(closeDone) }()
			select {
			case <-closeDone:
			case <-time.After(3 * time.Second):
				t.Fatal("Close must not wait for a non-session photo operation")
			}
			require.Nil(t, agentSessionService.Load())
			require.ErrorIs(t, f.service.Collect(t.Context()), session.ErrClosed)
			cached, retired, references := photoDownloaderLifecycleCounts()
			require.Zero(t, cached, "the actual Close path must clear its photo registry")
			if inflight {
				require.Equal(t, 1, retired)
				require.Equal(t, 1, references)
				_, _, prematurelyClosed := states.counts(true)
				require.Zero(t, prematurelyClosed, "Close only retires a transport held by another turn")
				releaseOnce.Do(func() { close(release) })
				select {
				case photoErr := <-returned:
					require.NoError(t, photoErr, "the non-session turn still completes download and encoding")
				case <-time.After(3 * time.Second):
					t.Fatal("retired photo did not finish")
				}
			}
			require.Eventually(t, func() bool {
				connections, _, closedConnections := states.counts(true)
				return connections > 0 && closedConnections == connections
			}, time.Second, time.Millisecond, "Close or the final release must close owned idle connections")
			cached, retired, references = photoDownloaderLifecycleCounts()
			require.Zero(t, cached)
			require.Zero(t, retired)
			require.Zero(t, references)
			_, sharedIdle, sharedClosed := states.counts(false)
			require.Equal(t, 1, sharedIdle)
			require.Zero(t, sharedClosed, "Close must not close the Bot's shared HTTP transport")
		})
	}
}
