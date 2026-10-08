//go:build windows

package agentv3

import (
	"net"
	"testing"

	"github.com/stretchr/testify/require"
)

// reserveRefusingLoopbackPort is best-effort on Windows: it returns a just-released loopback port.
func reserveRefusingLoopbackPort(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	_, port, err := net.SplitHostPort(ln.Addr().String())
	require.NoError(t, err)
	require.NoError(t, ln.Close())
	return port
}
