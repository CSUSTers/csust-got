package session

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/cloudwego/eino/schema"
	"go.uber.org/zap"
)

var (
	errSessionStorageRequired = errors.New("session repository and files are required")
	errSessionDurations       = errors.New("invalid session durations")
	errSessionLoadOnlyParent  = errors.New("load-only session parent cannot be committed")
)

// Options controls idle qualification, lease renewal, and operation deadlines.
type Options struct {
	// TTL accepts any positive duration; zero uses 24h. Expiry has Redis millisecond precision.
	TTL              time.Duration
	LeaseDuration    time.Duration
	RenewInterval    time.Duration
	OperationTimeout time.Duration
	// CollectTimeout bounds one scope's expiry collection; zero uses 2m.
	CollectTimeout time.Duration
}

// CollectionMarker optionally persists the local calendar day of the last completed collection.
type CollectionMarker interface {
	LastCollection(context.Context) (string, error)
	MarkCollection(context.Context, string) error
}

var forkedRootCommits atomic.Int64

// ForkedRootCommits counts commits that lost their loaded parent lease and published a new root.
func ForkedRootCommits() int64 { return forkedRootCommits.Load() }

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
	if options.CollectTimeout == 0 {
		options.CollectTimeout = 2 * time.Minute
	}
	if options.TTL <= 0 || options.LeaseDuration < time.Millisecond || options.RenewInterval <= 0 || options.RenewInterval >= options.LeaseDuration || options.OperationTimeout <= 0 || options.CollectTimeout <= 0 {
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

func (s *Service) track(ctx context.Context) (context.Context, func(), error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, nil, ErrClosed
	}
	s.wg.Add(1)
	s.mu.Unlock()
	op, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	return op, func() { stop(); cancel(); s.wg.Done() }, nil
}

func (s *Service) begin(ctx context.Context) (context.Context, func(), error) {
	tracked, done, err := s.track(ctx)
	if err != nil {
		return nil, nil, err
	}
	op, cancel := context.WithTimeout(tracked, s.options.OperationTimeout)
	return op, func() { cancel(); done() }, nil
}

// Load returns complete replay and a parent proof only after all ancestors are verified.
func (s *Service) Load(ctx context.Context, selection Selection) (LoadResult, error) {
	return s.LoadWithAcceptance(ctx, selection, nil)
}

// LoadWithAcceptance validates a pinned candidate before accepting it as user activity.
// accept may mutate messages, but must not retain the candidate or reenter this Service.
// It runs synchronously outside the scope lock, with caller and Service cancellation;
// only storage phases have the default operation deadline. Close waits for accept to return.
func (s *Service) LoadWithAcceptance(ctx context.Context, selection Selection, accept func(context.Context, *LoadCandidate) error) (result LoadResult, loadErr error) {
	op, done, err := s.track(ctx)
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
	defer func() {
		if pinned.Lease.Token != "" && result.Parent == nil {
			loadErr = errors.Join(loadErr, s.release(selection.Scope, pinned.Lease))
		}
	}()
	var messages []*schema.Message
	ancestorReplyIDs := map[int]struct{}{}
	readCtx, cancelRead := context.WithTimeout(op, s.options.OperationTimeout)
	defer cancelRead()
	err = s.files.WithScopeLock(readCtx, selection.Scope, func(files *ScopeFiles) error {
		var err error
		pinned, err = s.repo.ResolveAndPin(readCtx, selection, token, s.options.LeaseDuration)
		if err != nil {
			return err
		}
		if len(pinned.Nodes) == 0 {
			return ErrCorrupt
		}
		seen := map[NodeRef]bool{}
		var archiveSize int64
		for i, n := range pinned.Nodes {
			if err := readCtx.Err(); err != nil {
				return err
			}
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
			for _, id := range n.ReplyMessageIDs {
				ancestorReplyIDs[id] = struct{}{}
			}
		}
		return ValidateHistory(messages)
	})
	if err == nil {
		err = readCtx.Err()
	}
	cancelRead()
	if err != nil {
		return LoadResult{}, err
	}
	candidateCtx, cancelCandidate := context.WithCancelCause(op)
	defer cancelCandidate(nil)
	renewCtx, cancelRenew := context.WithCancel(candidateCtx)
	renewDone := make(chan error, 1)
	go func() {
		err := s.renewLease(renewCtx, selection.Scope, pinned.Lease)
		if err != nil {
			cancelCandidate(err)
		}
		renewDone <- err
	}()
	stopRenew := sync.OnceValue(func() error {
		cancelRenew()
		return <-renewDone
	})
	defer func() { loadErr = errors.Join(loadErr, stopRenew()) }()
	baseline, err := loadBaseline(messages, selection.LoadOnly)
	if err != nil {
		return LoadResult{}, err
	}
	if err = context.Cause(candidateCtx); err == nil && accept != nil {
		err = accept(candidateCtx, &LoadCandidate{Messages: messages, ancestorReplyIDs: ancestorReplyIDs})
	}
	if err != nil {
		return LoadResult{}, err
	}
	if err = errors.Join(stopRenew(), context.Cause(candidateCtx)); err != nil {
		return LoadResult{}, err
	}
	confirmCtx, cancelConfirm := context.WithTimeout(op, s.options.OperationTimeout)
	defer cancelConfirm()
	err = s.files.WithScopeLock(confirmCtx, selection.Scope, func(*ScopeFiles) error {
		return s.repo.ConfirmLoaded(confirmCtx, selection.Scope, pinned.Lease, s.options.LeaseDuration)
	})
	if err == nil {
		err = confirmCtx.Err()
	}
	cancelConfirm()
	if err != nil {
		return LoadResult{}, err
	}
	s.mu.Lock()
	err = op.Err()
	if s.closed && err == nil {
		err = ErrClosed
	}
	if err != nil {
		s.mu.Unlock()
		return LoadResult{}, err
	}
	parentCtx, cancel := context.WithCancel(s.ctx)
	parent := &LoadedParent{service: s, scope: selection.Scope, ref: pinned.Nodes[len(pinned.Nodes)-1].Ref, lease: pinned.Lease, baseline: baseline, loadOnly: selection.LoadOnly, ancestorReplyIDs: ancestorReplyIDs, valid: true, ctx: parentCtx, cancel: cancel, done: make(chan struct{})}
	s.parents[parent] = struct{}{}
	s.mu.Unlock()
	go parent.heartbeat()
	return LoadResult{Messages: messages, Parent: parent}, nil
}

