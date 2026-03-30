//go:build !386 && !arm

package chatv2

import (
	"bytes"
	"csust-got/orm"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"io"
	"sort"
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

func collectImageContextEntries(tc *TurnContext, history *RichHistory) []imageContextEntry {
	if tc == nil || tc.Message == nil {
		return nil
	}
	if history == nil {
		history = &RichHistory{}
	}

	seen := make(map[int]struct{})
	var entries []imageContextEntry

	entries = append(entries, collectImageEntriesFromMessages(tc, loadCurrentAlbumMessages(tc.Message), imageContextSourceCurrent, seen)...)

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
		if msg == nil || msg.Photo == nil {
			continue
		}
		if _, ok := seen[msg.ID]; ok {
			continue
		}

		dataURL, err := encodeTelegramPhotoDataURL(tc, msg.Photo)
		if err != nil {
			zap.L().Warn("chatv2: failed to encode image context photo",
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
	if msg == nil {
		return nil
	}
	if msg.AlbumID == "" || msg.Chat == nil {
		return []*tb.Message{msg}
	}

	messages := map[int]*tb.Message{msg.ID: msg}
	deadline := time.Now().Add(currentAlbumCompletionWait)

	for {
		loadAlbumSiblingMessages(msg.Chat.ID, msg.ID, msg.AlbumID, messages)
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(currentAlbumCompletionPollInterval)
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
	startID := messageID - currentAlbumSiblingWindow
	if startID < 1 {
		startID = 1
	}
	endID := messageID + currentAlbumSiblingWindow

	for id := startID; id <= endID; id++ {
		if _, ok := messages[id]; ok {
			continue
		}

		msg, err := loadStoredTelegramMessage(chatID, id)
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

	file, err := tc.Bot.FileByID(photo.FileID)
	if err != nil {
		return "", fmt.Errorf("failed to get photo file info: %w", err)
	}
	reader, err := tc.Bot.File(&file)
	if err != nil {
		return "", fmt.Errorf("failed to download photo: %w", err)
	}
	defer func() { _ = reader.Close() }()

	data, err := io.ReadAll(io.LimitReader(reader, 10*1024*1024))
	if err != nil {
		return "", fmt.Errorf("failed to read photo data: %w", err)
	}

	original, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return "", fmt.Errorf("failed to decode photo: %w", err)
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

	buf := bytes.NewBuffer(nil)
	if err := jpeg.Encode(buf, resized, &jpeg.Options{Quality: 90}); err != nil {
		return "", fmt.Errorf("failed to encode photo as jpeg: %w", err)
	}

	encoded := base64.StdEncoding.EncodeToString(buf.Bytes())
	return "data:image/jpeg;base64," + encoded, nil
}
