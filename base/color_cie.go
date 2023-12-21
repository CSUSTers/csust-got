package base

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"io"
	"slices"
	"time"

	"github.com/gabriel-vasile/mimetype"
	"go.uber.org/zap"
	"golang.org/x/image/webp"
	"gonum.org/v1/plot"
	"gonum.org/v1/plot/plotter"
	"gonum.org/v1/plot/vg"
	"gonum.org/v1/plot/vg/draw"
	"gonum.org/v1/plot/vg/vgimg"
	. "gopkg.in/telebot.v3"

	"csust-got/log"
	"csust-got/util"
)

var availableImageMimeTypes = []string{
	"image/jpeg",
	"image/png",
	"image/webp",
}

var errNoImgFile = errors.New("no img/file in message")
var errUnsupportedImgType = errors.New("unsupported image type")

func getImgFileFromMsg(msg *Message) (*File, error) {
	switch {
	case msg.Photo != nil:
		return &msg.Photo.File, nil
	case msg.Sticker != nil:
		return &msg.Sticker.File, nil
	case msg.Document != nil:
		return &msg.Document.File, nil
	default:
		return nil, errNoImgFile
	}
}

func checkMIMEType(mime *mimetype.MIME) bool {
	return slices.Contains(availableImageMimeTypes, mime.String())
}

// GenColorCIE generate CIE diagram from image reply by message
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
			} else if errors.Is(cancelCtx.Err(), context.Canceled) {
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
		defer func() { _ = wtr.Close() }()
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
			err = fmt.Errorf("%w: %s", errUnsupportedImgType, mime.String())
		}
		if err != nil {
			log.Error("decode image error", zap.Error(err))
			errMsg = "decode image error"
			return
		}

		out, err := plotCIEDiagram(decodeImg)
		if err != nil {
			log.Error("plot CIE diagram error", zap.Error(err))
			errMsg = "plot CIE diagram error"
			return
		}

		_, err = util.EditMessageWithError(replyMsg, Photo{File: FromReader(bytes.NewReader(out))})
		if err != nil {
			log.Error("send image error", zap.Error(err))
			errMsg = "send image error"
			return
		}
	}()

	return nil
}

func plotCIEDiagram(img image.Image) ([]byte, error) {
	startTime := time.Now()

	bound := img.Bounds()
	width := bound.Dx()
	height := bound.Dy()
	pixels := make([]color.Color, width*height)
	for i := 0; i < width; i++ {
		for j := 0; j < height; j++ {
			pix := img.At(i, j)
			pixels[i*width+j] = pix
		}
	}

	diag := plot.New()
	diag.Title.Text = "CIE 1931 Chromaticity Diagram"
	diag.X.Label.Text = "CIE x"
	diag.Y.Label.Text = "CIE y"
	diag.X.Min = 0
	diag.X.Max = 1
	diag.Y.Min = 0
	diag.Y.Max = 1

	const imgW = 500
	const imgH = 500
	cv := image.NewRGBA(image.Rect(0, 0, imgW, imgH))
	for _, pix := range pixels {
		rr, gg, bb, aa := pix.RGBA()
		if aa == 0 {
			continue
		}
		r := float32(rr * 0xffff / aa)
		g := float32(gg * 0xffff / aa)
		b := float32(bb * 0xffff / aa)

		x := r / (r + g + b)
		y := g / (r + g + b)

		cv.Set(int(x*imgW), int(y*imgH), pix)
	}

	diag.Add(plotter.NewImage(cv, 0, 0, 1, 1))

	cvImg := vgimg.New(vg.Points(float64(imgW)), vg.Points(float64(imgH)))
	dc := draw.New(cvImg)
	dc = draw.Crop(dc, 0, -vg.Centimeter, 0, 0)
	diag.Draw(dc)

	output := bytes.NewBuffer([]byte{})
	if _, err := (vgimg.PngCanvas{Canvas: cvImg}).WriteTo(output); err != nil {
		log.Error("write image error", zap.Error(err))
		return nil, err
	}

	processTime := time.Since(startTime)
	log.Info("CIE process time", zap.Duration("process-time", processTime), zap.Int("width", width), zap.Int("height", height))
	return output.Bytes(), nil
}
