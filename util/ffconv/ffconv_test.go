package ffconv

import (
	"bytes"
	"context"
	"io"
	"testing"

	"github.com/stretchr/testify/require"
)

type trackingCloser struct {
	io.Reader
	closed bool
}

func (c *trackingCloser) Close() error {
	c.closed = true
	return nil
}

func TestReadBufferCloseClosesUnderlyingCloser(t *testing.T) {
	tc := &trackingCloser{Reader: bytes.NewBufferString("hello")}
	buf := NewReadBuffer(tc, 64)
	require.NoError(t, buf.Close())
	require.True(t, tc.closed)
}

func TestConvertPipe2FileRespectsCanceledContext(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	cancel()

	ff := FFConv{}
	r, errCh := ff.ConvertPipe2File(ctx, bytes.NewReader(nil), NewPipeInputStream("webm"), "out.mp4")
	if r != nil {
		t.Cleanup(func() { _ = r.Close() })
	}

	err := <-errCh
	require.ErrorIs(t, err, context.Canceled)
}
