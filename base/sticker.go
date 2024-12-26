// nolint: goconst
package base

import (
	"archive/zip"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"regexp"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/sync/errgroup"

	"github.com/cespare/xxhash/v2"
	ffmpeg_go "github.com/u2takey/ffmpeg-go"

	// nolint: revive
	_ "golang.org/x/image/webp"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"
	"gopkg.in/yaml.v3"

	"csust-got/entities"
	"csust-got/log"
	"csust-got/orm"
	"csust-got/util"
	"csust-got/util/ffconv"
)

// var illegalFilenameChars = []string{"\\", "/", ":", "*", "?", "\"", "'", "<", ">", "|", "\t", "\n", "\r", "\u0000", "\ufffd"}
var illegalFilenameCharsPatt = regexp.MustCompile(`[\\/:*?"'<>|\t\n\r\x00\x{fffd}]+`)

func replaceIllegalFilenameChars(s string, replacer func(in string) string) string {
	if illegalFilenameCharsPatt.FindStringIndex(s) == nil {
		log.Debug("no illegal filename chars found in string", zap.String("s", s))
		return s
	}
	r := illegalFilenameCharsPatt.ReplaceAllStringFunc(s, replacer)
	log.Debug("replace illegal filename chars", zap.String("s", s), zap.String("ret", r))
	return r
}

func replaceIllegalFilenameCharsWithString(s string, r string) string {
	return replaceIllegalFilenameChars(s, func(in string) string {
		return r
	})
}

type stickerOpts struct {
	Format string `json:"format"`
	Pack   bool   `json:"pack"`
	Vf     string `json:"videoformat"`
	Sf     string `json:"stickerformat"`

	// pack format
	pf string

	nocache bool
}

func defaultOpts() stickerOpts {
	return stickerOpts{
		Format: "",
		Pack:   false,
	}
}

func defaultOptsWithConfig(m map[string]string) stickerOpts {
	o := defaultOpts()
	o = o.merge(m, false)
	return o
}

// nolint: gocritic
func (o stickerOpts) merge(m map[string]string, final bool) stickerOpts {
	if final {
		sets := make(map[string]struct{}, len(m))
		for k := range m {
			ok, kk, _ := normalizeParams(k, "")
			if ok {
				sets[kk] = struct{}{}
			}
		}

		if _, ok := sets["format"]; ok {
			if _, ok = sets["videoformat"]; !ok {
				o.Vf = ""
			}

			if _, ok = sets["stickerformat"]; !ok {
				o.Sf = ""
			}
		}
	}

	for k, v := range m {
		k = strings.ToLower(k)
		switch k {
		case "format", "f":
			o.Format = v
		case "pack", "p":
			if slices.Contains([]string{"", "true", "1"}, strings.ToLower(v)) {
				o.Pack = true
			} else if strings.ToLower(v) == "false" {
				o.Pack = false
			}
		case "vf", "videoformat":
			o.Vf = v
		case "sf", "stickerformat":
			o.Sf = v
		case "pf", "packformat":
			o.pf = v
		case "nocache":
			if strings.ToLower(v) == "false" {
				o.nocache = false
			} else if final {
				// only final can do
				o.nocache = true
			}
		}
	}

	return o
}

// nolint: gocritic
func (o stickerOpts) VideoFormat() string {
	if o.Vf != "" {
		return o.Vf
	}
	return o.Format
}

// nolint: gocritic
func (o stickerOpts) StickerFormat() string {
	if o.Sf != "" {
		return o.Sf
	}
	return o.Format
}

// nolint: gocritic
func (o stickerOpts) FileExt(video bool) string {
	if video {
		f := o.VideoFormat()
		if f == "" {
			f = "webm"
		}
		return "." + f
	}
	f := o.StickerFormat()
	switch f {
	case "":
		f = "webp"
	case "jpg", "jpeg":
		f = "jpg"
	}
	return "." + f
}

// nolint: gocritic
func (o stickerOpts) NotConvert(s *tb.Sticker) bool {
	if s.Video {
		switch o.VideoFormat() {
		case "", "webm":
			return true
		}
	} else {
		switch o.StickerFormat() {
		case "", "webp":
			return true
		}
	}

	return false
}

