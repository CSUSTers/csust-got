package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
)

var (
	errSessionStorageRequired = errors.New("session repository and files are required")
	errSessionDurations       = errors.New("invalid session durations")
)

// Options controls idle qualification, lease renewal, and operation deadlines.
type Options struct {
	// TTL accepts any positive duration; zero uses 24h. Expiry has Redis millisecond precision.
	TTL              time.Duration
	LeaseDuration    time.Duration
	RenewInterval    time.Duration
	OperationTimeout time.Duration
}

// Service coordinates fenced Redis metadata and confined archive operations.
type Service struct {
	repo      Repository
	files     *FileStore
	options   Options
	ctx       context.Context
	cancel    context.CancelFunc
	mu        sync.Mutex
	closed    bool
	parents   map[*LoadedParent]struct{}
	wg        sync.WaitGroup
	closeOnce sync.Once
	closeErr  error
}

// NewService takes ownership of files, but not the repository's Redis client.
// Recover retries durable intents/deleting; Collect additionally claims expiry.
// Scheduling belongs to the caller.
func NewService(repo Repository, files *FileStore, options Options) (*Service, error) {
	if repo == nil || files == nil {
		return nil, errSessionStorageRequired
	}
	if options.TTL == 0 {
		options.TTL = 24 * time.Hour
	}
	if options.LeaseDuration == 0 {
		options.LeaseDuration = 90 * time.Second
	}
	if options.RenewInterval == 0 {
		options.RenewInterval = options.LeaseDuration / 3
	}
	if options.OperationTimeout == 0 {
		options.OperationTimeout = 10 * time.Second
	}
	if options.TTL <= 0 || options.LeaseDuration < time.Millisecond || options.RenewInterval <= 0 || options.RenewInterval >= options.LeaseDuration || options.OperationTimeout <= 0 {
		return nil, errSessionDurations
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Service{repo: repo, files: files, options: options, ctx: ctx, cancel: cancel, parents: map[*LoadedParent]struct{}{}}, nil
}

func (s *Service) scope(scope Scope) (Scope, error) {
	if scope.Namespace == "" {
		scope.Namespace = s.repo.Namespace()
	}
	if scope.Namespace != s.repo.Namespace() {
		return Scope{}, ErrCorrupt
	}
	return scope, scope.Validate()
}

func (s *Service) begin(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	s.wg.Add(1)
	s.mu.Unlock()
	op, cancel := context.WithTimeout(ctx, s.options.OperationTimeout)
	stop := context.AfterFunc(s.ctx, cancel)
	return op, func() { stop(); cancel(); s.wg.Done() }, nil
}

// Load returns complete replay and a parent proof only after all ancestors are verified.
func (s *Service) Load(ctx context.Context, selection Selection) (LoadResult, error) {
	op, done, err := s.begin(ctx)
	if err != nil {
		return LoadResult{}, err
	}
	defer done()
	selection.Scope, err = s.scope(selection.Scope)
	if err != nil {
		return LoadResult{}, err
	}
	if selection.Mode == SelectNone {
		return LoadResult{}, ErrMiss
	}
	token, err := NewID()
	if err != nil {
		return LoadResult{}, err
	}
	var pinned Pinned
	var messages []*schema.Message
	err = s.files.WithScopeLock(op, selection.Scope, func(files *ScopeFiles) error {
		var err error
		pinned, err = s.repo.ResolveAndPin(op, selection, token, s.options.LeaseDuration)
		if err != nil {
			return err
		}
		if len(pinned.Nodes) == 0 {
			return ErrCorrupt
		}
		seen := map[NodeRef]bool{}
		var archiveSize int64
		for i, n := range pinned.Nodes {
			if n.Scope != selection.Scope || n.Ref.DAGID != pinned.Lease.DAGID || seen[n.Ref] {
				return ErrCorrupt
			}
			seen[n.Ref] = true
			if n.Size <= 0 || n.Size > maxArchiveBytes-archiveSize {
				return fmt.Errorf("%w: session chain too large", ErrCorrupt)
			}
			archiveSize += n.Size
			if i == 0 && n.Parent != nil || i > 0 && (n.Parent == nil || *n.Parent != pinned.Nodes[i-1].Ref) {
				return ErrCorrupt
			}
			c, err := files.Read(n)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrCorrupt, err)
			}
			if i == 0 {
				b, err := replayRecords(c.Bootstrap)
				if err != nil {
					return err
				}
				messages = append(messages, b...)
			}
			d, err := replayRecords(c.Delta)
			if err != nil {
				return err
			}
			messages = append(messages, d...)
			if len(messages) > maxArchiveRecords {
				return fmt.Errorf("%w: too many history messages", ErrCorrupt)
			}
		}
		if err = ValidateHistory(messages); err != nil {
			return err
		}
		return s.repo.ConfirmLoaded(op, selection.Scope, pinned.Lease, s.options.LeaseDuration)
	})
	if err != nil {
		if pinned.Lease.Token != "" {
			err = errors.Join(err, s.release(selection.Scope, pinned.Lease))
		}
		return LoadResult{}, err
	}
	baseline, err := Snapshot(TurnCapture{Bootstrap: History(messages...)})
	if err != nil {
		return LoadResult{}, errors.Join(err, s.release(selection.Scope, pinned.Lease))
	}
	parentCtx, cancel := context.WithCancel(s.ctx)
	parent := &LoadedParent{service: s, scope: selection.Scope, ref: pinned.Nodes[len(pinned.Nodes)-1].Ref, lease: pinned.Lease, baseline: baseline.Bootstrap, valid: true, ctx: parentCtx, cancel: cancel, done: make(chan struct{})}
	s.mu.Lock()
	s.parents[parent] = struct{}{}
	s.mu.Unlock()
	go parent.heartbeat()
	return LoadResult{Messages: messages, Parent: parent}, nil
}

