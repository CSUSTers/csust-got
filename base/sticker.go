// nolint: goconst
package base

import (
	"archive/zip"
	"bytes"
	"errors"
	"fmt"
	"image"
	"image/gif"
	"image/jpeg"
	"image/png"
	"io"
	"os"
	"path"
	"slices"
	"strings"
	"time"

	ffmpeg_go "github.com/u2takey/ffmpeg-go"
	//nolint: revive
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

type stickerOpts struct {
	format string
	pack   bool
	vf     string
	sf     string

	// pack format
	pf string
}

func defaultOpts() stickerOpts {
	return stickerOpts{
		format: "",
		pack:   false,
	}
}

func defaultOptsWithConfig(m map[string]string) stickerOpts {
	o := defaultOpts()
	o = o.merge(m, false)
	return o
}

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
				o.vf = ""
			}

			if _, ok = sets["stickerformat"]; !ok {
				o.sf = ""
			}
		}
	}

	for k, v := range m {
		k = strings.ToLower(k)
		switch k {
		case "format", "f":
			o.format = v
		case "pack", "p":
			if slices.Contains([]string{"", "true", "1"}, strings.ToLower(v)) {
				o.pack = true
			} else if strings.ToLower(v) == "false" {
				o.pack = false
			}
		case "vf", "videoformat":
			o.vf = v
		case "sf", "stickerformat":
			o.sf = v
		case "pf", "packformat":
			o.pf = v
		}
	}

	return o
}

func (o stickerOpts) VideoFormat() string {
	if o.vf != "" {
		return o.vf
	}
	return o.format
}

func (o stickerOpts) StickerFormat() string {
	if o.sf != "" {
		return o.sf
	}
	return o.format
}

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

	if !opt.pack {
		filename := sticker.SetName
		emoji := sticker.Emoji
		if sticker.CustomEmoji != "" {
			emoji += " " + sticker.CustomEmoji
		}

		// send video is sticker is video
		if sticker.Video {
			return sendVideoSticker(ctx, sticker, filename, emoji, opt)
		}

		return sendImageSticker(ctx, sticker, filename, emoji, opt)
	}
	return sendStickerPack(ctx, sticker, opt)
}

func sendImageSticker(ctx tb.Context, sticker *tb.Sticker, filename string, emoji string, opt stickerOpts) error {
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

func sendVideoSticker(ctx tb.Context, sticker *tb.Sticker, filename string, emoji string, opt stickerOpts) error {
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

	switch f {
	case "webm":
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
	case "mp4":
		ff := ffconv.FFConv{LogCmd: true}
		r, errCh := ff.ConvertPipe2File(reader, "webm", nil, filename+".mp4")
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
			log.Error("wait ffmpeg exec result timeout", zap.String("filename", filename), zap.String("convert_format", f))
			return ctx.Reply("convert to mp4 failed")
		}
		sendFile := &tb.Document{
			File:                 tb.FromReader(r),
			FileName:             filename + ".mp4",
			Caption:              emoji,
			DisableTypeDetection: true,
		}
		return ctx.Reply(sendFile)
	default:
		return ctx.Reply(fmt.Sprintf("not implement `%s` format for video sticker yet", f))
	}
}