// GetSticker will download sticker file, and convert to expected format, and send to chat
func GetSticker(ctx tb.Context) error {
	var msg = ctx.Message()
	var sticker *tb.Sticker

	if ctx.Chat().Type == tb.ChatPrivate && msg.Sticker != nil {
		sticker = msg.Sticker
	} else if replyTo := msg.ReplyTo; replyTo != nil && replyTo.Sticker != nil {
		sticker = replyTo.Sticker
	} else {
		// not found sticker error
		log.Debug("sticker not found", zap.Any("msg", msg))
		err := ctx.Reply("please use this command when reply a sticker message, or send a sticker in PM")
		if err != nil {
			return err
		}
		return nil
	}

	if sticker.Animated {
		return ctx.Reply("animated sticker is not supported")
	} else if sticker.PremiumAnimation != nil {
		// TODO: I dont know how to handle premium animation sticker
		return ctx.Reply("premium animation sticker is not supported")
	}

	userID := msg.Sender.ID
	opt := defaultOpts()
	userConfig, err := orm.GetIWantConfig(userID)
	if err == nil {
		opt = defaultOptsWithConfig(userConfig)
	}

	if msg.Text != "" {
		o, err := parseOpts(msg.Text)
		if err != nil {
			log.Error("parse command error", zap.String("text", msg.Text), zap.Error(err))
			err1 := ctx.Reply("failed to parse command args")
			if err1 != nil {
				return err
			}
			return err
		}
		opt = opt.merge(o, true)
	}

	if !opt.Pack {
		filename := replaceIllegalFilenameCharsWithString(sticker.SetName, "_")
		emoji := sticker.Emoji
		if sticker.CustomEmoji != "" {
			emoji += " " + sticker.CustomEmoji
		}

		// send video is sticker is video
		if sticker.Video {
			return sendVideoSticker(ctx, sticker, filename, emoji, &opt)
		}

		return sendImageSticker(ctx, sticker, filename, emoji, &opt)
	}
	return sendStickerPack(ctx, sticker, &opt)
}

func sendImageSticker(ctx tb.Context, sticker *tb.Sticker, filename string, emoji string, opt *stickerOpts) error {
	f := opt.StickerFormat()

	reader, err := ctx.Bot().File(&sticker.File)
	if err != nil {
		err1 := ctx.Reply("failed to get sticker file")
		return errors.Join(err, err1)
	}

	defer func(reader io.ReadCloser) {
		err = reader.Close()
		if err != nil {
			log.Error("failed to close reader", zap.Error(err))
		}
	}(reader)

	// send origin file with `format=[webp]`
	switch f {
	case "webp", "":
		sendFile := &tb.Document{
			File:                 tb.FromReader(reader),
			FileName:             filename + ".webp",
			Thumbnail:            sticker.Thumbnail,
			Caption:              emoji,
			DisableTypeDetection: true,
		}
		return ctx.Reply(sendFile)
	}

	// convert image format to params targeted
	img, _, err := image.Decode(reader)
	if err != nil {
		err1 := ctx.Reply("failed to convert image format")
		return errors.Join(err, err1)
	}

	bs := bytes.NewBuffer(nil)
	f = strings.ToLower(f)
	switch f {
	case "jpg", "jpeg":
		filename += ".jpg"
		err := jpeg.Encode(bs, img, &jpeg.Options{Quality: 100})
		if err != nil {
			err1 := ctx.Reply("failed to convert image format")
			return errors.Join(err, err1)
		}
	case "png":
		filename += ".png"
		err := png.Encode(bs, img)
		if err != nil {
			err1 := ctx.Reply("failed to convert image format")
			return errors.Join(err, err1)
		}
	case "gif":
		filename += ".gif"
		err := gif.Encode(bs, img, &gif.Options{NumColors: 255})
		if err != nil {
			err1 := ctx.Reply("failed to convert image format")
			return errors.Join(err, err1)
		}
	default:
		return ctx.Reply("unknown image format")
	}

	sendFile := &tb.Document{
		File:                 tb.FromReader(bs),
		FileName:             filename,
		Caption:              emoji,
		DisableTypeDetection: true,
	}

	return ctx.Reply(sendFile)
}

