package ffconv

import (
	"bufio"
	"errors"
	"io"
	"io/fs"
	"os"
)

// FFConv is utilities for convert media type
type FFConv struct {
	LogCmd    bool
	DebugFile string
	TempDir   string
}

// OutputFileReaderImpl is a [io.ReadCloser], when close it will remove temp work dir
type OutputFileReaderImpl struct {
	TempWorkDir string
	File        string
	file        *os.File
}

func (o *OutputFileReaderImpl) Read(p []byte) (n int, err error) {
	if o.file == nil {
		o.file, err = os.Open(o.File)
		if err != nil {
			return 0, err
		}
	}

	return o.file.Read(p)
}

// Close close the file, and remove temp work dir
func (o *OutputFileReaderImpl) Close() error {
	var err1, err2 error
	if o.file != nil {
		err1 = o.file.Close()
		if errors.Is(err1, fs.ErrClosed) {
			err1 = nil
		}
	}
	if o.TempWorkDir != "" {
		err2 = os.RemoveAll(o.TempWorkDir)
	}
	return errors.Join(err1, err2)
}

// ReadBuffer wrap an [io.Reader] with buffer
type ReadBuffer struct {
	io.Reader
}

// NewReadBuffer create a new [ReadBuffer]
func NewReadBuffer(r io.Reader, bufSize int) *ReadBuffer {
	return &ReadBuffer{
		Reader: bufio.NewReaderSize(r, bufSize),
	}
}

// Close close the reader
func (o *ReadBuffer) Close() error {
	if c, ok := o.Reader.(io.Closer); ok {
		return c.Close()
	}
	return nil
}
