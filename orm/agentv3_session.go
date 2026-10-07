package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

const (
	sessionDAGActive       = "active"
	sessionDAGDeleting     = "deleting"
	sessionIntentPending   = "pending"
	sessionIntentPublished = "published"
	sessionIntentAborted   = "aborted"
)

var errSessionRedisClientRequired = errors.New("session Redis client is nil")

// AgentV3SessionRepository maintains per-scope DAG state with atomic Redis transactions.
type AgentV3SessionRepository struct {
	client    *redis.Client
	base      string
	namespace string
}

var _ session.Repository = (*AgentV3SessionRepository)(nil)

// NewAgentV3SessionRepository borrows client and derives a namespace from keyPrefix.
func NewAgentV3SessionRepository(client *redis.Client, keyPrefix string) (*AgentV3SessionRepository, error) {
	if client == nil {
		return nil, errSessionRedisClientRequired
	}
	ns := session.StorageNamespace(keyPrefix)
	return &AgentV3SessionRepository{client: client, namespace: ns, base: keyPrefix + "agentv3:session:{" + ns + "}:"}, nil
}

// Namespace returns the deployment's file storage namespace.
func (r *AgentV3SessionRepository) Namespace() string { return r.namespace }

type sessionDAG struct {
	ID         string                    `json:"id"`
	Generation string                    `json:"generation"`
	State      string                    `json:"state"`
	LastActive int64                     `json:"last_active"`
	Nodes      map[string]session.Node   `json:"nodes"`
	Intents    map[string]session.Intent `json:"intents"`
	Leases     map[string]session.Lease  `json:"leases"`
}

type sessionState struct {
	Version  int                        `json:"version"`
	Scope    session.Scope              `json:"scope"`
	Sequence int64                      `json:"sequence"`
	DAGs     map[string]*sessionDAG     `json:"dags"`
	Messages map[string]session.NodeRef `json:"messages"`
	Runs     map[string]session.NodeRef `json:"runs"`
}

func emptySessionState(scope session.Scope) sessionState {
	return sessionState{Version: session.Version, Scope: scope, DAGs: map[string]*sessionDAG{}, Messages: map[string]session.NodeRef{}, Runs: map[string]session.NodeRef{}}
}

func (r *AgentV3SessionRepository) scopeKey(s session.Scope) (string, error) {
	if err := s.Validate(); err != nil {
		return "", err
	}
	if s.Namespace != r.namespace {
		return "", session.ErrCorrupt
	}
	return r.base + "scope:" + s.Key(), nil
}

func readSessionState(ctx context.Context, cmd redis.Cmdable, key string, scope session.Scope) (sessionState, error) {
	b, err := cmd.Get(ctx, key).Bytes()
	if errors.Is(err, redis.Nil) {
		return emptySessionState(scope), nil
	}
	if err != nil {
		return sessionState{}, err
	}
	var state sessionState
	if err = json.Unmarshal(b, &state); err != nil {
		return state, fmt.Errorf("%w: Redis state", session.ErrCorrupt)
	}
	if state.Version != session.Version || state.Scope != scope || state.DAGs == nil || state.Messages == nil || state.Runs == nil {
		return state, session.ErrCorrupt
	}
	for id, d := range state.DAGs {
		if d == nil || id != d.ID || !session.ValidID(id) || !session.ValidID(d.Generation) || d.State != sessionDAGActive && d.State != sessionDAGDeleting || d.Nodes == nil || d.Intents == nil || d.Leases == nil {
			return state, session.ErrCorrupt
		}
	}
	return state, nil
}

func (r *AgentV3SessionRepository) mutate(ctx context.Context, scope session.Scope, fn func(*sessionState, int64) error) error {
	key, err := r.scopeKey(scope)
	if err != nil {
		return err
	}
	for range 32 {
		err = r.client.Watch(ctx, func(tx *redis.Tx) error {
			state, err := readSessionState(ctx, tx, key, scope)
			if err != nil {
				return err
			}
			now, err := tx.Time(ctx).Result()
			if err != nil {
				return err
			}
			if err = fn(&state, now.UnixMilli()); err != nil {
				return err
			}
			b, err := json.Marshal(state)
			if err != nil {
				return err
			}
			scopeJSON, err := json.Marshal(scope)
			if err != nil {
				return err
			}
			_, err = tx.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
				if len(state.DAGs) == 0 {
					pipe.Del(ctx, key)
					pipe.HDel(ctx, r.base+"scopes", scope.Key())
				} else {
					pipe.Set(ctx, key, b, 0)
					pipe.HSet(ctx, r.base+"scopes", scope.Key(), scopeJSON)
				}
				return nil
			})
			return err
		}, key)
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
		if err = ctx.Err(); err != nil {
			return err
		}
	}
	return session.ErrConflict
}

