package agentv3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestPhotoDownloaderReusesHTTPResourcesAndConnections(t *testing.T) {
	for _, route := range []string{"TLS endpoint", "HTTP proxy"} {
		t.Run(route, func(t *testing.T) {
			setupPhotoDownloaderConfig(t)
			data := photoDownloaderImage(t)
			var connections, closed, lookups, downloads atomic.Int32
			var mu sync.Mutex
			var paths []string
			server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				paths = append(paths, r.URL.Path)
				mu.Unlock()
				if strings.HasSuffix(r.URL.Path, "/getFile") {
					lookups.Add(1)
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`))
					return
				}
				downloads.Add(1)
				_, _ = w.Write(data)
			}))
			server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
				switch state {
				case http.StateNew:
					connections.Add(1)
				case http.StateClosed:
					closed.Add(1)
				default:
				}
			}
			endpoint := "http://telegram.example.invalid/api-root"
			if route == "TLS endpoint" {
				server.StartTLS()
				endpoint = server.URL + "/api-root"
			} else {
				server.Start()
				config.BotConfig.Proxy = server.URL
			}
			defer server.Close()
			bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "photo-reuse-token", URL: endpoint})
			require.NoError(t, err)
			downloader, err := telegramPhotoDownloaders.acquire(bot)
			require.NoError(t, err)
			if route == "TLS endpoint" {
				// Trust only this fixture's TLS endpoint before the owned transport is used.
				downloader.transport.TLSClientConfig = server.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
			}
			telegramPhotoDownloaders.release(downloader)
			tc := &TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: t.Context()}}
			for i := range 3 {
				encoded, err := encodePhotoForLLM(tc, &tb.Photo{File: tb.File{FileID: fmt.Sprintf("photo-%d", i)}})
				require.NoError(t, err)
				require.True(t, strings.HasPrefix(encoded, "data:image/jpeg;base64,"))
				shared, err := telegramPhotoDownloaders.acquire(bot)
				require.NoError(t, err)
				require.Same(t, downloader, shared)
				require.Same(t, downloader.client, shared.client)
				require.Same(t, downloader.transport, shared.transport)
				telegramPhotoDownloaders.release(shared)
			}
			require.EqualValues(t, 3, lookups.Load())
			require.EqualValues(t, 3, downloads.Load())
			require.EqualValues(t, 1, connections.Load(), "metadata and bodies from three photos reuse a single TCP/TLS connection")
			mu.Lock()
			for i, path := range paths {
				if i%2 == 0 {
					require.Equal(t, "/api-root/botphoto-reuse-token/getFile", path)
				} else {
					require.Equal(t, "/api-root/file/botphoto-reuse-token/photo.png", path)
				}
			}
			mu.Unlock()
			closeTelegramPhotoDownloaders()
			require.Eventually(t, func() bool { return closed.Load() == 1 }, time.Second, time.Millisecond)
			t.Logf("%s: photos=3 HTTP requests=6 transport/client=1 TCP/TLS connections=%d; close drained owned idle connection", route, connections.Load())
		})
	}
}

func TestPhotoDownloaderCancellationDuringAllNetworkPhases(t *testing.T) {
	for _, phase := range []string{"lookup headers", "lookup body", "download headers", "download body"} {
		t.Run(phase, func(t *testing.T) {
			setupPhotoDownloaderConfig(t)
			data := photoDownloaderImage(t)
			started, stopped := make(chan struct{}), make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lookup := strings.HasSuffix(r.URL.Path, "/getFile")
				blocked := strings.Contains(r.URL.Path, "blocked.png")
				if lookup {
					var payload map[string]string
					if json.NewDecoder(r.Body).Decode(&payload) != nil {
						return
					}
					blocked = payload["file_id"] == "blocked"
					if blocked && strings.HasPrefix(phase, "download") {
						_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"blocked","file_path":"blocked.png"}}`))
						return
					}
				}
				if blocked {
					if strings.HasSuffix(phase, "body") {
						w.WriteHeader(http.StatusOK)
						if lookup {
							_, _ = w.Write([]byte(`{"ok":true,"result":`))
						} else {
							_, _ = w.Write(data[:8])
						}
						w.(http.Flusher).Flush()
					}
					close(started)
					<-r.Context().Done()
					close(stopped)
					return
				}
				if lookup {
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"healthy","file_path":"healthy.png"}}`))
					return
				}
				_, _ = w.Write(data)
			}))
			defer server.Close()
			const token = "PRIVATE_TOKEN_PHOTO_REQUEST_CANCEL"
			bot, err := tb.NewBot(tb.Settings{Offline: true, Token: token, URL: server.URL})
			require.NoError(t, err)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			blockedTC := &TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: ctx}}
			healthyTC := &TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: t.Context()}}
			returned := make(chan error, 1)
			go func() {
				_, err := encodePhotoForLLM(blockedTC, &tb.Photo{File: tb.File{FileID: "blocked"}})
				returned <- err
			}()
			select {
			case <-started:
			case <-time.After(time.Second):
				t.Fatal("request did not reach blocked " + phase)
			}
			_, err = encodePhotoForLLM(healthyTC, &tb.Photo{File: tb.File{FileID: "healthy"}})
			require.NoError(t, err, "independent request must not inherit the blocked turn context")
			shared, err := telegramPhotoDownloaders.acquire(bot)
			require.NoError(t, err)
			telegramPhotoDownloaders.release(shared)
			cancel()
			select {
			case err := <-returned:
				require.ErrorIs(t, err, context.Canceled)
				require.NotContains(t, fmt.Sprintf("%+v", err), token)
				require.NotContains(t, fmt.Sprintf("%+v", err), server.URL)
			case <-time.After(time.Second):
				t.Fatal("cancellation did not interrupt " + phase)
			}
			select {
			case <-stopped:
			case <-time.After(time.Second):
				t.Fatal("HTTP request was left detached")
			}
			_, err = encodePhotoForLLM(healthyTC, &tb.Photo{File: tb.File{FileID: "healthy"}})
			require.NoError(t, err, "cancel does not destroy shared transport")
			after, err := telegramPhotoDownloaders.acquire(bot)
			require.NoError(t, err)
			require.Same(t, shared, after)
			require.Same(t, shared.client, after.client)
			require.Same(t, shared.transport, after.transport)
			telegramPhotoDownloaders.release(after)
			t.Logf("%s actively canceled; simultaneous and later turn healthy on same downloader/client/transport", phase)
		})
	}
}

func TestPhotoDownloaderUsesCallerDeadlineWithoutPerPhotoBudget(t *testing.T) {
	setupPhotoDownloaderConfig(t)
	deadline := time.Now().Add(90 * time.Second)
	ctx, cancel := context.WithDeadline(t.Context(), deadline)
	defer cancel()
	var deadlines []time.Time
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "photo-deadline-token", URL: "http://telegram.invalid"})
	require.NoError(t, err)
	downloader, err := telegramPhotoDownloaders.acquire(bot)
	require.NoError(t, err)
	require.Zero(t, downloader.client.Timeout, "cancellable render requests are bounded only by their caller")
	require.Equal(t, time.Minute, downloader.legacyClient.Timeout, "non-cancellable legacy caller retains the SDK's standard client timeout")
	data := photoDownloaderImage(t)
	downloader.client.Transport = testRoundTripper(func(req *http.Request) (*http.Response, error) {
		d, ok := req.Context().Deadline()
		require.True(t, ok)
		deadlines = append(deadlines, d)
		body := data
		if req.Method == http.MethodPost {
			body = []byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`)
		}
		return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(bytes.NewReader(body)), Header: make(http.Header)}, nil
	})
	telegramPhotoDownloaders.release(downloader)
	for range 2 {
		_, err := encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: ctx}}, &tb.Photo{File: tb.File{FileID: "photo"}})
		require.NoError(t, err)
	}
	require.Equal(t, []time.Time{deadline, deadline, deadline, deadline}, deadlines, "getFile and download inherit the caller's >60s deadline without a per-photo reset")
}

