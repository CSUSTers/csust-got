package agentv3

import (
	"bytes"
	"context"
	"csust-got/config"
	"csust-got/orm"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
	"golang.org/x/image/draw"
	tb "gopkg.in/telebot.v3"

	_ "golang.org/x/image/webp"
)

type imageContextSource string

const (
	imageContextSourceCurrent imageContextSource = "current"
	imageContextSourceReply   imageContextSource = "reply"
	imageContextSourceHistory imageContextSource = "history"
)

var (
	currentAlbumCompletionWait         = 500 * time.Millisecond
	currentAlbumCompletionPollInterval = 100 * time.Millisecond
	currentAlbumSiblingWindow          = 16
	loadStoredTelegramMessage          = orm.GetMessage
	loadStoredTelegramMessageContext   = orm.GetMessageContext
	encodeTelegramPhotoDataURL         = encodePhotoForLLM
	errMissingTelegramPhotoContext     = errors.New("missing telegram photo context")
)

type imageContextEntry struct {
	Source    imageContextSource
	MessageID int
	Caption   string
	DataURL   string
}

func buildUserMessage(text string, tc *TurnContext, history *RichHistory) *schema.Message {
	if !multimodalImageContextEnabled(tc) {
		return buildPlainUserMessage(text, tc)
	}

	entries := collectImageContextEntries(tc, history)
	if len(entries) == 0 {
		return buildPlainUserMessage(text, tc)
	}

	text = joinUserMessageSections(text, buildImageContextManifest(entries), strings.Join(collectDocumentHints(tc), "\n"))
	parts := make([]schema.MessageInputPart, 0, len(entries)+1)
	parts = append(parts, schema.MessageInputPart{
		Type: schema.ChatMessagePartTypeText,
		Text: text,
	})
	for _, entry := range entries {
		urlStr := entry.DataURL
		if imageBase64RawEnabled(tc) {
			urlStr = stripDataURIPrefix(urlStr)
		}
		parts = append(parts, schema.MessageInputPart{
			Type: schema.ChatMessagePartTypeImageURL,
			Image: &schema.MessageInputImage{
				MessagePartCommon: schema.MessagePartCommon{
					URL: &urlStr,
				},
			},
		})
	}

	return &schema.Message{
		Role:                  schema.User,
		UserInputMultiContent: parts,
	}
}

func buildPlainUserMessage(text string, tc *TurnContext) *schema.Message {
	sections := []string{text, strings.Join(collectImageToolHints(tc), "\n"), strings.Join(collectDocumentHints(tc), "\n")}
	return &schema.Message{
		Role:    schema.User,
		Content: joinUserMessageSections(sections...),
	}
}

func multimodalImageContextEnabled(tc *TurnContext) bool {
	return tc != nil &&
		tc.Config != nil &&
		tc.Config.Model != nil &&
		tc.Config.Model.Features.Image &&
		tc.Config.Features.Image
}

func imageBase64RawEnabled(tc *TurnContext) bool {
	return tc != nil &&
		tc.Config != nil &&
		tc.Config.Model != nil &&
		tc.Config.Model.Features.ImageBase64Raw
}

func stripDataURIPrefix(s string) string {
	if strings.HasPrefix(s, "data:") {
		if _, after, ok := strings.Cut(s, ","); ok {
			return after
		}
	}
	return s
}

func collectImageContextEntries(tc *TurnContext, history *RichHistory) []imageContextEntry {
	if tc == nil || tc.Message == nil {
		return nil
	}
	if history == nil {
		history = &RichHistory{}
	}

	seen := make(map[int]struct{})
	var entries []imageContextEntry

	entries = append(entries, collectImageEntriesFromMessages(tc, loadCurrentAlbumMessagesContext(agentV3RenderContext(tc), tc.Message), imageContextSourceCurrent, seen)...)

	if tc.Message.ReplyTo != nil {
		entries = append(entries, collectImageEntriesFromMessages(tc, []*tb.Message{tc.Message.ReplyTo}, imageContextSourceReply, seen)...)
	}

	entries = append(entries, collectImageEntriesFromMessages(tc, history.FullMessages, imageContextSourceHistory, seen)...)
	return entries
}

