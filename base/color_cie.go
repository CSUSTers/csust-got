package base

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"io"
	"slices"

	"github.com/gabriel-vasile/mimetype"
	"go.uber.org/zap"
	"golang.org/x/image/webp"
	. "gopkg.in/telebot.v3"

	"csust-got/log"
	"csust-got/util"
)

var availableImageMimeTypes = []string{
	"image/jpeg",
	"image/png",
	"image/webp",
}

func getImgFileFromMsg(msg *Message) (*File, error) {
	if msg.Photo != nil {
		return &msg.Photo.File, nil
	} else if msg.Sticker != nil {
		return &msg.Sticker.File, nil
	} else if msg.Document != nil {
		return &msg.Document.File, nil
	} else {
		return nil, errors.New("no img/file in message")
	}
}

func checkMIMEType(mime *mimetype.MIME) bool {
	if slices.Contains(availableImageMimeTypes, mime.String()) {
		return true
	}
	return false
}

func GenColorCIE(ctx Context) error {
	msg := ctx.Message()
	if msg.ReplyTo != nil {
		msg = msg.ReplyTo
	}

	if msg.Photo == nil && msg.Sticker == nil && msg.Document == nil {
		return ctx.Reply("请发送一张图片或回复一张图片")
	}

	replyMsg := util.SendReply(ctx.Recipient(), "正在处理中，请稍后……", ctx.Message())
	go func() {
		cancelCtx, cancel := context.WithCancel(context.Background())
		errMsg := ""
		defer func() {
			if errMsg != "" {
				errMsg = "处理失败: " + errMsg
				util.EditMessage(replyMsg, errMsg)
			} else if context.Canceled == cancelCtx.Err() {
				util.EditMessage(replyMsg, "已取消")
			}
			cancel()
		}()

		img, err := getImgFileFromMsg(msg)
		if err != nil {
			log.Error("get image file error", zap.Error(err))
			errMsg = "get image file error"
			return
		}

		// File size limit up to 5MB
		if img.FileSize > 5*(2<<20) {
			log.Error("file size too large", zap.Int64("file_size", img.FileSize))
			errMsg = "file size too large"
			return
		}

		wtr, err := util.GetFile(img)
		if err != nil {
			log.Error("get image file error", zap.Error(err))
			errMsg = "get image file error"
			return
		}
		defer wtr.Close()
		bs, err := io.ReadAll(wtr)
		if err != nil {
			log.Error("read image file error", zap.Error(err))
			errMsg = "read image file error"
			return
		}
		mime := mimetype.Detect(bs)
		if !checkMIMEType(mime) {
			log.Error("invalid file type", zap.String("mime_type", mime.String()))
			errMsg = "invalid file type"
			return
		}

		var decodeImg image.Image
		switch mime.String() {
		case "image/jpeg":
			decodeImg, err = jpeg.Decode(bytes.NewReader(bs))
		case "image/png":
			decodeImg, err = png.Decode(bytes.NewReader(bs))
		case "image/webp":
			decodeImg, err = webp.Decode(bytes.NewReader(bs))
		default:
			err = fmt.Errorf("unsupported mime type: %s", mime.String())
		}
		if err != nil {
			log.Error("decode image error", zap.Error(err))
			errMsg = "decode image error"
			return
		}

		// TODO
	}()
}
