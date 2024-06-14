package base

import (
	"bytes"
	"errors"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"slices"
	"strings"

	"go.uber.org/zap"
	tb "gopkg.in/telebot.v3"

	"csust-got/entities"
	"csust-got/log"
	"csust-got/util"
)

type stickerOpts struct {
	format string
	pack   bool
}

func defaultOpts() stickerOpts {
	return stickerOpts{
		format: "",
		pack:   false,
	}
}

// GetSticker will download sticker file, and convert to expected format, and send to chat
func GetSticker(ctx tb.Context) error {
	var msg = ctx.Message()
	var sticker *tb.Sticker

	if ctx.Chat().Private && msg.Sticker != nil {
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

	opt := defaultOpts()
	if msg.Text != "" {
		o, err := parseOpts(msg.Text)
		if err != nil {
			log.Error("parse command error", zap.String("text", msg.Text), zap.Error(err))
			err := ctx.Reply("failed to parse command args")
			if err != nil {
				return err
			}
			return err
		}
		opt = o
	}

	if !opt.pack {
		file := &sticker.File
		filename := sticker.SetName
		emoji := sticker.Emoji

		// send origin file with `format=[webp]`
		switch opt.format {
		case "", "webp":
			sendFile := &tb.Document{
				File:     *file,
				FileName: filename + ".webp",
				Caption:  emoji,
				DisableTypeDetection: true,
			}
			return ctx.Reply(sendFile)
		}

		// convert image format to params targeted
		reader, err := ctx.Bot().File(file)
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

		img, _, err := image.Decode(reader)
		if err != nil {
			err1 := ctx.Reply("failed to convert image format")
			return errors.Join(err, err1)
		}

		bs := bytes.NewBuffer(nil)
		switch opt.format {
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
		default:
			// nolint: deadcode
			return ctx.Reply("unknown image format")
		}

		sendFile := &tb.Document{
			File:     tb.FromReader(bs),
			FileName: filename,
			Caption:  emoji,
			DisableTypeDetection: true,
		}

		return ctx.Reply(sendFile)
	}

	// nolint: goerr113
	return errors.New("not implement")

}

func parseOpts(text string) (stickerOpts, error) {
	opts := defaultOpts()

	cmd, _, err := entities.CommandFromText(text, -1)
	if err != nil {
		return opts, err
	}

	for _, arg := range cmd.Args() {
		k, v := util.ParseKeyValueMapStr(arg)
		switch strings.ToLower(k) {
		case "format", "f":
			f := strings.ToLower(v)
			if slices.Contains([]string{"", "webp", "jpg", "jpeg", "png"}, f) {
				opts.format = f
			}
		case "pack", "p":
			if slices.Contains([]string{"", "true", "1"}, strings.ToLower(v)) {
				opts.pack = true
			} else if strings.ToLower(v) == "false" {
				opts.pack = false
			}
		}
	}
	return opts, nil
}