// LoadedParent can only be constructed by a complete successful Load. Close it after
// generation/delivery/Commit, including when saving is disabled or the call fails.
type LoadedParent struct {
	service  *Service
	scope    Scope
	ref      NodeRef
	lease    Lease
	baseline []Record
	mu       sync.Mutex
	valid    bool
	ctx      context.Context
	cancel   context.CancelFunc
	done     chan struct{}
	closeErr error
}

// Ref returns the successfully loaded node, not a transferable parent proof.
func (p *LoadedParent) Ref() NodeRef { return p.ref }

// Close stops renewal, releases the pin, and reports release or storage cleanup failures.
func (p *LoadedParent) Close() error {
	p.cancel()
	<-p.done
	return p.closeErr
}

func (p *LoadedParent) alive() bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.valid
}

func (p *LoadedParent) heartbeat() {
	defer close(p.done)
	defer func() {
		p.mu.Lock()
		p.valid = false
		p.mu.Unlock()
		p.closeErr = p.service.release(p.scope, p.lease)
		p.service.mu.Lock()
		delete(p.service.parents, p)
		p.service.mu.Unlock()
	}()
	ticker := time.NewTicker(p.service.options.RenewInterval)
	defer ticker.Stop()
	for {
		select {
		case <-p.ctx.Done():
			return
		case <-ticker.C:
			ctx, cancel := context.WithTimeout(p.ctx, p.service.options.OperationTimeout)
			err := p.service.files.WithScopeLock(ctx, p.scope, func(*ScopeFiles) error {
				return p.service.repo.Renew(ctx, p.scope, p.lease, p.service.options.LeaseDuration)
			})
			cancel()
			if err != nil {
				return
			}
		}
	}
}

func (s *Service) release(scope Scope, lease Lease) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
	defer cancel()
	return s.files.WithScopeLock(ctx, scope, func(*ScopeFiles) error { return s.repo.Release(ctx, scope, lease) })
}