func sessionNode(state *sessionState, ref session.NodeRef) (session.Node, bool) {
	d := state.DAGs[ref.DAGID]
	if d == nil {
		return session.Node{}, false
	}
	n, ok := d.Nodes[ref.NodeID]
	return n, ok && n.Ref == ref
}

func validSessionLease(d *sessionDAG, lease session.Lease, now int64) bool {
	if d == nil || d.State != sessionDAGActive || lease.Generation != d.Generation || lease.DAGID != d.ID {
		return false
	}
	stored, ok := d.Leases[lease.Token]
	return ok && stored.Token == lease.Token && stored.DAGID == lease.DAGID && stored.Generation == lease.Generation && stored.Deadline > now
}

// ResolveAndPin atomically selects an active node, validates its ancestors, and pins its DAG.
func (r *AgentV3SessionRepository) ResolveAndPin(ctx context.Context, sel session.Selection, token string, duration time.Duration) (session.Pinned, error) {
	if !session.ValidID(token) || duration < time.Millisecond {
		return session.Pinned{}, session.ErrCorrupt
	}
	var pinned session.Pinned
	err := r.mutate(ctx, sel.Scope, func(state *sessionState, now int64) error {
		pinned = session.Pinned{}
		var ref session.NodeRef
		switch sel.Mode {
		case session.SelectNone:
			return session.ErrMiss
		case session.SelectReply:
			if sel.ReplyMessageID <= 0 {
				return session.ErrMiss
			}
			var ok bool
			ref, ok = state.Messages[strconv.Itoa(sel.ReplyMessageID)]
			if !ok {
				return session.ErrMiss
			}
		case session.SelectLatest:
			var latest int64
			for _, dag := range state.DAGs {
				if dag.State != sessionDAGActive {
					continue
				}
				for _, n := range dag.Nodes {
					if n.Agent == sel.Agent && n.CommitSequence > latest {
						ref, latest = n.Ref, n.CommitSequence
					}
				}
			}
			if latest == 0 {
				return session.ErrMiss
			}
		default:
			return session.ErrCorrupt
		}
		d := state.DAGs[ref.DAGID]
		if d == nil || d.State != sessionDAGActive {
			return session.ErrMiss
		}
		seen := map[string]bool{}
		for {
			if ref.Validate() != nil || ref.DAGID != d.ID || seen[ref.NodeID] {
				return session.ErrCorrupt
			}
			seen[ref.NodeID] = true
			n, ok := sessionNode(state, ref)
			if !ok || n.Scope != sel.Scope || n.Version != session.Version || n.FileName != n.Ref.NodeID+".jsonl" || !session.ValidID(n.RunID) {
				return session.ErrCorrupt
			}
			pinned.Nodes = append(pinned.Nodes, n)
			if n.Parent == nil {
				break
			}
			ref = *n.Parent
		}
		for i, j := 0, len(pinned.Nodes)-1; i < j; i, j = i+1, j-1 {
			pinned.Nodes[i], pinned.Nodes[j] = pinned.Nodes[j], pinned.Nodes[i]
		}
		pinned.Lease = session.Lease{DAGID: d.ID, Generation: d.Generation, Token: token, Deadline: now + duration.Milliseconds()}
		d.Leases[token] = pinned.Lease
		return nil
	})
	if err != nil {
		return session.Pinned{}, err
	}
	return pinned, nil
}

func (r *AgentV3SessionRepository) updateLease(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration, confirm bool) error {
	if duration < time.Millisecond {
		return session.ErrCorrupt
	}
	return r.mutate(ctx, scope, func(state *sessionState, now int64) error {
		d := state.DAGs[lease.DAGID]
		if !validSessionLease(d, lease, now) {
			return session.ErrFence
		}
		stored := d.Leases[lease.Token]
		stored.Deadline = now + duration.Milliseconds()
		d.Leases[lease.Token] = stored
		if confirm {
			d.LastActive = now
		}
		return nil
	})
}