func collectImageEntriesFromMessages(
	tc *TurnContext,
	messages []*tb.Message,
	source imageContextSource,
	seen map[int]struct{},
) []imageContextEntry {
	entries := make([]imageContextEntry, 0, len(messages))
	for _, msg := range messages {
		if agentV3RenderContext(tc).Err() != nil {
			break
		}
		if msg == nil || msg.Photo == nil {
			continue
		}
		if _, ok := seen[msg.ID]; ok {
			continue
		}

		dataURL, err := encodeTelegramPhotoDataURL(tc, msg.Photo)
		if err != nil {
			zap.L().Warn("agentv3: failed to encode image context photo",
				zap.String("source", string(source)),
				zap.Int("message_id", msg.ID),
				zap.Error(err),
			)
			continue
		}

		seen[msg.ID] = struct{}{}
		entries = append(entries, imageContextEntry{
			Source:    source,
			MessageID: msg.ID,
			Caption:   strings.TrimSpace(msg.Caption),
			DataURL:   dataURL,
		})
	}
	return entries
}

func loadCurrentAlbumMessages(msg *tb.Message) []*tb.Message {
	return loadCurrentAlbumMessagesContext(context.Background(), msg)
}

func loadCurrentAlbumMessagesContext(ctx context.Context, msg *tb.Message) []*tb.Message {
	if msg == nil {
		return nil
	}
	if msg.AlbumID == "" || msg.Chat == nil {
		return []*tb.Message{msg}
	}

	messages := map[int]*tb.Message{msg.ID: msg}
	deadline := time.Now().Add(currentAlbumCompletionWait)

	for ctx.Err() == nil {
		if ctx.Done() == nil {
			loadAlbumSiblingMessages(msg.Chat.ID, msg.ID, msg.AlbumID, messages)
		} else {
			loadAlbumSiblingMessagesContext(ctx, msg.Chat.ID, msg.ID, msg.AlbumID, messages)
		}
		if time.Now().After(deadline) {
			break
		}
		timer := time.NewTimer(currentAlbumCompletionPollInterval)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}

	ids := make([]int, 0, len(messages))
	for id := range messages {
		ids = append(ids, id)
	}
	sort.Ints(ids)

	result := make([]*tb.Message, 0, len(ids))
	for _, id := range ids {
		result = append(result, messages[id])
	}
	return result
}

func loadAlbumSiblingMessages(chatID int64, messageID int, albumID string, messages map[int]*tb.Message) {
	loadAlbumSiblingMessagesContext(context.Background(), chatID, messageID, albumID, messages)
}

func loadAlbumSiblingMessagesContext(ctx context.Context, chatID int64, messageID int, albumID string, messages map[int]*tb.Message) {
	startID := max(messageID-currentAlbumSiblingWindow, 1)
	endID := messageID + currentAlbumSiblingWindow

	for id := startID; id <= endID; id++ {
		if ctx.Err() != nil {
			return
		}
		if _, ok := messages[id]; ok {
			continue
		}

		msg, err := loadStoredTelegramMessageContext(ctx, chatID, id)
		if err != nil || msg == nil || msg.AlbumID != albumID {
			continue
		}
		messages[id] = msg
	}
}

func buildImageContextManifest(entries []imageContextEntry) string {
	if len(entries) == 0 {
		return ""
	}

	var builder strings.Builder
	builder.WriteString("Image Context:\n")
	for i, entry := range entries {
		fmt.Fprintf(&builder, "%d. %s message %d", i+1, imageContextSourceLabel(entry.Source), entry.MessageID)
		if caption := summarizeImageCaption(entry.Caption); caption != "" {
			builder.WriteString(" - ")
			builder.WriteString(caption)
		}
		if i < len(entries)-1 {
			builder.WriteByte('\n')
		}
	}
	return builder.String()
}

