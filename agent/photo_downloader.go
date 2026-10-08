package agentv3

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"csust-got/config"

	tb "gopkg.in/telebot.v3"
)

const maxTelegramPhotoDownloaders = 8

type telegramPhotoDownloaderKey struct {
	bot      *tb.Bot
	token    string
	endpoint string
	proxy    string
}

type telegramPhotoDownloader struct {
	key          telegramPhotoDownloaderKey
	client       *http.Client
	legacyClient *http.Client
	transport    *http.Transport
	inUse        int
	retired      bool
}

type telegramPhotoDownloaderRegistry struct {
	mu      sync.Mutex
	entries []*telegramPhotoDownloader
	retired map[*telegramPhotoDownloader]struct{}
}

type telegramPhotoStatusError struct{ code int }

func (e telegramPhotoStatusError) Error() string {
	return fmt.Sprintf("telebot: expected status 200 but got %d %s", e.code, http.StatusText(e.code))
}

var telegramPhotoDownloaders telegramPhotoDownloaderRegistry

func (r *telegramPhotoDownloaderRegistry) acquire(bot *tb.Bot) (*telegramPhotoDownloader, error) {
	key := telegramPhotoDownloaderKey{bot: bot, token: bot.Token, endpoint: bot.URL}
	if config.BotConfig != nil {
		key.proxy = config.BotConfig.Proxy
	}
	r.mu.Lock()
	var idle *http.Transport
	defer func() {
		r.mu.Unlock()
		if idle != nil {
			idle.CloseIdleConnections()
		}
	}()
	for i, entry := range r.entries {
		if entry.key == key {
			copy(r.entries[i:], r.entries[i+1:])
			r.entries[len(r.entries)-1] = entry
			entry.inUse++
			return entry, nil
		}
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	if key.proxy != "" {
		proxy, err := url.Parse(key.proxy)
		if err != nil {
			return nil, err
		}
		transport.Proxy = http.ProxyURL(proxy)
	}
	entry := &telegramPhotoDownloader{
		key: key, transport: transport,
		client:       &http.Client{Transport: transport},
		legacyClient: &http.Client{Transport: transport, Timeout: time.Minute},
		inUse:        1,
	}
	if len(r.entries) == maxTelegramPhotoDownloaders {
		idle = r.retire(r.entries[0])
		copy(r.entries, r.entries[1:])
		r.entries = r.entries[:len(r.entries)-1]
	}
	r.entries = append(r.entries, entry)
	return entry, nil
}

// In-flight photos retain their transport until the last release; active requests are not cancelled.
func closeTelegramPhotoDownloaders() {
	telegramPhotoDownloaders.close()
}

func (r *telegramPhotoDownloaderRegistry) close() {
	r.mu.Lock()
	idle := make([]*http.Transport, 0, len(r.entries))
	for _, entry := range r.entries {
		if transport := r.retire(entry); transport != nil {
			idle = append(idle, transport)
		}
	}
	r.entries = nil
	r.mu.Unlock()
	for _, transport := range idle {
		transport.CloseIdleConnections()
	}
}

// retire is called with r.mu held; callers close returned transports after unlocking.
func (r *telegramPhotoDownloaderRegistry) retire(entry *telegramPhotoDownloader) *http.Transport {
	entry.retired = true
	if entry.inUse == 0 {
		return entry.transport
	}
	if r.retired == nil {
		r.retired = make(map[*telegramPhotoDownloader]struct{})
	}
	r.retired[entry] = struct{}{}
	return nil
}

func (r *telegramPhotoDownloaderRegistry) release(entry *telegramPhotoDownloader) {
	r.mu.Lock()
	entry.inUse--
	closeIdle := entry.inUse == 0 && entry.retired
	if closeIdle {
		delete(r.retired, entry)
		if len(r.retired) == 0 {
			r.retired = nil
		}
	}
	r.mu.Unlock()
	if closeIdle {
		entry.transport.CloseIdleConnections()
	}
}

func (d *telegramPhotoDownloader) file(ctx context.Context, fileID string) (io.ReadCloser, error) {
	file, err := d.lookup(ctx, fileID)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, d.key.endpoint+"/file/bot"+d.key.token+"/"+file.FilePath, nil)
	if err != nil {
		return nil, err
	}
	resp, err := d.httpClient(ctx).Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		_ = resp.Body.Close()
		return nil, telegramPhotoStatusError{code: resp.StatusCode}
	}
	return resp.Body, nil
}

func (d *telegramPhotoDownloader) lookup(ctx context.Context, fileID string) (tb.File, error) {
	payload, err := json.Marshal(map[string]string{"file_id": fileID})
	if err != nil {
		return tb.File{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, d.key.endpoint+"/bot"+d.key.token+"/getFile", bytes.NewReader(payload))
	if err != nil {
		return tb.File{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := d.httpClient(ctx).Do(req)
	if err != nil {
		return tb.File{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	data, err := io.ReadAll(photoContextReader{ctx: ctx, reader: resp.Body})
	if ctx.Err() != nil {
		return tb.File{}, ctx.Err()
	}
	if err != nil {
		return tb.File{}, err
	}
	var result struct {
		OK          bool    `json:"ok"`
		Code        int     `json:"error_code"`
		Description string  `json:"description"`
		File        tb.File `json:"result"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return tb.File{}, err
	}
	if !result.OK {
		if err := tb.Err(result.Description); err != nil {
			return tb.File{}, err
		}
		return tb.File{}, tb.NewError(result.Code, result.Description, result.Description)
	}
	return result.File, nil
}

func (d *telegramPhotoDownloader) httpClient(ctx context.Context) *http.Client {
	if ctx.Done() == nil {
		return d.legacyClient
	}
	return d.client
}