// ConfirmLoaded validates the pin and refreshes activity after complete archive loading.
func (r *AgentV3SessionRepository) ConfirmLoaded(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	return r.updateLease(ctx, scope, lease, duration, true)
}

// Renew extends a valid pin without refreshing user activity.
func (r *AgentV3SessionRepository) Renew(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	return r.updateLease(ctx, scope, lease, duration, false)
}

// Release idempotently removes this generation's pin token.
func (r *AgentV3SessionRepository) Release(ctx context.Context, scope session.Scope, lease session.Lease) error {
	return r.mutate(ctx, scope, func(state *sessionState, _ int64) error {
		if d := state.DAGs[lease.DAGID]; d != nil && d.Generation == lease.Generation {
			delete(d.Leases, lease.Token)
		}
		return nil
	})
}

// Reserve durably registers a unique file attempt under a valid parent or a new root.
func (r *AgentV3SessionRepository) Reserve(ctx context.Context, req session.Reservation, duration time.Duration) (session.Intent, error) {
	if !session.ValidID(req.RunID) || req.Agent == "" || duration < time.Millisecond || (req.Parent == nil) != (req.Lease == nil) {
		return session.Intent{}, session.ErrCorrupt
	}
	nodeID, err := session.NewID()
	if err != nil {
		return session.Intent{}, err
	}
	dagID, err := session.NewID()
	if err != nil {
		return session.Intent{}, err
	}
	generation, err := session.NewID()
	if err != nil {
		return session.Intent{}, err
	}
	token, err := session.NewID()
	if err != nil {
		return session.Intent{}, err
	}
	var intent session.Intent
	err = r.mutate(ctx, req.Scope, func(state *sessionState, now int64) error {
		for _, d := range state.DAGs {
			if previous, ok := d.Intents[req.RunID]; ok {
				if previous.Node.Agent != req.Agent || !sameSessionParent(previous.Node.Parent, req.Parent) {
					return session.ErrCorrupt
				}
				if previous.Status == sessionIntentAborted {
					return session.ErrFence
				}
				intent = previous
				return nil
			}
		}
		if _, ok := state.Runs[req.RunID]; ok {
			return session.ErrConflict
		}
		var d *sessionDAG
		var lease session.Lease
		if req.Parent != nil {
			d = state.DAGs[req.Parent.DAGID]
			if !validSessionLease(d, *req.Lease, now) || req.Lease.DAGID != req.Parent.DAGID {
				return session.ErrFence
			}
			if _, ok := sessionNode(state, *req.Parent); !ok {
				return session.ErrFence
			}
			lease = *req.Lease
		} else {
			d = &sessionDAG{ID: dagID, Generation: generation, State: sessionDAGActive, LastActive: now, Nodes: map[string]session.Node{}, Intents: map[string]session.Intent{}, Leases: map[string]session.Lease{}}
			state.DAGs[dagID] = d
			lease = session.Lease{DAGID: dagID, Generation: generation, Token: token, Deadline: now + duration.Milliseconds()}
			d.Leases[token] = lease
		}
		intent = session.Intent{Node: session.Node{Scope: req.Scope, Ref: session.NodeRef{DAGID: d.ID, NodeID: nodeID}, Parent: req.Parent, Agent: req.Agent, RunID: req.RunID, FileName: nodeID + ".jsonl", Version: session.Version}, Lease: lease, Status: sessionIntentPending}
		d.Intents[req.RunID] = intent
		return nil
	})
	return intent, err
}

func sameSessionParent(a, b *session.NodeRef) bool {
	if a == nil || b == nil {
		return a == nil && b == nil
	}
	return *a == *b
}

