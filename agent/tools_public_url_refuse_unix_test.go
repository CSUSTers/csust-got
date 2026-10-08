//go:build unix

package agentv3

import (
	"errors"
	"strconv"
	"syscall"
	"testing"

	"github.com/stretchr/testify/require"
)

// reserveRefusingLoopbackPort returns a loopback port that refuses every connection until the test ends.
// It holds sockets bound to the port without listening, so the kernel keeps the port reserved (nobody else can
// listen on it) while connects still fail with ECONNREFUSED. IPv6 is reserved on the same port when available.
func reserveRefusingLoopbackPort(t *testing.T) string {
	t.Helper()
	bindHeld := func(family int, sa syscall.Sockaddr) (int, error) {
		fd, err := syscall.Socket(family, syscall.SOCK_STREAM, 0)
		if err != nil {
			return -1, err
		}
		if err := syscall.Bind(fd, sa); err != nil {
			_ = syscall.Close(fd)
			return -1, err
		}
		return fd, nil
	}
	for range 50 {
		fd4, err := bindHeld(syscall.AF_INET, &syscall.SockaddrInet4{Addr: [4]byte{127, 0, 0, 1}})
		require.NoError(t, err, "bind an IPv4 loopback port")
		sa, err := syscall.Getsockname(fd4)
		require.NoError(t, err)
		port := sa.(*syscall.SockaddrInet4).Port
		fds := []int{fd4}
		fd6, err := bindHeld(syscall.AF_INET6, &syscall.SockaddrInet6{Port: port, Addr: [16]byte{15: 1}})
		if errors.Is(err, syscall.EADDRINUSE) {
			_ = syscall.Close(fd4)
			continue
		}
		if err == nil {
			fds = append(fds, fd6)
		}
		t.Cleanup(func() {
			for _, fd := range fds {
				_ = syscall.Close(fd)
			}
		})
		return strconv.Itoa(port)
	}
	t.Fatal("no loopback port could be reserved")
	return ""
}
