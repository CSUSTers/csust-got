//go:build !386 && !arm

package chatv2

import (
	"context"
	"errors"
	"testing"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var (
	errStubStart = errors.New("boom")
	errStubInit  = errors.New("bad init")
)

type stubMcpLifecycleClient struct {
	calls    []string
	startErr error
	initErr  error
	req      mcp.InitializeRequest
}

func (s *stubMcpLifecycleClient) Start(_ context.Context) error {
	s.calls = append(s.calls, "start")
	return s.startErr
}

func (s *stubMcpLifecycleClient) Initialize(_ context.Context, req mcp.InitializeRequest) (*mcp.InitializeResult, error) {
	s.calls = append(s.calls, "initialize")
	s.req = req
	if s.initErr != nil {
		return nil, s.initErr
	}
	return &mcp.InitializeResult{}, nil
}

func TestInitializeMCPClient(t *testing.T) {
	t.Run("starts and initializes client", func(t *testing.T) {
		cli := &stubMcpLifecycleClient{}
		err := initializeMCPClient(t.Context(), cli)
		require.NoError(t, err)
		assert.Equal(t, []string{"start", "initialize"}, cli.calls)
		assert.Equal(t, mcp.LATEST_PROTOCOL_VERSION, cli.req.Params.ProtocolVersion)
		assert.Equal(t, "csust-got chatv2", cli.req.Params.ClientInfo.Name)
		assert.Equal(t, "dev", cli.req.Params.ClientInfo.Version)
	})

	t.Run("start failure stops initialization", func(t *testing.T) {
		cli := &stubMcpLifecycleClient{startErr: errStubStart}
		err := initializeMCPClient(t.Context(), cli)
		require.Error(t, err)
		assert.Equal(t, []string{"start"}, cli.calls)
		assert.ErrorContains(t, err, "start client")
	})

	t.Run("initialize failure is wrapped", func(t *testing.T) {
		cli := &stubMcpLifecycleClient{initErr: errStubInit}
		err := initializeMCPClient(t.Context(), cli)
		require.Error(t, err)
		assert.Equal(t, []string{"start", "initialize"}, cli.calls)
		assert.ErrorContains(t, err, "initialize client")
	})
}