// Publish atomically fences the attempt and idempotently publishes its node and indexes.
func (r *AgentV3SessionRepository) Publish(ctx context.Context, scope session.Scope, intent session.Intent, digest string, size int64, receipt session.DeliveryReceipt) (session.Node, error) {
	if len(digest) != 64 || size <= 0 || len(receipt.MessageIDs) == 0 {
		return session.Node{}, session.ErrCorrupt
	}
	for _, id := range receipt.MessageIDs {
		if id <= 0 {
			return session.Node{}, session.ErrCorrupt
		}
	}
	var node session.Node
	err := r.mutate(ctx, scope, func(state *sessionState, now int64) error {
		if ref, ok := state.Runs[intent.Node.RunID]; ok {
			var found bool
			node, found = sessionNode(state, ref)
			if !found || node.Ref != intent.Node.Ref || node.Digest != digest || node.Size != size {
				return session.ErrCorrupt
			}
			return nil
		}
		d := state.DAGs[intent.Node.Ref.DAGID]
		if !validSessionLease(d, intent.Lease, now) {
			return session.ErrFence
		}
		stored, ok := d.Intents[intent.Node.RunID]
		if !ok || stored.Status != sessionIntentPending || stored.Node.Ref != intent.Node.Ref || stored.Lease != intent.Lease {
			return session.ErrFence
		}
		if stored.Node.Parent != nil {
			if _, ok := sessionNode(state, *stored.Node.Parent); !ok {
				return session.ErrFence
			}
		}
		state.Sequence++
		node = stored.Node
		node.Digest, node.Size, node.CommitSequence = digest, size, state.Sequence
		node.ReplyMessageIDs = append([]int(nil), receipt.MessageIDs...)
		d.Nodes[node.Ref.NodeID] = node
		state.Runs[node.RunID] = node.Ref
		for _, id := range receipt.MessageIDs {
			state.Messages[strconv.Itoa(id)] = node.Ref
		}
		d.LastActive = now
		stored.Status, stored.Node = sessionIntentPublished, node
		d.Intents[node.RunID] = stored
		return nil
	})
	return node, err
}

// GetPublication resolves an uncertain result without modifying activity or leases.
func (r *AgentV3SessionRepository) GetPublication(ctx context.Context, scope session.Scope, runID string) (*session.Node, error) {
	key, err := r.scopeKey(scope)
	if err != nil {
		return nil, err
	}
	state, err := readSessionState(ctx, r.client, key, scope)
	if err != nil {
		return nil, err
	}
	ref, ok := state.Runs[runID]
	if !ok {
		return nil, nil
	}
	node, ok := sessionNode(&state, ref)
	if !ok || node.Scope != scope || node.RunID != runID {
		return nil, session.ErrCorrupt
	}
	return &node, nil
}

// AbortIntent fences an unpublished attempt; false means its files must not be compensated.
func (r *AgentV3SessionRepository) AbortIntent(ctx context.Context, scope session.Scope, intent session.Intent) (bool, error) {
	aborted := false
	err := r.mutate(ctx, scope, func(state *sessionState, _ int64) error {
		aborted = false
		if _, ok := state.Runs[intent.Node.RunID]; ok {
			return nil
		}
		d := state.DAGs[intent.Node.Ref.DAGID]
		if d == nil {
			return session.ErrFence
		}
		stored, ok := d.Intents[intent.Node.RunID]
		if !ok || stored.Node.Ref != intent.Node.Ref {
			return session.ErrFence
		}
		stored.Status = sessionIntentAborted
		d.Intents[intent.Node.RunID] = stored
		aborted = true
		return nil
	})
	return aborted, err
}

// FinishIntent removes recovery metadata only after publication or completed compensation.
func (r *AgentV3SessionRepository) FinishIntent(ctx context.Context, scope session.Scope, intent session.Intent) error {
	return r.mutate(ctx, scope, func(state *sessionState, _ int64) error {
		d := state.DAGs[intent.Node.Ref.DAGID]
		if d == nil {
			return nil
		}
		stored, ok := d.Intents[intent.Node.RunID]
		if !ok {
			return nil
		}
		if stored.Node.Ref != intent.Node.Ref || stored.Status == sessionIntentPending {
			return session.ErrFence
		}
		delete(d.Intents, intent.Node.RunID)
		if intent.Node.Parent == nil {
			delete(d.Leases, stored.Lease.Token)
		}
		return nil
	})
}

// Scopes lists the durable namespace catalog without scanning unrelated Redis keys.
func (r *AgentV3SessionRepository) Scopes(ctx context.Context) ([]session.Scope, error) {
	entries, err := r.client.HGetAll(ctx, r.base+"scopes").Result()
	if err != nil {
		return nil, err
	}
	scopes := make([]session.Scope, 0, len(entries))
	for key, entry := range entries {
		var scope session.Scope
		if err = json.Unmarshal([]byte(entry), &scope); err != nil {
			return nil, session.ErrCorrupt
		}
		if scope.Validate() != nil || scope.Namespace != r.namespace || scope.Key() != key {
			return nil, session.ErrCorrupt
		}
		scopes = append(scopes, scope)
	}
	return scopes, nil
}

