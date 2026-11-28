package ffconv

import (
	"context"
	"csust-got/log"
	"io"
	"os"
	"os/exec"
	"path"
	"strings"

	ff "github.com/u2takey/ffmpeg-go"
	"go.uber.org/zap"
)

// ConvertPipe2File read media file from reader `r` and convert it to file with default/provided options
// return the converted data readcloser and a channel of run error, and delete the temp work dir when readcloser closed
func (c *FFConv) ConvertPipe2File(ctx context.Context, r io.Reader, intputStream StreamApply,
	outputFilename string, outputArgs ...ff.KwArgs) (io.ReadCloser, <-chan error) {
	input := intputStream(ff.Input("pipe:"))

	stderr := io.Discard
	var stderrCloser io.Closer
	if c.DebugFile != "" {
		f, err := os.OpenFile(c.DebugFile, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
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
	compiled := runner.Compile()
	if c.LogCmd {
		log.Info("ffmpeg command", zap.String("cmd", compiled.Path), zap.Strings("args", compiled.Args))
	}

	go func() {
		if stderrCloser != nil {
			defer func() {
				_ = stderrCloser.Close()
			}()
		}
		var args []string
		if len(compiled.Args) > 1 {
			args = compiled.Args[1:]
		}
		cmd := exec.CommandContext(ctx, compiled.Path, args...)
		cmd.Stdin = compiled.Stdin
		cmd.Stdout = compiled.Stdout
		cmd.Stderr = compiled.Stderr
		cmd.Dir = compiled.Dir
		cmd.Env = compiled.Env
		resultCh <- cmd.Run()
	}()

	return &OutputFileReaderImpl{
		TempWorkDir: workdir,
		File:        outputFile,
	}, resultCh
}
