package agentv3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"csust-got/config"

	"github.com/stretchr/testify/require"
	"go.uber.org/zap"
	"go.uber.org/zap/zapcore"
	tb "gopkg.in/telebot.v3"
)

func TestPhotoContextIOStopsRenderReadsAndWrites(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	reader := bytes.NewReader([]byte("unread data"))
	var data [16]byte
	n, err := (photoContextReader{ctx: ctx, reader: reader}).Read(data[:])
	require.Zero(t, n)
	require.ErrorIs(t, err, context.Canceled)
	require.Equal(t, int64(0), reader.Size()-int64(reader.Len()))
	var buffer bytes.Buffer
	n, err = (photoContextWriter{ctx: ctx, writer: &buffer}).Write([]byte("unwritten data"))
	require.Zero(t, n)
	require.ErrorIs(t, err, context.Canceled)
	require.Empty(t, buffer.Bytes())
}

func TestPhotoContextSanitizedErrorRetainsCancellation(t *testing.T) {
	const token = "PRIVATE_TOKEN_NESTED_CANCELLATION"
	bot := &tb.Bot{Token: token, URL: "https://private-telegram.invalid"}
	for _, cause := range []error{context.Canceled, context.DeadlineExceeded} {
		t.Run(cause.Error(), func(t *testing.T) {
			requestErr := &url.Error{Op: "Post", URL: bot.URL + "/bot" + token + "/getFile", Err: fmt.Errorf("fixture transport: %w", cause)}
			err := fmt.Errorf("photo lookup: %w", safeTelegramPhotoError(requestErr, bot))
			require.ErrorIs(t, err, cause)
			for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), fmt.Sprintf("%#v", safeTelegramPhotoError(requestErr, bot))} {
				require.NotContains(t, text, token)
				require.NotContains(t, text, bot.URL)
			}
			var exposed *url.Error
			require.False(t, errors.As(err, &exposed), "unsafe URL errors are not exposed through Unwrap")
		})
	}
}

func TestPhotoContextDownloaderPreservesProxyAndEndpoint(t *testing.T) {
	originalConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = originalConfig })
	var imageData bytes.Buffer
	require.NoError(t, png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	var mu sync.Mutex
	var requests []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requests = append(requests, r.Method+" "+r.URL.String())
		mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			var payload struct {
				FileID string `json:"file_id"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil || payload.FileID != "photo" {
				http.Error(w, "bad getFile request", http.StatusBadRequest)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.png"}}`))
			return
		}
		_, _ = w.Write(imageData.Bytes())
	}))
	defer proxy.Close()
	config.BotConfig.Proxy = proxy.URL
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "photo-fixture-token", URL: "http://telegram.example.invalid/api-root"})
	require.NoError(t, err)
	tc := &TurnContext{Bot: bot, Config: &config.AgentConfig{}, V3: &AgentV3TurnState{renderCtx: t.Context()}}
	result, err := encodePhotoForLLM(tc, &tb.Photo{File: tb.File{FileID: "photo"}})
	require.NoError(t, err)
	require.True(t, strings.HasPrefix(result, "data:image/jpeg;base64,"))
	require.Equal(t, "http://telegram.example.invalid/api-root", bot.URL)
	require.Same(t, t.Context(), tc.V3.renderCtx)
	mu.Lock()
	defer mu.Unlock()
	require.Equal(t, []string{
		"POST http://telegram.example.invalid/api-root/botphoto-fixture-token/getFile",
		"GET http://telegram.example.invalid/api-root/file/botphoto-fixture-token/photo.png",
	}, requests)
}

func TestPhotoContextDownloaderKeepsSDKLookupError(t *testing.T) {
	originalConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = originalConfig })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ok":false,"error_code":400,"description":"fixture metadata rejection"}`))
	}))
	defer server.Close()
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: "photo-fixture-token", URL: server.URL})
	require.NoError(t, err)
	_, sdkErr := bot.File(&tb.File{FileID: "photo"})
	require.Error(t, sdkErr)
	_, err = encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: t.Context()}}, &tb.Photo{File: tb.File{FileID: "photo"}})
	require.Error(t, err)
	require.True(t, errors.Is(err, sdkErr) || strings.Contains(err.Error(), sdkErr.Error()), "private downloader preserves the SDK's API error")
}

