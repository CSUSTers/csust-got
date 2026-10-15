package session

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestSessionKernelLockHelper(t *testing.T) {
	name := os.Getenv("SESSION_LOCK_HELPER")
	if name == "" {
		return
	}
	f, err := os.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = tryLock(f); err != nil {
		t.Fatal(err)
	}
	fmt.Println("locked")
	_, _ = os.Stdin.Read(make([]byte, 1))
}

func TestSessionKernelLockAcrossProcesses(t *testing.T) {
	name := filepath.Join(t.TempDir(), "stable.lock")
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(executable, "-test.run=^TestSessionKernelLockHelper$")
	cmd.Env = append(os.Environ(), "SESSION_LOCK_HELPER="+name)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer stdin.Close()
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	cmd.Stderr = os.Stderr
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if cmd.ProcessState == nil {
			_ = cmd.Process.Kill()
			_ = cmd.Wait()
		}
	}()
	ready := make(chan bool, 1)
	go func() { scanner := bufio.NewScanner(stdout); ready <- scanner.Scan() && scanner.Text() == "locked" }()
	select {
	case ok := <-ready:
		if !ok {
			t.Fatal("helper failed to acquire lock")
		}
	case <-time.After(10 * time.Second):
		t.Fatal("helper lock timeout")
	}
	f, err := os.OpenFile(name, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = tryLock(f); !lockBusy(err) {
		t.Fatalf("independent process did not exclude lock: %v", err)
	}
	if err = cmd.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if err = tryLock(f); err != nil {
		t.Fatalf("kernel did not release killed process's lock: %v", err)
	}
	if err = unlock(f); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(name); err != nil {
		t.Fatalf("stable lock file disappeared: %v", err)
	}
}