func sendVideoSticker(ctx tb.Context, sticker *tb.Sticker, filename string, emoji string, opt *stickerOpts) error {
	f := opt.VideoFormat()

	reader, err := ctx.Bot().File(&sticker.File)
	if err != nil {
		err1 := ctx.Reply("failed to get sticker file")
		return errors.Join(err, err1)
	}

	defer func(reader io.ReadCloser) {
		err = reader.Close()
		if err != nil {
			log.Error("failed to close reader", zap.Error(err))
		}
	}(reader)

	f = strings.ToLower(f)
	switch f {
	case "", "webm":
		sendFile := &tb.Document{
			File:                 tb.FromReader(reader),
			FileName:             filename + ".webm",
			Caption:              emoji,
			DisableTypeDetection: true,
		}
		return ctx.Reply(sendFile)
	case "gif":
		ff := ffconv.FFConv{LogCmd: true}
		r, errCh := ff.Convert2GifFromReader(reader, "webm")
		tempFile, err0 := os.CreateTemp("", "*.gif")
		if err0 != nil {
			log.Error("failed to create temp file", zap.Error(err0))
			err1 := ctx.Reply("convert to gif failed")
			return errors.Join(err0, err1)
		}
		defer func() {
			_ = tempFile.Close()
			_ = os.Remove(tempFile.Name())
		}()

		_, err0 = io.Copy(tempFile, r)
		if err0 != nil {
			log.Error("failed to copy", zap.Error(err0))
			err1 := ctx.Reply("convert to gif failed")
			return errors.Join(err0, err1)
		}

		select {
		case err := <-errCh:
			if err != nil {
				log.Error("failed to convert", zap.Error(err))
				err1 := ctx.Reply("convert to gif failed")
				return errors.Join(err, err1)
			}
		case <-time.After(time.Second * 5):
			log.Error("wait ffmpeg exec result timeout")
			return ctx.Reply("convert to gif failed")
		}

		sendFile := &tb.Document{
			File:                 tb.FromDisk(tempFile.Name()),
			FileName:             filename + ".gif",
			Caption:              emoji,
			DisableTypeDetection: true,
		}
		return ctx.Reply(sendFile)
	case "mp4", "png", "apng", "webp":
		ff := ffconv.FFConv{LogCmd: true}
		outputArgs := []ffmpeg_go.KwArgs{}
		if f == "png" || f == "apng" {
			outputArgs = append(outputArgs, ffmpeg_go.KwArgs{
				"plays": "0",
				"f":     "apng",
				"c:v":   "apng",
			})
		} else if f == "webp" {
			outputArgs = append(outputArgs, ffmpeg_go.KwArgs{
				"plays": "0",
				"f":     "webp",
				"c:v":   "libwebp",
			})
		}

		cc, cancel := context.WithCancel(context.Background())
		defer cancel()
		r, errCh := ff.ConvertPipe2File(cc, reader, "webm", nil, filename+"."+f, outputArgs...)
		defer func() {
			_ = r.Close()
		}()
		select {
		case err := <-errCh:
			if err != nil {
				log.Error("failed to convert", zap.Error(err))
				err1 := ctx.Reply("convert to mp4 failed")
				return errors.Join(err, err1)
			}
		case <-time.After(time.Second * 30):
			cancel()
			log.Error("wait ffmpeg exec result timeout", zap.String("filename", filename), zap.String("convert_format", f))
			return ctx.Reply("convert to mp4 failed")
		}
		sendFile := &tb.Document{
			File:                 tb.FromReader(r),
			FileName:             filename + "." + f,
			Caption:              emoji,
			DisableTypeDetection: true,
		}
		return ctx.Reply(sendFile)
	default:
		return ctx.Reply(fmt.Sprintf("not implement `%s` format for video sticker yet", f))
	}
}

