package ffconv

import (
	"csust-got/log"
	"errors"
	"io"
	"os"

	ff "github.com/u2takey/ffmpeg-go"
	"go.uber.org/zap"
)

// ConvertPipe2Pipe read media file from reader `r`
// return the converted data reader and a channel of run error
func (c *FFConv) ConvertPipe2Pipe(r io.Reader, inputStreamFunc StreamApply,
	outputArg ff.KwArgs, outputArgs ...ff.KwArgs) (io.Reader, <-chan error) {

	input := inputStreamFunc(ff.Input("pipe:"))
	outputArgs = append([]ff.KwArgs{outputArg}, outputArgs...)

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
	runner := input.Output("pipe:", outputArgs...).Silent(true).WithInput(r).WithOutput(pipeW, stderr)
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

// FormatArg returns a [ff.KwArgs] with format
func FormatArg(format string) ff.KwArgs {
	return ff.KwArgs{"f": format}
}

// VideoCodecArg returns a [ff.KwArgs] with video codec
func VideoCodecArg(codec string) ff.KwArgs {
	return ff.KwArgs{"c:v": codec}
}

// AudioCodecArg returns a [ff.KwArgs] with audio codec
func AudioCodecArg(codec string) ff.KwArgs {
	return ff.KwArgs{"c:a": codec}
}

// NoAudioArg returns a [ff.KwArgs] with no audio option
func NoAudioArg() ff.KwArgs {
	return ff.KwArgs{"an": ""}
}
