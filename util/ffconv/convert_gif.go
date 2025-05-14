package ffconv

import (
	"csust-got/log"
	"errors"
	"io"
	"os"

	ff "github.com/u2takey/ffmpeg-go"
	"go.uber.org/zap"
)

// Convert2GifFromReader read media file from reader `r` and convert it to gif
// return the converted data reader and a channel of run error
func (c *FFConv) Convert2GifFromReader(r io.Reader, inputFileType string) (io.Reader, <-chan error) {
	input := GetPipeInputStream(inputFileType)
	vf := GetGifPaletteVfStream(input)

	outputArgs := ff.KwArgs{
		"c:v": "gif",
		"f":   "gif",
	}
	pipeR, pipeW := io.Pipe()
	bufOut := NewReadBuffer(pipeR, 1*1024*1024)
	stderr := io.Discard
	var stderrCloser io.Closer
	if c.DebugFile != "" {
		f, err := os.OpenFile(c.DebugFile, os.O_APPEND|os.O_CREATE, 0644)
		if err == nil {
			stderr = f
			stderrCloser = f
		}
	}
	runner := vf.Output("pipe:", outputArgs).Silent(true).WithInput(r).WithOutput(pipeW, stderr)
	if c.LogCmd {
		cmd := runner.Compile()
		log.Info("ffmpeg command", zap.String("cmd", cmd.Path), zap.Strings("args", cmd.Args))
	}
	resultCh := make(chan error, 1)

	go func() {
		if stderrCloser != nil {
			defer func() {
				_ = stderrCloser.Close()
			}()
		}
		err := runner.Run()
		err1 := pipeW.Close()
		resultCh <- errors.Join(err, err1)
	}()
	return bufOut, resultCh
}

// GetGifPaletteVfStream get gif palette vfilter stream
func GetGifPaletteVfStream(input *ff.Stream) *ff.Stream {
	split := input.Split()
	ori, s1 := split.Get("ori"), split.Get("s1")
	p1 := s1.Filter("palettegen", ff.Args{}, ff.KwArgs{
		"reserve_transparent": "on",
		"transparency_color":  "ffffff",
		"stats_mode":          "full",
	})

	vf := ff.Filter([]*ff.Stream{ori, p1}, "paletteuse", ff.Args{}, ff.KwArgs{"dither": "sierra2_4a"})
	return vf
}