func sendStickerPack(ctx tb.Context, sticker *tb.Sticker, opt *stickerOpts) error {
	stickerSet, err := ctx.Bot().StickerSet(sticker.SetName)
	if err != nil {
		err2 := ctx.Reply("failed to get sticker set")
		return errors.Join(err, err2)
	}

	if !opt.nocache {
		keys, err := getFileCacheKeys(stickerSet.Name, opt)
		if err != nil {
			log.Error("failed to get file cache keys", zap.Error(err))
			goto process
		}

		fileCache, err := orm.GetFileCache(keys, time.Hour*24*7)
		if err == nil && fileCache != nil && fileCache.FileId != "" {
			file, err := ctx.Bot().FileByID(fileCache.FileId)
			if err != nil {
				log.Error("failed to get file by id", zap.Error(err))
				goto process
			}
			err = ctx.Reply(&tb.Document{
				FileName: fileCache.Filename,
				File:     file,
			})
			if err != nil {
				log.Error("failed to send file", zap.Error(err))
				return ctx.Reply("failed to send file")
			}

			return nil
		}
		log.Error("failed to get file cache, continue process", zap.Error(err))
	}

process:

	// TODO support other compression format
	// pf := opt.pf
	// if pf == "" {
	// 	pf = "zip"
	// }

	tempDir, err := os.MkdirTemp("", "telebot")
	if err != nil {
		err2 := ctx.Reply("process failed")
		return errors.Join(err, err2)
	}
	defer func() {
		_ = os.RemoveAll(tempDir)
	}()

	if len(stickerSet.Stickers) == 0 {
		return ctx.Reply("sticker set is empty")
	}

	outputFiles := make([]string, 0, len(stickerSet.Stickers))

	lastNotify := time.Now()
	_ = ctx.Notify(tb.ChoosingSticker)

	var MaxTask = runtime.NumCPU() + 2
	taskGroup, cc := errgroup.WithContext(context.Background())
	taskGroup.SetLimit(MaxTask)

	replyMsg := ""
	replyMsgLock := sync.Mutex{}

loop:
	for i := range stickerSet.Stickers {
		s := &stickerSet.Stickers[i]

		select {
		case <-cc.Done():
			break loop
		default:
		}

		now := time.Now()
		if now.Sub(lastNotify) > time.Second*3 {
			_ = ctx.Notify(tb.ChoosingSticker)
			lastNotify = now
		}

		emoji := s.Emoji
		if emoji != "" {
			emoji = "_" + emoji
		}
		filename := fmt.Sprintf("%s_%03d%s%s", stickerSet.Name, i+1, emoji, opt.FileExt(s.Video))
		outputFiles = append(outputFiles, filename)

		taskGroup.Go(func() error {

			// TODO reduce complexity by move some code to function
			// nolint: nestif,gocritic
			if opt.NotConvert(s) {

				of, err := os.OpenFile(path.Join(tempDir, filename), os.O_CREATE|os.O_RDWR, 0o640)
				if err != nil {
					return err
				}
				defer func() { _ = of.Close() }()

				fileR, err := ctx.Bot().File(&s.File)
				if err != nil {
					return err
				}
				defer func() { _ = fileR.Close() }()

				_, err = io.Copy(of, fileR)
				if err != nil {
					return err
				}
			} else if s.Video {
				f := opt.VideoFormat()

				ff := ffconv.FFConv{LogCmd: true}
				var outputArgs []ffmpeg_go.KwArgs

				// limit ffmpeg threads
				// outputArgs = append(outputArgs,
				// 	ffmpeg_go.KwArgs{"threads": FFmpegThreadsPerTask})

				fileR, err := ctx.Bot().File(&s.File)
				if err != nil {
					return err
				}

				input := ffconv.GetPipeInputStream("webm")
				defer func() {
					_ = fileR.Close()
				}()
				switch f {
				case "gif":
					input = ffconv.GetGifPaletteVfStream(input)
					outputArgs = append(outputArgs, ffmpeg_go.KwArgs{
						"loop": "0",
						"c:v":  "gif",
						"f":    "gif",
					})
				case "mp4":
					// nothing to do
				case "png", "apng":
					outputArgs = append(outputArgs, ffmpeg_go.KwArgs{
						"loop": "0",
						"c:v":  "apng",
						"f":    "apng",
					})
				case "webp":
					outputArgs = append(outputArgs, ffmpeg_go.KwArgs{
						"loop": "0",
						"c:v":  "libwebp",
						"f":    "webp",
					})
				}
				ccc, cancel := context.WithTimeout(cc, time.Second*120)
				defer cancel()
				_, errCh := ff.ConvertPipe2File(ccc, fileR, "", input, path.Join(tempDir, filename), outputArgs...)
				select {
				case err := <-errCh:
					if err != nil {
						log.Error("failed to convert", zap.Error(err))
					}
					return err
				case <-ccc.Done():
					if errors.Is(ccc.Err(), context.DeadlineExceeded) {
						log.Error("wait ffmpeg exec result timeout", zap.String("filename", filename), zap.String("convert_format", f))

						replyMsgLock.Lock()
						if replyMsg == "" {
							replyMsg = "convert video sticker failed"
						}
						replyMsgLock.Unlock()

						return ccc.Err()
					}
				}
			} else {
				f := opt.StickerFormat()

				fileR, err := ctx.Bot().File(&s.File)
				if err != nil {
					return err
				}

				defer func() {
					_ = fileR.Close()
				}()

				img, _, err1 := image.Decode(fileR)
				if err1 != nil {
					replyMsgLock.Lock()
					if replyMsg == "" {
						replyMsg = "failed to decode image"
					}
					replyMsgLock.Unlock()

					return err
				}

				of, err1 := os.OpenFile(path.Join(tempDir, filename), os.O_CREATE|os.O_WRONLY, 0o640)
				if err1 != nil {
					return err
				}
				defer func() { _ = of.Close() }()

				var err2 error
				switch f {
				case "jpg", "jpeg":
					err2 = jpeg.Encode(of, img, &jpeg.Options{Quality: 100})
				case "png":
					err2 = png.Encode(of, img)
				case "gif":
					err2 = gif.Encode(of, img, &gif.Options{NumColors: 255})
				default:
					replyMsgLock.Lock()
					if replyMsg == "" {
						replyMsg = fmt.Sprintf("unknown target image format: %s", f)
					}
					replyMsgLock.Unlock()

					// TODO use static error
					// nolint: err113
					return errors.New("unknown target image format")
				}

				if err2 != nil {
					replyMsgLock.Lock()
					if replyMsg == "" {
						replyMsg = "failed to convert image format"
					}
					replyMsgLock.Unlock()

					return err
				}
			}

			return nil
		})
	}

	if err := taskGroup.Wait(); err != nil {
		log.Error("failed to convert sticker", zap.Error(err))
		if replyMsg == "" {
			replyMsg = "process failed"
		}
		return ctx.Reply(replyMsg)
	}

	if len(outputFiles) == 0 {
		return ctx.Reply("no files found, maybe something error")
	}

	_ = ctx.Notify(tb.UploadingDocument)

	packFile, err := os.CreateTemp("", "*.zip")
	if err != nil {
		err2 := ctx.Reply("process failed")
		return errors.Join(err, err2)
	}
	defer func() {
		_ = os.Remove(packFile.Name())
	}()
	compress := zip.NewWriter(packFile)
	for _, f := range outputFiles {
		w, err1 := compress.Create(f)
		if err1 != nil {
			err2 := ctx.Reply("process failed")
			return errors.Join(err1, err2)
		}

		file, err1 := os.Open(path.Join(tempDir, f))
		if err1 != nil {
			err2 := ctx.Reply("process failed")
			return errors.Join(err1, err2)
		}

		_, err1 = io.Copy(w, file)
		_ = file.Close()
		if err1 != nil {
			err2 := ctx.Reply("process failed")
			return errors.Join(err1, err2)
		}
	}
	fileInfo, _ := packFile.Stat()
	_ = compress.Close()
	_ = packFile.Close()

	cpFile := tb.FromDisk(packFile.Name())
	setName := replaceIllegalFilenameCharsWithString(stickerSet.Name, "_")
	setTitle := replaceIllegalFilenameCharsWithString(stickerSet.Title, "_")
	respMsg, err := ctx.Bot().Send(ctx.Recipient(), &tb.Document{
		FileName: fmt.Sprintf("%s-%s%s", setName, setTitle, ".zip"),
		File:     cpFile,
	}, &tb.SendOptions{ReplyTo: ctx.Message(), AllowWithoutReply: true})
	if errors.Is(err, tb.ErrTooLarge) {
		if fileInfo != nil {
			return ctx.Reply(fmt.Sprintf("太...太大了...有%.2fMB辣么大", float64(fileInfo.Size())/1024/1024))
		}
		return ctx.Reply("太大了，反正就是大")
	}
	if doc := respMsg.Document; doc != nil {
		fileCache := &orm.FileCache{
			FileId:   doc.FileID,
			Filename: doc.FileName,
		}

		keys, err := getFileCacheKeys(setName, opt)
		if err != nil {
			log.Error("failed to get file cache keys", zap.Error(err))
			return err
		}

		err = orm.SetFileCache(keys, fileCache, time.Hour*24*7)
		if err != nil {
			log.Error("failed to set file cache", zap.Error(err))
			return err
		}
	} else {
		log.Info("cannot get file of sent document", zap.Any("respMsg", respMsg))
	}
	return err
}