func TestPhotoContextCancellationDoesNotAffectSharedBotOtherTurn(t *testing.T) {
	originalConfig := config.BotConfig
	config.BotConfig = config.NewBotConfig()
	t.Cleanup(func() { config.BotConfig = originalConfig })
	var imageData bytes.Buffer
	require.NoError(t, png.Encode(&imageData, image.NewNRGBA(image.Rect(0, 0, 2, 2))))
	lookupStarted := make(chan struct{})
	lookupCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/getFile") {
			var payload struct {
				FileID string `json:"file_id"`
			}
			if json.NewDecoder(r.Body).Decode(&payload) != nil {
				http.Error(w, "bad fixture request", http.StatusBadRequest)
				return
			}
			if payload.FileID == "blocked" {
				close(lookupStarted)
				<-r.Context().Done()
				close(lookupCanceled)
				return
			}
			_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"healthy","file_path":"photo.png"}}`))
			return
		}
		_, _ = io.Copy(w, bytes.NewReader(imageData.Bytes()))
	}))
	defer server.Close()
	const token = "PRIVATE_TOKEN_SHARED_PHOTO_CONTEXT"
	bot, err := tb.NewBot(tb.Settings{Offline: true, Token: token, URL: server.URL})
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	blockedTC := &TurnContext{Bot: bot, Config: &config.AgentConfig{}, V3: &AgentV3TurnState{renderCtx: ctx}}
	healthyTC := &TurnContext{Bot: bot, Config: blockedTC.Config, V3: &AgentV3TurnState{renderCtx: t.Context()}}
	blocked := make(chan error, 1)
	go func() {
		_, err := encodePhotoForLLM(blockedTC, &tb.Photo{File: tb.File{FileID: "blocked"}})
		blocked <- err
	}()
	select {
	case <-lookupStarted:
	case <-time.After(time.Second):
		t.Fatal("blocked lookup did not start")
	}
	healthy := make(chan error, 1)
	go func() {
		_, err := encodePhotoForLLM(healthyTC, &tb.Photo{File: tb.File{FileID: "healthy"}})
		healthy <- err
	}()
	select {
	case err := <-healthy:
		require.NoError(t, err)
	case <-time.After(time.Second):
		t.Fatal("other turn inherited blocked lookup context")
	}
	cancel()
	select {
	case err := <-blocked:
		require.ErrorIs(t, err, context.Canceled)
		require.NotContains(t, err.Error(), token)
		require.NotContains(t, fmt.Sprintf("%+v", err), server.URL)
	case <-time.After(time.Second):
		t.Fatal("blocked lookup did not honor cancellation")
	}
	select {
	case <-lookupCanceled:
	case <-time.After(time.Second):
		t.Fatal("lookup request remained detached")
	}
	require.Same(t, ctx, blockedTC.V3.renderCtx)
	require.Same(t, t.Context(), healthyTC.V3.renderCtx)
	require.Same(t, blockedTC.Bot, healthyTC.Bot)
	require.Equal(t, server.URL, bot.URL)
}

func TestPhotoContextErrorDoesNotExposeCredentialsOrResponseBody(t *testing.T) {
	for _, failure := range []string{"lookup API echo", "lookup API body echo", "lookup malformed HTTP", "download malformed HTTP", "download status"} {
		t.Run(failure, func(t *testing.T) {
			originalConfig := config.BotConfig
			config.BotConfig = config.NewBotConfig()
			t.Cleanup(func() { config.BotConfig = originalConfig })
			const token = "PRIVATE_TOKEN_MEDIA_ERROR_BOUNDARY"
			const rawBody = "RAW_RESPONSE_BODY_MUST_NOT_BE_LOGGED"
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				lookup := strings.HasSuffix(r.URL.Path, "/getFile")
				if lookup && strings.HasPrefix(failure, "download") {
					_, _ = w.Write([]byte(`{"ok":true,"result":{"file_id":"photo","file_path":"photo.jpg"}}`))
					return
				}
				if strings.HasPrefix(failure, "lookup API") {
					description := "file lookup failed at http://private-telegram.invalid/bot" + token + "/getFile"
					if failure == "lookup API body echo" {
						description = `{"token":"` + token + `","body":"` + rawBody + `"}`
					}
					_ = json.NewEncoder(w).Encode(map[string]any{"ok": false, "error_code": 400, "description": description})
					return
				}
				if failure == "download status" {
					w.WriteHeader(http.StatusServiceUnavailable)
					_, _ = w.Write([]byte(token + " " + rawBody))
					return
				}
				connection, buffer, err := w.(http.Hijacker).Hijack()
				if err != nil {
					return
				}
				defer connection.Close()
				_, _ = buffer.WriteString("INVALID_HTTP_" + token + "_" + rawBody + "\r\n\r\n")
				_ = buffer.Flush()
			}))
			defer server.Close()
			bot, err := tb.NewBot(tb.Settings{Offline: true, Token: token, URL: server.URL})
			require.NoError(t, err)
			_, err = encodePhotoForLLM(&TurnContext{Bot: bot, V3: &AgentV3TurnState{renderCtx: t.Context()}}, &tb.Photo{File: tb.File{FileID: "photo"}})
			require.Error(t, err)
			var logOutput bytes.Buffer
			logger := zap.New(zapcore.NewCore(zapcore.NewJSONEncoder(zap.NewProductionEncoderConfig()), zapcore.AddSync(&logOutput), zap.DebugLevel))
			logger.Warn("photo failed", zap.Error(err))
			for _, text := range []string{err.Error(), fmt.Sprintf("%+v", err), logOutput.String()} {
				require.NotContains(t, text, token)
				require.NotContains(t, text, server.URL)
				require.NotContains(t, text, "http://private-telegram.invalid")
				require.NotContains(t, text, rawBody)
			}
			if strings.HasPrefix(failure, "lookup API") {
				require.Contains(t, err.Error(), "400")
			}
			if failure == "download status" {
				require.Contains(t, err.Error(), "503")
			}
		})
	}
}