func imageContextSourceLabel(source imageContextSource) string {
	switch source {
	case imageContextSourceCurrent:
		return "current"
	case imageContextSourceReply:
		return "reply"
	default:
		return "context"
	}
}

func summarizeImageCaption(caption string) string {
	caption = strings.TrimSpace(strings.ReplaceAll(caption, "\n", " "))
	if len(caption) > 80 {
		return caption[:77] + "..."
	}
	return caption
}

func collectImageToolHints(tc *TurnContext) []string {
	if tc == nil || tc.Message == nil {
		return nil
	}

	var hints []string
	if tc.Message.Photo != nil {
		hints = append(hints, fmt.Sprintf(
			"[This message contains an attached image (file_id: %s). Use the analyze_image tool to view and analyze it if needed.]",
			tc.Message.Photo.FileID,
		))
	}
	if tc.Message.ReplyTo != nil && tc.Message.ReplyTo.Photo != nil {
		hints = append(hints, fmt.Sprintf(
			"[Referenced message contains an image (file_id: %s). Use the analyze_image tool to view and analyze it if needed.]",
			tc.Message.ReplyTo.Photo.FileID,
		))
	}
	return hints
}

func collectDocumentHints(tc *TurnContext) []string {
	if tc == nil || tc.Message == nil {
		return nil
	}

	var hints []string
	if tc.Message.Document != nil {
		doc := tc.Message.Document
		hints = append(hints, fmt.Sprintf(
			"[This message has an attached file: %s (file_id: %s, mime: %s).]",
			doc.FileName, doc.FileID, doc.MIME,
		))
	}
	if tc.Message.ReplyTo != nil && tc.Message.ReplyTo.Document != nil {
		doc := tc.Message.ReplyTo.Document
		hints = append(hints, fmt.Sprintf(
			"[Referenced message has an attached file: %s (file_id: %s, mime: %s).]",
			doc.FileName, doc.FileID, doc.MIME,
		))
	}
	return hints
}

func joinUserMessageSections(sections ...string) string {
	nonEmpty := make([]string, 0, len(sections))
	for _, section := range sections {
		if strings.TrimSpace(section) == "" {
			continue
		}
		nonEmpty = append(nonEmpty, strings.TrimSpace(section))
	}
	return strings.Join(nonEmpty, "\n\n")
}