func sendStickerPack(ctx tb.Context, sticker *tb.Sticker, opt stickerOpts) error {
	stickerSet, err := ctx.Bot().StickerSet(sticker.SetName)
	if err != nil {
		err2 := ctx.Reply("failed to get sticker set")
		return errors.Join(err, err2)
	}

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

	// TODO: use go routine

	for i := range stickerSet.Stickers {
		s := &stickerSet.Stickers[i]

		_ = ctx.Notify(tb.ChoosingSticker)

		emoji := s.Emoji
		if emoji != "" {
			emoji = "_" + emoji
		}
		filename := fmt.Sprintf("%s_%03d%s%s", stickerSet.Name, i+1, emoji, opt.FileExt(s.Video))
		outputFiles = append(outputFiles, filename)

		// TODO reduce complexity by move some code to function
		// nolint: nestif,gocritic
		if opt.NotConvert(s) {
			err := func() error {
				of, err := os.OpenFile(path.Join(tempDir, filename), os.O_CREATE|os.O_RDWR, 0o640)
				if err != nil {
					return err
				}
				defer func() { _ = of.Close() }()

				err = func() error {
					fileR, err := ctx.Bot().File(&s.File)
					if err != nil {
						return err
					}

					_, err = io.Copy(of, fileR)
					return err
				}()
				return err
			}()
			if err != nil {
				err2 := ctx.Reply("process failed")
				return errors.Join(err, err2)
			}
		} else if s.Video {
			f := opt.VideoFormat()

			ff := ffconv.FFConv{LogCmd: true}
			var outputArgs []ffmpeg_go.KwArgs

			fileR, err := ctx.Bot().File(&s.File)
			if err != nil {
				err2 := ctx.Reply("failed to get sticker file")
				return errors.Join(err, err2)
			}

			input := ffconv.GetPipeInputStream("webm")
			err = func() error {
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
				}
				r, errCh := ff.ConvertPipe2File(fileR, "", input, path.Join(tempDir, filename), outputArgs...)
				defer func() {
					_ = r.Close()
				}()
				select {
				case err := <-errCh:
					if err != nil {
						log.Error("failed to convert", zap.Error(err))
						err1 := ctx.Reply("convert video sticker failed")
						return errors.Join(err, err1)
					}
					return nil
				case <-time.After(time.Second * 30):
					log.Error("wait ffmpeg exec result timeout", zap.String("filename", filename), zap.String("convert_format", f))
					err2 := ctx.Reply("convert video sticker failed")
					// TODO use static error
					// nolint: err113
					return errors.Join(errors.New("wait ffmpeg exec result timeout"), err2)
				}
			}()

			if err != nil {
				return err
			}
		} else {
			f := opt.StickerFormat()

			fileR, err := ctx.Bot().File(&s.File)
			if err != nil {
				err2 := ctx.Reply("failed to get sticker file")
				return errors.Join(err, err2)
			}

			err = func() error {
				defer func() {
					_ = fileR.Close()
				}()

				img, _, err1 := image.Decode(fileR)
				if err1 != nil {
					err2 := ctx.Reply("failed to decode image")
					return errors.Join(err1, err2)
				}
				const ErrConvertMsg = "failed to convert image format"

				of, err1 := os.OpenFile(path.Join(tempDir, filename), os.O_CREATE|os.O_WRONLY, 0o640)
				if err1 != nil {
					return errors.Join(err1, ctx.Reply(ErrConvertMsg))
				}
				defer func() { _ = of.Close() }()

				switch opt.StickerFormat() {
				case "jpg", "jpeg":
					err2 := jpeg.Encode(of, img, &jpeg.Options{Quality: 100})
					if err2 != nil {
						err1 := ctx.Reply(ErrConvertMsg)
						return errors.Join(err2, err1)
					}
				case "png":
					err2 := png.Encode(of, img)
					if err2 != nil {
						err1 := ctx.Reply(ErrConvertMsg)
						return errors.Join(err2, err1)
					}
				case "gif":
					err := gif.Encode(of, img, &gif.Options{NumColors: 255})
					if err != nil {
						err1 := ctx.Reply(ErrConvertMsg)
						return errors.Join(err, err1)
					}
				default:
					// TODO use static error
					// nolint: err113
					return errors.Join(errors.New("unknown target image format"),
						ctx.Reply(fmt.Sprintf("unknown target image format: %s", f)))
				}
				return nil
			}()

			if err != nil {
				return err
			}
		}
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
	_ = compress.Close()
	_ = packFile.Close()

	err = ctx.Reply(&tb.Document{
		FileName: fmt.Sprintf("%s-%s%s", stickerSet.Name, stickerSet.Title, ".zip"),
		File:     tb.FromDisk(packFile.Name()),
	})
	return err
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
			if slices.Contains([]string{"", "webp", "jpg", "jpeg", "png", "mp4", "gif", "webm"}, f) {
				ret[k] = v
			}
		case "pack", "p":
			if slices.Contains([]string{"", "true", "1"}, strings.ToLower(v)) {
				ret[k] = "true"
			} else if strings.ToLower(v) == "false" {
				ret[k] = "false"
			}
		case "vf", "videoformat":
			f := strings.ToLower(v)
			if slices.Contains([]string{"", "mp4", "gif", "webm"}, f) {
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
	case "pack", "p":
		k = "pack"
	case "vf", "videoformat":
		k = "videoformat"
	case "sf", "stickerformat":
		k = "stickerformat"
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
			return ctx.Reply(fmt.Sprintf("iwant config: ```\n%s```", string(cs)), tb.SendOptions{ParseMode: tb.ModeMarkdownV2})
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