func getFileCacheKeys(setName string, opt *stickerOpts) ([]string, error) {
	keys := make([]string, 0, 2)

	// key 1: sticker id
	keys = append(keys, setName)

	// key 2: hash of opt json string
	hash := xxhash.New()
	optStr, err := json.MarshalIndent(opt, "", "")
	if err != nil {
		log.Error("failed to marshal opt", zap.Error(err))
		return nil, err
	}
	_, err = hash.Write(optStr)
	if err != nil {
		log.Error("failed to write to hasher", zap.Error(err))
		return nil, err
	}
	keys = append(keys, fmt.Sprintf("%x", hash.Sum64()))
	return keys, nil
}

func parseOpts(text string) (map[string]string, error) {
	cmd, _, err := entities.CommandFromText(text, -1)
	if err != nil {
		return nil, err
	}

	ret := make(map[string]string, 4)
	for _, arg := range cmd.Args() {
		k, v := util.ParseKeyValueMapStr(arg)

		ok, k, v := normalizeParams(k, v)
		if !ok {
			continue
		}

		switch strings.ToLower(k) {
		case "format", "f":
			f := strings.ToLower(v)
			// check available formats
			if slices.Contains([]string{"", "webp", "jpg", "jpeg", "png", "apng", "mp4", "gif", "webm"}, f) {
				ret[k] = v
			}
		case "pack", "p":
			if v == "false" {
				ret[k] = "false"
			} else {
				ret[k] = "true"
			}
		case "vf", "videoformat":
			f := strings.ToLower(v)
			if slices.Contains([]string{"", "mp4", "gif", "webm", "webp", "png", "apng"}, f) {
				ret[k] = v
			}
		case "sf", "stickerformat":
			f := strings.ToLower(v)
			if slices.Contains([]string{"", "webp", "jpg", "jpeg", "png", "gif"}, f) {
				ret[k] = v
			}
		}
	}
	return ret, nil
}