func encodePhotoForLLM(tc *TurnContext, photo *tb.Photo) (string, error) {
	if tc == nil || tc.Bot == nil || photo == nil {
		return "", errMissingTelegramPhotoContext
	}

	ctx, cancel := context.WithTimeout(agentV3RenderContext(tc), 10*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return "", err
	}
	transport := http.DefaultTransport
	if config.BotConfig != nil && config.BotConfig.Proxy != "" {
		proxy, err := url.Parse(config.BotConfig.Proxy)
		if err != nil {
			return "", safeTelegramPhotoError(err, tc.Bot)
		}
		local := &http.Transport{Proxy: http.ProxyURL(proxy)}
		defer local.CloseIdleConnections()
		transport = local
	}
	// Telebot has no context-aware File API. Use a private downloader, not the
	// live bot's client, so both metadata and body reads honor the load deadline.
	bot, err := tb.NewBot(tb.Settings{Token: tc.Bot.Token, URL: tc.Bot.URL, Offline: true, Client: &http.Client{Transport: photoContextTransport{ctx: ctx, base: transport}}})
	if err != nil {
		return "", safeTelegramPhotoError(err, tc.Bot)
	}
	file := tb.File{FileID: photo.FileID}
	reader, err := bot.File(&file)
	if err != nil {
		return "", fmt.Errorf("failed to download photo: %w", safeTelegramPhotoError(err, tc.Bot))
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(photoContextReader{ctx: ctx, reader: reader}, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read photo data: %w", safeTelegramPhotoError(err, tc.Bot))
	}

	original, _, err := image.Decode(photoContextReader{ctx: ctx, reader: bytes.NewReader(data)})
	if err != nil {
		return "", fmt.Errorf("failed to decode photo: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	bounds := original.Bounds()
	width, height := bounds.Dx(), bounds.Dy()
	if tc.Config != nil {
		width, height = tc.Config.Features.ImageResize(width, height)
	}

	resized := original
	if width != bounds.Dx() || height != bounds.Dy() {
		dst := image.NewRGBA(image.Rect(0, 0, width, height))
		draw.ApproxBiLinear.Scale(dst, dst.Rect, original, bounds, draw.Over, nil)
		resized = dst
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}

	buf := bytes.NewBuffer(nil)
	if err := jpeg.Encode(photoContextWriter{ctx: ctx, writer: buf}, resized, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("failed to encode photo as jpeg: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	if err := ctx.Err(); err != nil {
		return "", err
	}
	return "data:image/jpeg;base64," + encoded, nil
}

var telegramPhotoErrorURL = regexp.MustCompile(`[a-zA-Z][a-zA-Z0-9+.-]*://[^\s]+`)

type telegramPhotoError struct {
	message string
	cause   error
}

func (e *telegramPhotoError) Error() string { return e.message }

// Do not unwrap the unsafe cause into verbose logging; preserve identity via Is.
func (e *telegramPhotoError) Is(target error) bool { return errors.Is(e.cause, target) }

func (e *telegramPhotoError) Format(state fmt.State, _ rune) { _, _ = io.WriteString(state, e.message) }

func safeTelegramPhotoError(err error, bot *tb.Bot) error {
	for {
		var requestErr *url.Error
		if !errors.As(err, &requestErr) {
			break
		}
		err = requestErr.Err
	}
	message := "Telegram photo operation failed"
	switch {
	case errors.Is(err, context.Canceled):
		message = context.Canceled.Error()
	case errors.Is(err, context.DeadlineExceeded):
		message = context.DeadlineExceeded.Error()
	case strings.HasPrefix(err.Error(), "telegram:"):
		message = err.Error()
		if len(message) > 256 || strings.ContainsAny(message, "\r\n{}<>") {
			message = "Telegram API request rejected"
			if index := strings.LastIndex(err.Error(), "("); index >= 0 {
				code, parseErr := strconv.Atoi(strings.TrimSuffix(err.Error()[index+1:], ")"))
				if parseErr == nil && code >= 100 && code <= 599 {
					message = fmt.Sprintf("Telegram API request rejected (%d)", code)
				}
			}
		}
	case strings.HasPrefix(err.Error(), "telebot: expected status 200 but got "):
		status := strings.TrimPrefix(err.Error(), "telebot: expected status 200 but got ")
		fields := strings.Fields(status)
		if len(fields) > 0 {
			code, parseErr := strconv.Atoi(fields[0])
			if parseErr == nil && code >= 100 && code <= 599 {
				message = fmt.Sprintf("telebot: expected status 200 but got %d %s", code, http.StatusText(code))
			}
		}
	}
	message = telegramPhotoErrorURL.ReplaceAllString(message, "[redacted URL]")
	for _, secret := range []string{bot.Token, url.PathEscape(bot.Token), url.QueryEscape(bot.Token), bot.URL} {
		if secret != "" {
			message = strings.ReplaceAll(message, secret, "[redacted]")
		}
	}
	return &telegramPhotoError{message: message, cause: err}
}

func agentV3RenderContext(tc *TurnContext) context.Context {
	if tc != nil && tc.V3 != nil && tc.V3.renderCtx != nil {
		return tc.V3.renderCtx
	}
	return context.Background()
}

type photoContextTransport struct {
	ctx  context.Context
	base http.RoundTripper
}

func (t photoContextTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	return t.base.RoundTrip(req.WithContext(t.ctx))
}

type photoContextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (r photoContextReader) Read(data []byte) (int, error) {
	if err := r.ctx.Err(); err != nil {
		return 0, err
	}
	return r.reader.Read(data)
}

type photoContextWriter struct {
	ctx    context.Context
	writer io.Writer
}

func (w photoContextWriter) Write(data []byte) (int, error) {
	if err := w.ctx.Err(); err != nil {
		return 0, err
	}
	return w.writer.Write(data)
}
