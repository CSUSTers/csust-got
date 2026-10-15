package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestPhotoDownloaderRetirementClosesAfterLastPhoto(t *testing.T) {
	for _, retirement := range []string{"evict", "registry close", "evict then registry close"} {
		for _, users := range []int{1, 2} {
			t.Run(fmt.Sprintf("%s/users=%d", retirement, users), func(t *testing.T) {
				setupPhotoDownloaderConfig(t)
				imageData := photoDownloaderImage(t)
				started := []chan struct{}{make(chan struct{}), make(chan struct{})}
				release := []chan struct{}{make(chan struct{}), make(chan struct{})}
				var releaseOnce [2]sync.Once
				var states photoLifecycleConnectionStates
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					owned := strings.Contains(r.URL.Path, "botretired-photo-token/")
					states.identify(r, owned)
					if strings.HasSuffix(r.URL.Path, "/getFile") {
						var payload map[string]string
						if json.NewDecoder(r.Body).Decode(&payload) != nil {
							return
						}
						if owned {
							index := 0
							if payload["file_id"] == "second" {
								index = 1
							}
							close(started[index])
							select {
							case <-release[index]:
							case <-r.Context().Done():
								return
							}
						}
						_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`))
						return
					}
					_, _ = w.Write(imageData)
				}))
				server.Config.ConnContext = states.context
				server.Config.ConnState = states.record
				server.Start()
				t.Cleanup(server.Close)
				bot := &tb.Bot{Token: "retired-photo-token", URL: server.URL}
				ctx, cancel := context.WithCancel(t.Context())
				returned := make([]chan error, users)
				finished := make([]chan struct{}, users)
				for i := range users {
					returned[i], finished[i] = make(chan error, 1), make(chan struct{})
					go func() {
						defer close(finished[i])
						fileID := "first"
						if i == 1 {
							fileID = "second"
						}
						_, err := encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: ctx}}, &tb.Photo{File: tb.File{FileID: fileID}})
						returned[i] <- err
					}()
				}
				t.Cleanup(func() {
					cancel()
					for i := range users {
						releaseOnce[i].Do(func() { close(release[i]) })
						select {
						case <-finished[i]:
						case <-time.After(time.Second):
							t.Error("photo worker did not stop")
						}
					}
				})
				for i := range users {
					select {
					case <-started[i]:
					case <-time.After(time.Second):
						t.Fatal("photo lookup did not reach the blocked server")
					}
				}
				var survivor *tb.Bot
				if strings.HasPrefix(retirement, "evict") {
					for range maxTelegramPhotoDownloaders {
						survivor = &tb.Bot{Token: "surviving-photo-token", URL: server.URL}
						entry, err := telegramPhotoDownloaders.acquire(survivor)
						require.NoError(t, err)
						telegramPhotoDownloaders.release(entry)
					}
					if retirement == "evict then registry close" {
						closeTelegramPhotoDownloaders()
					}
				} else {
					closeTelegramPhotoDownloaders()
					closeTelegramPhotoDownloaders()
					survivor = &tb.Bot{Token: "surviving-photo-token", URL: server.URL}
				}
				_, retired, references := photoDownloaderLifecycleCounts()
				require.Equal(t, 1, retired, "in-use entry remains tracked after retirement")
				require.Equal(t, users, references)
				_, err := encodePhotoForLLM(&TurnContext{Bot: survivor, V3: &AgentV3TurnState{renderCtx: t.Context()}}, &tb.Photo{File: tb.File{FileID: "healthy"}})
				require.NoError(t, err, "retirement cannot wait for or cancel another turn")
				_, _, closed := states.counts(true)
				require.Zero(t, closed, "retirement must not kill active lookup connections")
				for i := range users {
					releaseOnce[i].Do(func() { close(release[i]) })
					select {
					case err := <-returned[i]:
						require.NoError(t, err, "retired entry must still finish metadata, download and encoding")
					case <-time.After(time.Second):
						t.Fatal("retired photo did not finish")
					}
					if i < users-1 {
						require.Eventually(t, func() bool { _, idle, _ := states.counts(true); return idle == 1 }, time.Second, time.Millisecond)
						_, _, closed := states.counts(true)
						require.Zero(t, closed, "the first release cannot close idle connections while another photo holds the same entry")
						_, retired, references := photoDownloaderLifecycleCounts()
						require.Equal(t, 1, retired)
						require.Equal(t, 1, references, "the second photo still owns a reference")
					}
				}
				require.Eventually(t, func() bool {
					connections, _, closed := states.counts(true)
					return connections >= users && closed == connections
				}, time.Second, time.Millisecond,
					"last photo release must close all retired idle connections without waiting for IdleConnTimeout")
				connections, idle, closed := states.counts(true)
				require.Equal(t, users, connections, "lookup and post-retirement download reuse the existing sockets")
				require.Zero(t, idle)
				_, retired, references = photoDownloaderLifecycleCounts()
				require.Zero(t, retired, "last release removes retired tracking")
				require.Zero(t, references)
				_, _, survivorClosed := states.counts(false)
				require.Zero(t, survivorClosed, "retired cleanup must not close the current cached entry's connection")
				t.Logf("%s, users=%d: owned connections=%d closed=%d idle=%d; survivor connection still open", retirement, users, connections, closed, idle)
			})
		}
	}
}

func TestPhotoDownloaderRetiredErrorAndCancellationPathsRelease(t *testing.T) {
	for _, retirement := range []string{"evict", "registry close"} {
		for _, failure := range []string{"lookup rejection", "download status", "read failure", "decode failure", "cancel lookup", "cancel download headers", "cancel download body"} {
			t.Run(retirement+"/"+failure, func(t *testing.T) {
				setupPhotoDownloaderConfig(t)
				lookupStarted, lookupRelease := make(chan struct{}), make(chan struct{})
				downloadStarted := make(chan struct{})
				var releaseOnce sync.Once
				var states photoLifecycleConnectionStates
				server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					states.identify(r, true)
					if strings.HasSuffix(r.URL.Path, "/getFile") {
						var payload map[string]string
						if json.NewDecoder(r.Body).Decode(&payload) != nil {
							return
						}
						close(lookupStarted)
						select {
						case <-lookupRelease:
						case <-r.Context().Done():
							return
						}
						if failure == "lookup rejection" {
							_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"Bad Request: wrong file identifier/HTTP URL specified"}`))
							return
						}
						_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`))
						return
					}
					if strings.HasPrefix(failure, "cancel download") {
						if failure == "cancel download body" {
							_, _ = w.Write([]byte("partial image"))
							w.(http.Flusher).Flush()
						}
						close(downloadStarted)
						<-r.Context().Done()
						return
					}
					switch failure {
					case "download status":
						w.WriteHeader(http.StatusServiceUnavailable)
					case "read failure":
						w.Header().Set("Content-Length", "100")
						_, _ = w.Write([]byte("partial image"))
					case "decode failure":
						_, _ = w.Write([]byte("invalid image format"))
					}
				}))
				server.Config.ConnContext = states.context
				server.Config.ConnState = states.record
				server.Start()
				t.Cleanup(server.Close)
				ctx, cancel := context.WithCancel(t.Context())
				returned := make(chan error, 1)
				finished := make(chan struct{})
				go func() {
					defer close(finished)
					_, err := encodePhotoForLLM(&TurnContext{Bot: &tb.Bot{Token: "retired-error-token", URL: server.URL}, V3: &AgentV3TurnState{renderCtx: ctx}}, &tb.Photo{File: tb.File{FileID: "photo"}})
					returned <- err
				}()
				t.Cleanup(func() {
					cancel()
					releaseOnce.Do(func() { close(lookupRelease) })
					select {
					case <-finished:
					case <-time.After(time.Second):
						t.Error("photo error worker did not stop")
					}
				})
				select {
				case <-lookupStarted:
				case <-time.After(time.Second):
					t.Fatal("lookup did not reach blocked server")
				}
				if retirement == "evict" {
					for range maxTelegramPhotoDownloaders {
						entry, err := telegramPhotoDownloaders.acquire(&tb.Bot{Token: "filler-token", URL: server.URL})
						require.NoError(t, err)
						telegramPhotoDownloaders.release(entry)
					}
				} else {
					closeTelegramPhotoDownloaders()
				}
				if failure == "cancel lookup" {
					cancel()
				} else {
					releaseOnce.Do(func() { close(lookupRelease) })
					if strings.HasPrefix(failure, "cancel download") {
						select {
						case <-downloadStarted:
						case <-time.After(time.Second):
							t.Fatal("download did not reach cancellation stage")
						}
						cancel()
					}
				}
				select {
				case err := <-returned:
					require.Error(t, err)
					if strings.HasPrefix(failure, "cancel") {
						require.ErrorIs(t, err, context.Canceled)
					}
				case <-time.After(time.Second):
					t.Fatal("retired error path did not release")
				}
				_, retired, references := photoDownloaderLifecycleCounts()
				require.Zero(t, retired)
				require.Zero(t, references, "every error/cancellation path releases its acquired photo reference")
				require.Eventually(t, func() bool {
					connections, _, closed := states.counts(true)
					return connections > 0 && connections == closed
				}, time.Second, time.Millisecond)
			})
		}
	}
}

func photoDownloaderLifecycleCounts() (cached, retired, references int) {
	telegramPhotoDownloaders.mu.Lock()
	defer telegramPhotoDownloaders.mu.Unlock()
	cached, retired = len(telegramPhotoDownloaders.entries), len(telegramPhotoDownloaders.retired)
	for entry := range telegramPhotoDownloaders.retired {
		references += entry.inUse
	}
	for _, entry := range telegramPhotoDownloaders.entries {
		references += entry.inUse
	}
	return
}

type photoLifecycleConnectionKey struct{}

type photoLifecycleConnectionStates struct {
	mu     sync.Mutex
	states map[net.Conn]http.ConnState
	owned  map[net.Conn]bool
}

func (s *photoLifecycleConnectionStates) context(ctx context.Context, conn net.Conn) context.Context {
	return context.WithValue(ctx, photoLifecycleConnectionKey{}, conn)
}

func (s *photoLifecycleConnectionStates) identify(req *http.Request, owned bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.owned == nil {
		s.owned = make(map[net.Conn]bool)
	}
	s.owned[req.Context().Value(photoLifecycleConnectionKey{}).(net.Conn)] = owned
}

func (s *photoLifecycleConnectionStates) record(conn net.Conn, state http.ConnState) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.states == nil {
		s.states = make(map[net.Conn]http.ConnState)
	}
	s.states[conn] = state
}

func (s *photoLifecycleConnectionStates) counts(owned bool) (connections, idle, closed int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for conn, isOwned := range s.owned {
		if isOwned != owned {
			continue
		}
		connections++
		switch s.states[conn] {
		case http.StateIdle:
			idle++
		case http.StateClosed:
			closed++
		default:
		}
	}
	return
}