func normalizeParams(k, v string) (bool, string, string) {
	k = strings.ToLower(k)
	switch k {
	case "format", "f":
		k = "format"
	case "!pack", "!p", "nopack", "pack!", "p!":
		v = "false"
		fallthrough
	case "pack", "p":
		k = "pack"
		if strings.ToLower(v) == "false" {
			v = "false"
		} else {
			v = "true"
		}
	case "vf", "videoformat":
		k = "videoformat"
	case "sf", "stickerformat":
		k = "stickerformat"
	case "pf", "packformat":
		k = "packformat"
	case "nocache":
		k = "nocache"
		if strings.ToLower(v) == "false" {
			v = "false"
		} else {
			v = "true"
		}
	default:
		return false, k, v
	}

	return true, k, v
}

// SetStickerConfig is command for set sticker config
func SetStickerConfig(ctx tb.Context) error {
	cmd, _, err := entities.CommandFromText(ctx.Message().Text, -1)
	if err != nil {
		_ = ctx.Reply("failed to parse params")
		return err
	}

	userID := ctx.Sender().ID
	m := make(map[string]string)
	clearConf := false
	for _, arg := range cmd.Args() {
		k, v := util.ParseKeyValueMapStr(arg)
		switch k {
		case "~clear":
			clearConf = true
			clear(m)
		case "~show":
			c, err := orm.GetIWantConfig(userID)
			if err != nil {
				_ = ctx.Reply("failed to get iwant config")
				return err
			}

			cs, err := yaml.Marshal(c)
			if err != nil {
				_ = ctx.Reply("failed to marshal iwant config")
				return err
			}
			return ctx.Reply(
				fmt.Sprintf("iwant config: ```\n%s```",
					util.EscapeTelegramReservedChars(string(cs))),
				&tb.SendOptions{ParseMode: tb.ModeMarkdownV2})
		}

		ok, k, v := normalizeParams(k, v)
		if ok {
			m[k] = v
		}
	}

	msg := ""
	if clearConf {
		err = orm.ClearIWantConfig(userID)
		if err != nil {
			_ = ctx.Reply("failed to clear iwant config")
			return err
		}
		msg = "config cleared, "
	}

	if len(m) == 0 {
		return ctx.Reply(msg + "no params applied")
	}

	err = orm.SetIWantConfig(userID, m)
	if err != nil {
		_ = ctx.Reply(msg + "failed to set iwant config")
		return err
	}

	ss := make([]string, 0, len(m))
	for k, v := range m {
		ss = append(ss, fmt.Sprintf("%s=%s", k, v))
	}
	return ctx.Reply(msg + "iwant config set: " + strings.Join(ss, " "))
}
