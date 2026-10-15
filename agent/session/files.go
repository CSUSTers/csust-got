package session

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// FileStore confines archives and kernel locks to one stable directory root.
type FileStore struct {
	root   *os.Root
	onLock func(Scope)
}

// NewFileStore opens the archive root and probes locking, sync, and hard-link support.
func NewFileStore(directory string) (*FileStore, error) {
	abs, err := filepath.Abs(directory)
	if err != nil {
		return nil, err
	}
	if err = os.MkdirAll(abs, 0700); err != nil {
		return nil, err
	}
	r, err := os.OpenRoot(abs)
	if err != nil {
		return nil, err
	}
	s := &FileStore{root: r}
	if err = s.probe(); err != nil {
		return nil, errors.Join(err, r.Close())
	}
	return s, nil
}

func (s *FileStore) probe() (err error) {
	id, err := NewID()
	if err != nil {
		return err
	}
	name := ".probe-" + id
	f, err := s.root.OpenFile(name, os.O_CREATE|os.O_EXCL|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, s.root.Remove(name)) }()
	defer func() { err = errors.Join(err, f.Close()) }()
	if err = tryLock(f); err != nil {
		return err
	}
	defer func() { err = errors.Join(err, unlock(f)) }()
	if _, err = f.Write([]byte("session-storage-probe")); err != nil {
		return err
	}
	if err = f.Sync(); err != nil {
		return err
	}
	if err = s.root.Link(name, name+".final"); err != nil {
		return err
	}
	if err = s.root.Remove(name + ".final"); err != nil {
		return err
	}
	return syncDirectory(s.root)
}

// Close releases the stable root handle without deleting archives.
func (s *FileStore) Close() error { return s.root.Close() }

func scopePath(scope Scope) (string, error) {
	if err := scope.Validate(); err != nil {
		return "", err
	}
	b, _ := json.Marshal([]string{scope.Bot, scope.Platform})
	h := sha256.Sum256(b)
	return filepath.Join(scope.Namespace, hex.EncodeToString(h[:]), strconv.FormatInt(scope.ChatID, 10)), nil
}

func safeDirectory(root *os.Root, path string, create bool) (*os.Root, error) {
	parts := strings.Split(path, string(filepath.Separator))
	var current string
	for _, part := range parts {
		if part == "" || part == "." || part == ".." {
			return nil, ErrCorrupt
		}
		current = filepath.Join(current, part)
		if create {
			mkdirErr := root.Mkdir(current, 0700)
			if mkdirErr != nil && !errors.Is(mkdirErr, fs.ErrExist) {
				return nil, mkdirErr
			}
			if mkdirErr == nil {
				parent, err := root.OpenRoot(filepath.Dir(current))
				if err != nil {
					return nil, err
				}
				err = errors.Join(syncDirectory(parent), parent.Close())
				if err != nil {
					return nil, err
				}
			}
		}
		info, err := root.Lstat(current)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, ErrCorrupt
		}
	}
	return root.OpenRoot(path)
}

func regular(root *os.Root, name string) error {
	i, err := root.Lstat(name)
	if err != nil {
		return err
	}
	if !i.Mode().IsRegular() {
		return ErrCorrupt
	}
	return nil
}

// WithScopeLock serializes scope storage operations; its stable lock file is never removed.
func (s *FileStore) WithScopeLock(ctx context.Context, scope Scope, fn func(*ScopeFiles) error) (err error) {
	path, err := scopePath(scope)
	if err != nil {
		return err
	}
	r, err := safeDirectory(s.root, path, true)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	const name = ".scope.lock"
	if err = regular(r, name); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	f, err := r.OpenFile(name, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, f.Close()) }()
	for {
		if err = ctx.Err(); err != nil {
			return err
		}
		err = tryLock(f)
		if err == nil {
			break
		}
		if !lockBusy(err) {
			return err
		}
		t := time.NewTimer(10 * time.Millisecond)
		select {
		case <-ctx.Done():
			t.Stop()
			return ctx.Err()
		case <-t.C:
		}
	}
	defer func() { err = errors.Join(err, unlock(f)) }()
	if s.onLock != nil {
		s.onLock(scope)
	}
	if err = ctx.Err(); err != nil {
		return err
	}
	return fn(&ScopeFiles{root: r, scope: scope})
}

// ScopeFiles exposes confined archive operations during a scope lock callback.
type ScopeFiles struct {
	root  *os.Root
	scope Scope
}

func validateNode(n Node, scope Scope) error {
	if n.Scope != scope || n.Version != Version || n.Ref.Validate() != nil || n.FileName != n.Ref.NodeID+".jsonl" || n.Agent == "" || !ValidID(n.RunID) {
		return ErrCorrupt
	}
	if n.Parent != nil && (n.Parent.Validate() != nil || n.Parent.DAGID != n.Ref.DAGID || *n.Parent == n.Ref) {
		return ErrCorrupt
	}
	return nil
}