func TestPhotoDownloaderRegistryIsBoundedAndSeparatesBotCredentials(t *testing.T) {
	setupPhotoDownloaderConfig(t)
	var registry telegramPhotoDownloaderRegistry
	defer registry.close()
	bot := &tb.Bot{Token: "first-token", URL: "http://first.invalid/api"}
	first, err := registry.acquire(bot)
	require.NoError(t, err)
	registry.release(first)
	secondBot := &tb.Bot{Token: "second-token", URL: bot.URL}
	second, err := registry.acquire(secondBot)
	require.NoError(t, err)
	registry.release(second)
	require.NotSame(t, first, second)
	require.NotSame(t, first.client, second.client)
	bot.Token = "rotated-token"
	rotated, err := registry.acquire(bot)
	require.NoError(t, err)
	registry.release(rotated)
	require.NotSame(t, first, rotated)
	require.Equal(t, "first-token", first.key.token, "downloader retains immutable credential identity")
	config.BotConfig.Proxy = "socks5://localhost:1080"
	proxied, err := registry.acquire(bot)
	require.NoError(t, err)
	registry.release(proxied)
	require.NotSame(t, rotated, proxied)
	request, err := http.NewRequestWithContext(t.Context(), http.MethodGet, bot.URL, nil)
	require.NoError(t, err)
	proxyURL, err := proxied.transport.Proxy(request)
	require.NoError(t, err)
	require.Equal(t, "socks5://localhost:1080", proxyURL.String())
	for range maxTelegramPhotoDownloaders + 1 {
		entry, err := registry.acquire(&tb.Bot{Token: "additional-token", URL: "http://additional.invalid"})
		require.NoError(t, err)
		registry.release(entry)
	}
	require.Len(t, registry.entries, maxTelegramPhotoDownloaders)
	registry.close()
	require.Empty(t, registry.entries)
}

func TestPhotoDownloaderPreservesSDKSentinelErrors(t *testing.T) {
	setupPhotoDownloaderConfig(t)
	for _, sdkError := range []*tb.Error{tb.ErrWrongFileID, tb.ErrUnauthorized} {
		t.Run(sdkError.Description, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": sdkError.Code, "description": sdkError.Description})
			}))
			defer server.Close()
			bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "photo-sdk-error-token", URL: server.URL})
			require.NoError(t, err)
			_, err = encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: t.Context()}}, &tb.Photo{File: tb.File{FileID: "photo"}})
			require.ErrorIs(t, err, sdkError)
			require.Contains(t, err.Error(), sdkError.Error())
		})
	}
}

func setupPhotoDownloaderConfig(t *testing.T) {
	t.Helper()
	oldConfig := config.BotConfig
	closeTelegramPhotoDownloaders()
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { closeTelegramPhotoDownloaders(); config.BotConfig = oldConfig })
}

func photoDownloaderImage(t *testing.T) []byte {
	t.Helper()
	var data bytes.Buffer
	require.NoError(t, png.Encode(&data, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	return data.Bytes()
}