// Pending returns recoverable intents from active DAGs.
func (r *AgentV3SessionRepository) Pending(ctx context.Context, scope session.Scope) ([]session.Intent, error) {
	key, err := r.scopeKey(scope)
	if err != nil {
		return nil, err
	}
	state, err := readSessionState(ctx, r.client, key, scope)
	if err != nil {
		return nil, err
	}
	var intents []session.Intent
	for _, d := range state.DAGs {
		if d.State == sessionDAGActive {
			for _, i := range d.Intents {
				if i.Node.Scope != scope || i.Node.Ref.DAGID != d.ID || i.Lease.DAGID != d.ID || i.Lease.Generation != d.Generation {
					return nil, session.ErrCorrupt
				}
				intents = append(intents, i)
			}
		}
	}
	return intents, nil
}

func sessionDeletionManifest(scope session.Scope, d *sessionDAG) (session.Deletion, error) {
	manifest := session.Deletion{DAGID: d.ID, Generation: d.Generation}
	for id, n := range d.Nodes {
		if n.Scope != scope || n.Ref.DAGID != d.ID || n.Ref.NodeID != id || n.Ref.Validate() != nil {
			return session.Deletion{}, session.ErrCorrupt
		}
		manifest.Nodes = append(manifest.Nodes, n)
	}
	for run, i := range d.Intents {
		if i.Node.Scope != scope || i.Node.Ref.DAGID != d.ID || i.Node.RunID != run || i.Node.Ref.Validate() != nil || i.Lease.DAGID != d.ID || i.Lease.Generation != d.Generation {
			return session.Deletion{}, session.ErrCorrupt
		}
		manifest.Intents = append(manifest.Intents, i)
	}
	return manifest, nil
}

// Deleting reads existing tombstones without claiming expired active DAGs.
func (r *AgentV3SessionRepository) Deleting(ctx context.Context, scope session.Scope) ([]session.Deletion, error) {
	key, err := r.scopeKey(scope)
	if err != nil {
		return nil, err
	}
	state, err := readSessionState(ctx, r.client, key, scope)
	if err != nil {
		return nil, err
	}
	var deletions []session.Deletion
	for _, d := range state.DAGs {
		if d.State != sessionDAGDeleting {
			continue
		}
		manifest, err := sessionDeletionManifest(scope, d)
		if err != nil {
			return nil, err
		}
		deletions = append(deletions, manifest)
	}
	return deletions, nil
}

// ClaimDeleting atomically rechecks idle eligibility and pins, then freezes due DAGs.
func (r *AgentV3SessionRepository) ClaimDeleting(ctx context.Context, scope session.Scope, ttl time.Duration) ([]session.Deletion, error) {
	if ttl <= 0 {
		return nil, session.ErrCorrupt
	}
	var deletions []session.Deletion
	err := r.mutate(ctx, scope, func(state *sessionState, now int64) error {
		deletions = nil
		for _, d := range state.DAGs {
			if d.State == sessionDAGActive {
				if d.LastActive > now-ttl.Milliseconds() {
					continue
				}
				pinned := false
				for token, l := range d.Leases {
					if l.Deadline > now {
						pinned = true
					} else {
						delete(d.Leases, token)
					}
				}
				if pinned {
					continue
				}
			}
			manifest, err := sessionDeletionManifest(scope, d)
			if err != nil {
				return err
			}
			d.State = sessionDAGDeleting
			deletions = append(deletions, manifest)
		}
		return nil
	})
	return deletions, err
}

// FinishDelete removes a fenced tombstone and compare-deletes its reference indexes.
func (r *AgentV3SessionRepository) FinishDelete(ctx context.Context, scope session.Scope, deletion session.Deletion) error {
	return r.mutate(ctx, scope, func(state *sessionState, _ int64) error {
		d := state.DAGs[deletion.DAGID]
		if d == nil {
			return nil
		}
		if d.State != sessionDAGDeleting || d.Generation != deletion.Generation {
			return session.ErrFence
		}
		for _, n := range d.Nodes {
			for _, id := range n.ReplyMessageIDs {
				key := strconv.Itoa(id)
				if state.Messages[key] == n.Ref {
					delete(state.Messages, key)
				}
			}
			if state.Runs[n.RunID] == n.Ref {
				delete(state.Runs, n.RunID)
			}
		}
		delete(state.DAGs, d.ID)
		return nil
	})
}
