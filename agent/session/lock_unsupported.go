//go:build !windows && !linux && !darwin && !freebsd && !openbsd && !netbsd && !dragonfly

package session

import (
	"errors"
	"os"
)

var (
	errKernelLocksUnsupported = errors.New("session storage requires supported kernel file locks")
	errFilesystemUnsupported  = errors.New("unsupported session filesystem platform")
)

func tryLock(*os.File) error {
	return errKernelLocksUnsupported
}
func unlock(*os.File) error        { return nil }
func lockBusy(error) bool          { return false }
func syncDirectory(*os.Root) error { return errFilesystemUnsupported }
