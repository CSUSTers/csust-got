//go:build linux || darwin || freebsd || openbsd || netbsd || dragonfly

package session

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func tryLock(f *os.File) error { return unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB) }
func unlock(f *os.File) error  { return unix.Flock(int(f.Fd()), unix.LOCK_UN) }
func lockBusy(err error) bool  { return errors.Is(err, unix.EWOULDBLOCK) || errors.Is(err, unix.EAGAIN) }

func syncDirectory(root *os.Root) (err error) {
	f, err := root.Open(".")
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	return f.Sync()
}