// Commit publishes a complete delivered turn, using a new root if its loaded parent was fenced.
func (s *Service) Commit(ctx context.Context, req CommitRequest) (Node, error) {
	op, done, err := s.begin(ctx)
	if err != nil {
		return Node{}, err
	}
	defer done()
	req.Scope, err = s.scope(req.Scope)
	if err != nil {
		return Node{}, err
	}
	if req.Agent == "" || !ValidID(req.RunID) || len(req.Receipt.MessageIDs) == 0 {
		return Node{}, ErrCorrupt
	}
	for _, id := range req.Receipt.MessageIDs {
		if id <= 0 {
			return Node{}, ErrCorrupt
		}
	}
	req.Receipt.MessageIDs = append([]int(nil), req.Receipt.MessageIDs...)
	if req.Parent != nil && (req.Parent.service != s || req.Parent.scope != req.Scope) {
		return Node{}, ErrCorrupt
	}
	capture, err := Snapshot(req.Capture)
	if err != nil {
		return Node{}, err
	}
	if err = ValidateCapture(capture, req.Parent == nil); err != nil {
		return Node{}, err
	}
	var node Node
	err = s.files.WithScopeLock(op, req.Scope, func(files *ScopeFiles) error {
		published, err := s.repo.GetPublication(op, req.Scope, req.RunID)
		if err != nil {
			return err
		}
		if published != nil {
			if published.Agent != req.Agent {
				return ErrCorrupt
			}
			node = *published
			return nil
		}
		reservation := Reservation{Scope: req.Scope, Agent: req.Agent, RunID: req.RunID}
		if req.Parent != nil {
			if req.Parent.alive() {
				err = s.repo.Renew(op, req.Scope, req.Parent.lease, s.options.LeaseDuration)
			} else {
				err = ErrFence
			}
			switch {
			case err == nil:
				ref, lease := req.Parent.ref, req.Parent.lease
				reservation.Parent, reservation.Lease = &ref, &lease
			case errors.Is(err, ErrFence):
				capture.Bootstrap = req.Parent.baseline
				if err = ValidateCapture(capture, true); err != nil {
					return err
				}
			default:
				return err
			}
		}
		intent, err := s.repo.Reserve(op, reservation, s.options.LeaseDuration)
		if err != nil {
			return err
		}
		digest, size, err := files.WriteAtomic(intent.Node, capture)
		if err != nil {
			return errors.Join(err, s.compensate(files, req.Scope, intent))
		}
		node, err = s.repo.Publish(op, req.Scope, intent, digest, size, req.Receipt)
		if err != nil {
			// Use a fresh bounded context: a lost/canceled publication response is not proof of failure.
			check, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
			defer cancel()
			published, queryErr := s.repo.GetPublication(check, req.Scope, req.RunID)
			if queryErr != nil {
				return errors.Join(ErrUnknown, err, queryErr)
			}
			if published == nil {
				return errors.Join(err, s.compensate(files, req.Scope, intent))
			}
			if published.Ref != intent.Node.Ref || published.Digest != digest || published.Size != size {
				return ErrCorrupt
			}
			node = *published
		}
		// This cleanup is optional for visibility: a crash is recoverable from the published intent.
		cleanup, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
		defer cancel()
		_ = s.repo.FinishIntent(cleanup, req.Scope, intent)
		return nil
	})
	if err != nil {
		return Node{}, err
	}
	return node, nil
}

func (s *Service) compensate(files *ScopeFiles, scope Scope, intent Intent) error {
	ctx, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
	defer cancel()
	ok, err := s.repo.AbortIntent(ctx, scope, intent)
	if err != nil {
		return errors.Join(ErrUnknown, err)
	}
	if !ok {
		return nil
	} // Published nodes must never be removed by compensation.
	if err = files.Remove(intent.Node); err != nil {
		return err
	}
	return s.repo.FinishIntent(ctx, scope, intent)
}

// Close cancels operations and renewal, waits for pins, and closes files without closing Redis.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		s.mu.Lock()
		parents := make([]*LoadedParent, 0, len(s.parents))
		for parent := range s.parents {
			parents = append(parents, parent)
		}
		s.mu.Unlock()
		for _, parent := range parents {
			s.closeErr = errors.Join(s.closeErr, parent.Close())
		}
		s.closeErr = errors.Join(s.closeErr, s.files.Close())
	})
	return s.closeErr
}