func loadBaseline(messages []*schema.Message, loadOnly bool) ([]Record, error) {
	if loadOnly {
		return nil, nil
	}
	baseline, err := Snapshot(TurnCapture{Bootstrap: History(messages...)})
	return baseline.Bootstrap, err
}

// RejectContext records an unusable node for the supplied context without accepting it as activity.
func (s *Service) RejectContext(ctx context.Context, scope Scope, ref NodeRef, contextKey string) error {
	op, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	scope, err = s.scope(scope)
	if err != nil {
		return err
	}
	if err = s.repo.RejectContext(op, scope, ref, contextKey); err != nil {
		return err
	}
	return op.Err()
}

// LoadedParent can only be constructed by a complete successful Load. Close it after
// generation/delivery/Commit, including when saving is disabled or the call fails.
type LoadedParent struct {
	service          *Service
	scope            Scope
	ref              NodeRef
	lease            Lease
	baseline         []Record
	loadOnly         bool
	ancestorReplyIDs map[int]struct{}
	mu               sync.Mutex
	valid            bool
	ctx              context.Context
	cancel           context.CancelFunc
	done             chan struct{}
	closeErr         error
}

// Ref returns the successfully loaded node, not a transferable parent proof.
func (p *LoadedParent) Ref() NodeRef { return p.ref }

// ContainsReplyMessageID reports coverage of this parent's verified ancestor chain.
func (p *LoadedParent) ContainsReplyMessageID(id int) bool {
	if p == nil || id <= 0 {
		return false
	}
	_, ok := p.ancestorReplyIDs[id]
	return ok
}

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
	_ = p.service.renewLease(p.ctx, p.scope, p.lease)
}

// renewLease retries transient renewal failures until the lease itself would lapse;
// only fencing, corruption, or closure end renewal early.
func (s *Service) renewLease(ctx context.Context, scope Scope, lease Lease) error {
	ticker := time.NewTicker(s.options.RenewInterval)
	defer ticker.Stop()
	lastSuccess := time.Now()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			op, cancel := context.WithTimeout(ctx, s.options.OperationTimeout)
			err := s.files.WithScopeLock(op, scope, func(*ScopeFiles) error {
				return s.repo.Renew(op, scope, lease, s.options.LeaseDuration)
			})
			if err == nil {
				err = op.Err()
			}
			cancel()
			if ctx.Err() != nil {
				return nil
			}
			if err == nil {
				lastSuccess = time.Now()
				continue
			}
			if errors.Is(err, ErrFence) || errors.Is(err, ErrCorrupt) || errors.Is(err, ErrClosed) {
				return err
			}
			if time.Since(lastSuccess) >= s.options.LeaseDuration-s.options.RenewInterval/2 {
				return err
			}
			zap.L().Debug("session: lease renewal failed; retrying before lease expiry", zap.String("dag_id", lease.DAGID), zap.Error(err))
		}
	}
}

