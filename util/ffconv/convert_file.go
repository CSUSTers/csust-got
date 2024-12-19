package ffconv

import (
	"context"
	"csust-got/log"
	"io"
	"os"
	"path"
	"strings"

	ff "github.com/u2takey/ffmpeg-go"
	"go.uber.org/zap"
)

// ConvertPipe2File read media file from reader `r` and convert it to file with default/provided options
// return the converted data readcloser and a channel of run error, and delete the temp work dir when readcloser closed
func (c *FFConv) ConvertPipe2File(ctx context.Context, r io.Reader, inputFileType string, input *ff.Stream,
	outputFilename string, outputArgs ...ff.KwArgs) (io.ReadCloser, <-chan error) {
	if input == nil {
		input = GetPipeInputStream(inputFileType)
		if ctx != nil {
			input.Context = ctx
		}
	}

	stderr := io.Discard
	var stderrCloser io.Closer
	if c.DebugFile != "" {
		f, err := os.OpenFile(c.DebugFile, os.O_APPEND|os.O_CREATE, 0644)
		if err == nil {
			stderr = f
			stderrCloser = f
		}
	}

	resultCh := make(chan error, 1)

	outputFile := outputFilename
	var workdir string
	if (!path.IsAbs(outputFilename)) && (!strings.HasPrefix(outputFilename, ".") && path.Dir(outputFilename) == ".") {
		var err error
		workdir, err = os.MkdirTemp(c.TempDir, "ffconv-")
		if err != nil {
			log.Error("ffconv: failed to create temp dir", zap.Error(err))
			resultCh <- err
			return nil, resultCh
		}
		outputFile = path.Join(workdir, path.Base(outputFilename))
	}

	runner := input.Output(outputFile, outputArgs...).Silent(true).WithInput(r).WithErrorOutput(stderr)
	if c.LogCmd {
		cmd := runner.Compile()
		log.Info("ffmpeg command", zap.String("cmd", cmd.Path), zap.Strings("args", cmd.Args))
	}

	go func() {

		if stderrCloser != nil {
			defer func() {
				_ = stderrCloser.Close()
			}()
		}
		err := runner.Run()
		resultCh <- err
	}()

	return &OutputFileReaderImpl{
		TempWorkDir: workdir,
		File:        outputFile,
	}, resultCh
}

// GetPipeInputStream get pipe input stream
func GetPipeInputStream(fileType string) *ff.Stream {
	inputArgs := ff.KwArgs{}
	if fileType != "" {
		inputArgs["f"] = fileType
	}
	input := ff.Input("pipe:", inputArgs)
	return input
}