type limitedWriter struct {
	writer io.Writer
	size   int64
}

func (w *limitedWriter) Write(b []byte) (int, error) {
	if int64(len(b)) > maxArchiveBytes-w.size {
		return 0, fmt.Errorf("%w: maximum %d bytes", errArchiveLimit, maxArchiveBytes)
	}
	n, err := w.writer.Write(b)
	w.size += int64(n)
	return n, err
}

// WriteAtomic publishes a synced archive by hard-link without replacing existing files.
func (f *ScopeFiles) WriteAtomic(n Node, capture TurnCapture) (digest string, size int64, err error) {
	if err := validateNode(n, f.scope); err != nil {
		return "", 0, err
	}
	r, err := safeDirectory(f.root, n.Ref.DAGID, true)
	if err != nil {
		return "", 0, err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	tmp := n.Ref.NodeID + ".tmp"
	w, err := r.OpenFile(tmp, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return "", 0, err
	}
	h := sha256.New()
	out := &limitedWriter{writer: io.MultiWriter(w, h)}
	err = encodeArchive(out, n, capture)
	if err == nil {
		err = w.Sync()
	}
	err = errors.Join(err, w.Close())
	if err != nil {
		return "", 0, err
	}
	if err = r.Link(tmp, n.FileName); err != nil {
		return "", 0, err
	}
	if err = syncDirectory(r); err != nil {
		return "", 0, err
	}
	if err = r.Remove(tmp); err != nil {
		return "", 0, err
	}
	if err = syncDirectory(r); err != nil {
		return "", 0, err
	}
	if err = syncDirectory(f.root); err != nil {
		return "", 0, err
	}
	return hex.EncodeToString(h.Sum(nil)), out.size, nil
}

// Read verifies the archive envelope, complete tool chain, size, and digest.
func (f *ScopeFiles) Read(n Node) (capture TurnCapture, err error) {
	if err := validateNode(n, f.scope); err != nil {
		return TurnCapture{}, err
	}
	r, err := safeDirectory(f.root, n.Ref.DAGID, false)
	if err != nil {
		return TurnCapture{}, err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	if err = regular(r, n.FileName); err != nil {
		return TurnCapture{}, err
	}
	file, err := r.Open(n.FileName)
	if err != nil {
		return TurnCapture{}, err
	}
	defer func() { err = errors.Join(err, file.Close()) }()
	info, err := file.Stat()
	if err != nil {
		return TurnCapture{}, err
	}
	if info.Size() != n.Size {
		return TurnCapture{}, ErrCorrupt
	}
	return decodeArchive(file, n)
}

// Remove deletes only this node's known files, retaining unknown or corrupt final archives.
func (f *ScopeFiles) Remove(n Node) (err error) {
	if err := validateNode(n, f.scope); err != nil {
		return err
	}
	r, err := safeDirectory(f.root, n.Ref.DAGID, false)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, r.Close()) }()
	// A final archive is not merely an arbitrary regular file at a plausible name.
	// Pending intents have no digest yet: validate their envelope and complete JSONL
	// against the durable intent before removing. Unknown/corrupt final files survive.
	if err = regular(r, n.FileName); err == nil {
		file, openErr := r.Open(n.FileName)
		if openErr != nil {
			return openErr
		}
		info, statErr := file.Stat()
		if statErr != nil {
			return errors.Join(statErr, file.Close())
		}
		expected := n
		if expected.Digest == "" {
			h := sha256.New()
			size, copyErr := io.Copy(h, io.LimitReader(file, maxArchiveBytes+1))
			if copyErr != nil || size > maxArchiveBytes {
				return errors.Join(ErrCorrupt, copyErr, file.Close())
			}
			expected.Digest, expected.Size = hex.EncodeToString(h.Sum(nil)), size
			if _, err = file.Seek(0, io.SeekStart); err != nil {
				return errors.Join(err, file.Close())
			}
		}
		if info.Size() != expected.Size {
			return errors.Join(ErrCorrupt, file.Close())
		}
		_, decodeErr := decodeArchive(file, expected)
		closeErr := file.Close()
		if err = errors.Join(decodeErr, closeErr); err != nil {
			return err
		}
	} else if !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	for _, name := range []string{n.Ref.NodeID + ".tmp", n.FileName} {
		if err = regular(r, name); errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return err
		}
		if err = r.Remove(name); err != nil && !errors.Is(err, fs.ErrNotExist) {
			return err
		}
	}
	return syncDirectory(r)
}

// RemoveEmptyDAG removes only an empty DAG directory, retaining and reporting unknown entries.
func (f *ScopeFiles) RemoveEmptyDAG(id string) error {
	if !ValidID(id) {
		return ErrCorrupt
	}
	if err := f.root.Remove(id); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return syncDirectory(f.root)
}