// Replay reads one node's complete ancestor history per turn without pinning the DAG or
// refreshing its activity. It is a read-only view for background compaction.
func (s *Service) Replay(ctx context.Context, scope Scope, ref NodeRef) ([]ReplayTurn, error) {
	op, done, err := s.begin(ctx)
	if err != nil {
		return nil, err
	}
	defer done()
	scope, err = s.scope(scope)
	if err != nil {
		return nil, err
	}
	if err = ref.Validate(); err != nil {
		return nil, err
	}
	var turns []ReplayTurn
	err = s.files.WithScopeLock(op, scope, func(files *ScopeFiles) error {
		nodes, err := s.repo.Chain(op, scope, ref)
		if err != nil {
			return err
		}
		if len(nodes) == 0 || nodes[len(nodes)-1].Ref != ref {
			return ErrCorrupt
		}
		var messages []*schema.Message
		var archiveSize int64
		seen := map[NodeRef]bool{}
		for i, n := range nodes {
			if err := op.Err(); err != nil {
				return err
			}
			if n.Scope != scope || n.Ref.DAGID != ref.DAGID || seen[n.Ref] {
				return ErrCorrupt
			}
			seen[n.Ref] = true
			if n.Size <= 0 || n.Size > maxArchiveBytes-archiveSize {
				return fmt.Errorf("%w: session chain too large", ErrCorrupt)
			}
			archiveSize += n.Size
			if i == 0 && n.Parent != nil || i > 0 && (n.Parent == nil || *n.Parent != nodes[i-1].Ref) {
				return ErrCorrupt
			}
			c, err := files.Read(n)
			if err != nil {
				return fmt.Errorf("%w: %w", ErrCorrupt, err)
			}
			turn := ReplayTurn{Node: n}
			if i == 0 {
				if turn.Bootstrap, err = replayRecords(c.Bootstrap); err != nil {
					return err
				}
				messages = append(messages, turn.Bootstrap...)
			}
			if turn.Delta, err = replayRecords(c.Delta); err != nil {
				return err
			}
			messages = append(messages, turn.Delta...)
			if len(messages) > maxArchiveRecords {
				return fmt.Errorf("%w: too many history messages", ErrCorrupt)
			}
			turns = append(turns, turn)
		}
		return ValidateHistory(messages)
	})
	if err == nil {
		err = op.Err()
	}
	if err != nil {
		return nil, err
	}
	return turns, nil
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
	if req.Receipt.RedirectFrom != nil && (req.Parent != nil || req.Receipt.RedirectFrom.Validate() != nil) {
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
	if req.Parent != nil && req.Parent.loadOnly {
		return Node{}, errSessionLoadOnlyParent
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
				forkedRootCommits.Add(1)
				zap.L().Warn("session: loaded parent lease lost; committing turn as a new root",
					zap.String("scope", req.Scope.Key()), zap.Int64("chat_id", req.Scope.ChatID), zap.String("agent", req.Agent),
					zap.String("parent_dag_id", req.Parent.ref.DAGID), zap.String("parent_node_id", req.Parent.ref.NodeID), zap.Error(err))
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
	return s.abortIntent(ctx, files, scope, intent)
}

func (s *Service) abortIntent(ctx context.Context, files *ScopeFiles, scope Scope, intent Intent) error {
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
	if ctx.Err() != nil {
		// The abort and file removal already started; finish only this durable intent.
		cleanup, cancel := context.WithTimeout(context.Background(), s.options.OperationTimeout)
		defer cancel()
		return s.repo.FinishIntent(cleanup, scope, intent)
	}
	return s.repo.FinishIntent(ctx, scope, intent)
}

// LastCollection reads the persisted local day of the last completed collection, if the repository supports it.
func (s *Service) LastCollection(ctx context.Context) (string, error) {
	marker, ok := s.repo.(CollectionMarker)
	if !ok {
		return "", errors.ErrUnsupported
	}
	op, done, err := s.begin(ctx)
	if err != nil {
		return "", err
	}
	defer done()
	return marker.LastCollection(op)
}

// MarkCollection persists the local day of a completed collection, if the repository supports it.
func (s *Service) MarkCollection(ctx context.Context, day string) error {
	marker, ok := s.repo.(CollectionMarker)
	if !ok {
		return errors.ErrUnsupported
	}
	op, done, err := s.begin(ctx)
	if err != nil {
		return err
	}
	defer done()
	return marker.MarkCollection(op, day)
}

// Close cancels operations and renewal, waits for pins, and closes files without closing Redis.
func (s *Service) Close() error {
	s.closeOnce.Do(func() {
		s.mu.Lock()
		s.closed = true
		parents := make([]*LoadedParent, 0, len(s.parents))
		for parent := range s.parents {
			parents = append(parents, parent)
		}
		s.cancel()
		s.mu.Unlock()
		s.wg.Wait()
		for _, parent := range parents {
			s.closeErr = errors.Join(s.closeErr, parent.Close())
		}
		s.closeErr = errors.Join(s.closeErr, s.files.Close())
	})
	return s.closeErr
}
