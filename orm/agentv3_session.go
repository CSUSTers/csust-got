package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"slices"
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

// AgentV3SessionRepository maintains partitioned DAG metadata with Redis transactions.
type AgentV3SessionRepository struct {
	client    *redis.Client
	base      string
	namespace string
}

var _ session.Repository = (*AgentV3SessionRepository)(nil)

// NewAgentV3SessionRepository borrows client and derives the deployment namespace.
func NewAgentV3SessionRepository(client *redis.Client, keyPrefix string) (*AgentV3SessionRepository, error) {
	if client == nil {
		return nil, errSessionRedisClientRequired
	}
	ns := session.StorageNamespace(keyPrefix)
	return &AgentV3SessionRepository{client: client, namespace: ns, base: keyPrefix + "agentv3:session:{" + ns + "}:"}, nil
}

// Namespace returns the deployment's file storage namespace.
func (r *AgentV3SessionRepository) Namespace() string { return r.namespace }

// ResolveAndPin validates one target DAG's ancestors and atomically pins its generation.
func (r *AgentV3SessionRepository) ResolveAndPin(ctx context.Context, sel session.Selection, token string, duration time.Duration) (session.Pinned, error) {
	if !session.ValidID(token) || duration < time.Millisecond {
		return session.Pinned{}, session.ErrCorrupt
	}
	if sel.Mode == session.SelectNone {
		return session.Pinned{}, session.ErrMiss
	}
	s, err := r.scopeKeys(sel.Scope)
	if err != nil {
		return session.Pinned{}, err
	}
	var out session.Pinned
	err = r.atomic(ctx, func(t *sessionTxn) error {
		out = session.Pinned{}
		var ref session.NodeRef
		var sequence int64
		switch sel.Mode {
		case session.SelectReply:
			if sel.ReplyMessageID <= 0 {
				return nil
			}
			if err := t.check(map[string]string{s.messages: sessionRedisHash}); err != nil {
				return err
			}
			found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, s.messages, strconv.Itoa(sel.ReplyMessageID)), &ref)
			if err != nil || !found {
				return err
			}
		case session.SelectLatest:
			key := s.latest(sel.Agent)
			if err := t.check(map[string]string{key: sessionRedisZSet}); err != nil {
				return err
			}
			entries, err := t.tx.ZRevRangeWithScores(ctx, key, 0, 0).Result()
			if err != nil {
				return err
			}
			if len(entries) == 0 {
				return nil
			}
			if entries[0].Score != 0 {
				return session.ErrCorrupt
			}
			ref, sequence, err = sessionParseLatest(fmt.Sprint(entries[0].Member))
			if err != nil {
				return err
			}
		default:
			return session.ErrCorrupt
		}
		if ref.Validate() != nil {
			return session.ErrCorrupt
		}
		d := s.dag(ref.DAGID)
		if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash, d.leases: sessionRedisHash}); err != nil {
			return err
		}
		var metaCmd *redis.StringCmd
		var nodesCmd *redis.MapStringStringCmd
		var clockCmd *redis.TimeCmd
		_, err := t.tx.Pipelined(ctx, func(p redis.Pipeliner) error {
			metaCmd = p.Get(ctx, d.meta)
			nodesCmd = p.HGetAll(ctx, d.nodes)
			clockCmd = p.Time(ctx)
			return nil
		})
		if err != nil && !errors.Is(err, redis.Nil) {
			return err
		}
		meta, err := sessionReadMeta(ctx, metaCmd, d, sel.Scope)
		if err != nil {
			return err
		}
		if meta == nil {
			return session.ErrCorrupt
		}
		if meta.State != sessionDAGActive {
			return nil
		}
		raw, err := nodesCmd.Result()
		if err != nil {
			return err
		}
		nodes, err := sessionDecodeNodes(ctx, raw, sel.Scope, meta.ID)
		if err != nil {
			return err
		}
		selected, ok := nodes[ref.NodeID]
		if !ok {
			return session.ErrCorrupt
		}
		if sequence != 0 && (selected.Agent != sel.Agent || selected.CommitSequence != sequence) {
			return session.ErrCorrupt
		}
		if sel.ContextKey != "" {
			if err := t.contextAccepted(d, selected.Ref, sel.ContextKey); err != nil {
				return err
			}
		}
		out.Nodes, err = sessionAncestorChain(ctx, nodes, ref, meta.ID)
		if err != nil {
			return err
		}
		now, err := clockCmd.Result()
		if err != nil {
			return err
		}
		out.Lease = session.Lease{DAGID: meta.ID, Generation: meta.Generation, Token: token, Deadline: now.UnixMilli() + duration.Milliseconds()}
		var previous session.Lease
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.leases, token), &previous)
		if err != nil {
			return err
		}
		if found && sessionValidateLease(previous, meta) != nil {
			return session.ErrCorrupt
		}
		if !found || previous != out.Lease {
			t.write("hset", d.leases, token, sessionEncode(out.Lease))
		}
		return nil
	})
	if err != nil {
		return session.Pinned{}, err
	}
	if len(out.Nodes) == 0 {
		return session.Pinned{}, session.ErrMiss
	}
	return out, nil
}

func sessionAncestorChain(ctx context.Context, nodes map[string]session.Node, ref session.NodeRef, dagID string) ([]session.Node, error) {
	var chain []session.Node
	seen := map[string]bool{}
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if ref.DAGID != dagID || seen[ref.NodeID] {
			return nil, session.ErrCorrupt
		}
		seen[ref.NodeID] = true
		n, ok := nodes[ref.NodeID]
		if !ok || n.Ref != ref {
			return nil, session.ErrCorrupt
		}
		chain = append(chain, n)
		if n.Parent == nil {
			break
		}
		ref = *n.Parent
	}
	slices.Reverse(chain)
	return chain, nil
}

// Chain reads the root-to-node ancestor metadata of an active DAG without pinning it.
func (r *AgentV3SessionRepository) Chain(ctx context.Context, scope session.Scope, ref session.NodeRef) ([]session.Node, error) {
	if ref.Validate() != nil {
		return nil, session.ErrCorrupt
	}
	s, err := r.scopeKeys(scope)
	if err != nil {
		return nil, err
	}
	d := s.dag(ref.DAGID)
	var out []session.Node
	err = r.atomic(ctx, func(t *sessionTxn) error {
		out = nil
		if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil || meta.State != sessionDAGActive {
			return session.ErrMiss
		}
		raw, err := t.tx.HGetAll(ctx, d.nodes).Result()
		if err != nil {
			return err
		}
		nodes, err := sessionDecodeNodes(ctx, raw, scope, meta.ID)
		if err != nil {
			return err
		}
		if _, ok := nodes[ref.NodeID]; !ok {
			return session.ErrMiss
		}
		out, err = sessionAncestorChain(ctx, nodes, ref, meta.ID)
		return err
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

func (r *AgentV3SessionRepository) updateLease(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration, confirm bool) error {
	if duration < time.Millisecond || !session.ValidID(lease.DAGID) {
		return session.ErrCorrupt
	}
	s, err := r.scopeKeys(scope)
	if err != nil {
		return err
	}
	d := s.dag(lease.DAGID)
	return r.atomic(ctx, func(t *sessionTxn) error {
		if err := t.check(map[string]string{d.meta: "string", d.leases: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		stored, err := t.validLease(d, meta, lease)
		if err != nil {
			return err
		}
		now, err := t.tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		if stored.Deadline <= now.UnixMilli() {
			return session.ErrFence
		}
		deadline := now.UnixMilli() + duration.Milliseconds()
		if stored.Deadline != deadline {
			stored.Deadline = deadline
			t.write("hset", d.leases, lease.Token, sessionEncode(stored))
		}
		if confirm && meta.LastActive != now.UnixMilli() {
			meta.LastActive = now.UnixMilli()
			t.write(sessionRedisSet, d.meta, sessionEncode(meta))
		}
		return nil
	})
}

// ConfirmLoaded refreshes activity only after a complete accepted archive load.
func (r *AgentV3SessionRepository) ConfirmLoaded(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	return r.updateLease(ctx, scope, lease, duration, true)
}

// Renew extends a valid lease without refreshing user activity.
func (r *AgentV3SessionRepository) Renew(ctx context.Context, scope session.Scope, lease session.Lease, duration time.Duration) error {
	return r.updateLease(ctx, scope, lease, duration, false)
}

// Release idempotently removes a matching generation's token.
func (r *AgentV3SessionRepository) Release(ctx context.Context, scope session.Scope, lease session.Lease) error {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return err
	}
	if !session.ValidID(lease.DAGID) {
		return session.ErrCorrupt
	}
	d := s.dag(lease.DAGID)
	return r.atomic(ctx, func(t *sessionTxn) error {
		if err := t.check(map[string]string{d.meta: "string", d.leases: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil || meta.Generation != lease.Generation {
			return nil
		}
		var stored session.Lease
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.leases, lease.Token), &stored)
		if err != nil {
			return err
		}
		if found {
			if sessionValidateLease(stored, meta) != nil || stored.Token != lease.Token {
				return session.ErrCorrupt
			}
			t.write("hdel", d.leases, lease.Token)
		}
		return nil
	})
}

// Reserve durably registers one unique file attempt under a parent or a new root.
func (r *AgentV3SessionRepository) Reserve(ctx context.Context, req session.Reservation, duration time.Duration) (session.Intent, error) {
	if !session.ValidID(req.RunID) || req.Agent == "" || duration < time.Millisecond || (req.Parent == nil) != (req.Lease == nil) {
		return session.Intent{}, session.ErrCorrupt
	}
	s, err := r.scopeKeys(req.Scope)
	if err != nil {
		return session.Intent{}, err
	}
	ids := make([]string, 4)
	for i := range ids {
		ids[i], err = session.NewID()
		if err != nil {
			return session.Intent{}, err
		}
	}
	var out session.Intent
	err = r.atomic(ctx, func(t *sessionTxn) error {
		out = session.Intent{}
		if err := t.check(map[string]string{s.runs: sessionRedisHash, s.pending: sessionRedisSet}); err != nil {
			return err
		}
		run, err := t.run(s, req.RunID)
		if err != nil {
			return err
		}
		if run != nil {
			d := s.dag(run.Ref.DAGID)
			if err := t.check(map[string]string{d.meta: "string", d.intents: sessionRedisHash, d.nodes: sessionRedisHash}); err != nil {
				return err
			}
			meta, err := t.meta(d, req.Scope)
			if err != nil {
				return err
			}
			if meta == nil {
				return session.ErrCorrupt
			}
			var previous session.Intent
			found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.intents, req.RunID), &previous)
			if err != nil {
				return err
			}
			if !found {
				if run.Status == sessionIntentPublished {
					return session.ErrConflict
				}
				return session.ErrCorrupt
			}
			if sessionValidateIntent(previous, meta, req.RunID) != nil || previous.Node.Ref != run.Ref || previous.Status != run.Status {
				return session.ErrCorrupt
			}
			if previous.Node.Agent != req.Agent || !sameSessionParent(previous.Node.Parent, req.Parent) {
				return session.ErrCorrupt
			}
			if previous.Status == sessionIntentAborted || meta.State != sessionDAGActive {
				return session.ErrFence
			}
			out = previous
			return t.ensurePending(s, d)
		}
		var meta *sessionMeta
		var lease session.Lease
		var d sessionDAGKeys
		if req.Parent != nil {
			d = s.dag(req.Parent.DAGID)
			meta, err = t.reserveParent(d, req)
			lease = *req.Lease
		} else {
			d = s.dag(ids[1])
			meta, lease, err = r.reserveRoot(t, s, d, req, ids, duration)
		}
		if err != nil {
			return err
		}
		out = session.Intent{Node: session.Node{Scope: req.Scope, Ref: session.NodeRef{DAGID: meta.ID, NodeID: ids[0]}, Parent: req.Parent, Agent: req.Agent, RunID: req.RunID, FileName: ids[0] + ".jsonl", Version: session.Version}, Lease: lease, Status: sessionIntentPending}
		t.write("hset", d.intents, req.RunID, sessionEncode(out))
		t.write("hset", s.runs, req.RunID, sessionEncode(sessionRunIndex{Ref: out.Node.Ref, Status: out.Status}))
		return t.ensurePending(s, d)
	})
	if err != nil {
		return session.Intent{}, err
	}
	return out, nil
}

func sameSessionParent(a, b *session.NodeRef) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}

// Publish fences the attempt and atomically publishes its node and reference indexes.
func (r *AgentV3SessionRepository) Publish(ctx context.Context, scope session.Scope, intent session.Intent, digest string, size int64, receipt session.DeliveryReceipt) (session.Node, error) {
	if !sessionDigest(digest) || size <= 0 || len(receipt.MessageIDs) == 0 || intent.Node.Ref.Validate() != nil || !session.ValidID(intent.Node.RunID) {
		return session.Node{}, session.ErrCorrupt
	}
	for _, id := range receipt.MessageIDs {
		if id <= 0 {
			return session.Node{}, session.ErrCorrupt
		}
	}
	s, err := r.scopeKeys(scope)
	if err != nil {
		return session.Node{}, err
	}
	d := s.dag(intent.Node.Ref.DAGID)
	var out session.Node
	err = r.atomic(ctx, func(t *sessionTxn) error {
		out = session.Node{}
		if err := t.check(map[string]string{s.runs: sessionRedisHash, s.pending: sessionRedisSet, d.meta: "string", d.nodes: sessionRedisHash, d.leases: sessionRedisHash, d.intents: sessionRedisHash, s.sequence: "string", s.messages: sessionRedisHash, s.latest(intent.Node.Agent): sessionRedisZSet}); err != nil {
			return err
		}
		run, err := t.run(s, intent.Node.RunID)
		if err != nil {
			return err
		}
		if run == nil || run.Ref != intent.Node.Ref {
			return session.ErrFence
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil {
			return session.ErrCorrupt
		}
		if run.Status == sessionIntentPublished {
			found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, run.Ref.NodeID), &out)
			if err != nil {
				return err
			}
			if !found || sessionValidateNode(out, scope, d.id, true) != nil || !sessionSamePublication(out, intent.Node, digest, size, receipt) {
				return session.ErrCorrupt
			}
			return nil
		}
		if run.Status != sessionIntentPending {
			return session.ErrFence
		}
		storedLease, err := t.validLease(d, meta, intent.Lease)
		if err != nil {
			return err
		}
		now, err := t.tx.Time(ctx).Result()
		if err != nil {
			return err
		}
		if storedLease.Deadline <= now.UnixMilli() {
			return session.ErrFence
		}
		var stored session.Intent
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.intents, intent.Node.RunID), &stored)
		if err != nil {
			return err
		}
		if !found || sessionValidateIntent(stored, meta, intent.Node.RunID) != nil {
			return session.ErrCorrupt
		}
		if stored.Status != sessionIntentPending || stored.Node.Ref != intent.Node.Ref || stored.Lease != intent.Lease || stored.Node.Scope != intent.Node.Scope || stored.Node.Version != intent.Node.Version || stored.Node.FileName != intent.Node.FileName || stored.Node.Agent != intent.Node.Agent || !sameSessionParent(stored.Node.Parent, intent.Node.Parent) {
			return session.ErrFence
		}
		if stored.Node.Parent != nil {
			if err := t.parent(d, scope, *stored.Node.Parent); err != nil {
				return err
			}
		}
		var existing session.Node
		found, err = sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, stored.Node.Ref.NodeID), &existing)
		if err != nil {
			return err
		}
		if found {
			return session.ErrCorrupt
		}
		sequence, err := t.sequence(s)
		if err != nil {
			return err
		}
		if sequence == math.MaxInt64 {
			return fmt.Errorf("%w: commit sequence exhausted", session.ErrConflict)
		}
		out = stored.Node
		out.Digest, out.Size, out.CommitSequence = digest, size, sequence+1
		out.ReplyMessageIDs = slices.Clone(receipt.MessageIDs)
		supersedesLatest := true
		if receipt.RedirectFrom != nil {
			if stored.Node.Parent != nil {
				return session.ErrCorrupt
			}
			out.RedirectedFrom = receipt.RedirectFrom
			if out.ReplyMessageIDs, err = t.redirectableMessages(s, receipt); err != nil {
				return err
			}
			if supersedesLatest, err = t.latestIs(s.latest(out.Agent), *receipt.RedirectFrom); err != nil {
				return err
			}
		}
		stored.Status, stored.Node = sessionIntentPublished, out
		meta.LastActive = now.UnixMilli()
		t.write(sessionRedisSet, s.sequence, strconv.FormatInt(out.CommitSequence, 10))
		t.write("hset", d.nodes, out.Ref.NodeID, sessionEncode(out))
		t.write("hset", s.runs, out.RunID, sessionEncode(sessionRunIndex{Ref: out.Ref, Status: sessionIntentPublished}))
		t.write("hset", d.intents, out.RunID, sessionEncode(stored))
		t.write(sessionRedisSet, d.meta, sessionEncode(meta))
		if supersedesLatest {
			t.write("zadd", s.latest(out.Agent), 0, sessionLatestMember(out))
		}
		for _, id := range out.ReplyMessageIDs {
			t.write("hset", s.messages, strconv.Itoa(id), sessionEncode(out.Ref))
		}
		return t.ensurePending(s, d)
	})
	if err != nil {
		return session.Node{}, err
	}
	return out, nil
}

// redirectableMessages keeps only the delivered IDs whose mapping still points at the redirect source.
func (t *sessionTxn) redirectableMessages(s sessionScopeKeys, receipt session.DeliveryReceipt) ([]int, error) {
	fields := make([]string, len(receipt.MessageIDs))
	for i, id := range receipt.MessageIDs {
		fields[i] = strconv.Itoa(id)
	}
	current, err := t.indexFields(s.messages, fields)
	if err != nil {
		return nil, err
	}
	var out []int
	for i, field := range fields {
		value, ok := current[field]
		if !ok {
			continue
		}
		var ref session.NodeRef
		if json.Unmarshal([]byte(value), &ref) != nil || ref.Validate() != nil {
			return nil, session.ErrCorrupt
		}
		if ref == *receipt.RedirectFrom {
			out = append(out, receipt.MessageIDs[i])
		}
	}
	if len(out) == 0 {
		return nil, session.ErrStale
	}
	return out, nil
}

func (t *sessionTxn) latestIs(key string, ref session.NodeRef) (bool, error) {
	entries, err := t.tx.ZRevRangeWithScores(t.ctx, key, 0, 0).Result()
	if err != nil {
		return false, err
	}
	if len(entries) == 0 {
		return false, nil
	}
	if entries[0].Score != 0 {
		return false, session.ErrCorrupt
	}
	latest, _, err := sessionParseLatest(fmt.Sprint(entries[0].Member))
	if err != nil {
		return false, err
	}
	return latest == ref, nil
}

func sessionSamePublication(a, b session.Node, digest string, size int64, receipt session.DeliveryReceipt) bool {
	if receipt.RedirectFrom == nil {
		if a.RedirectedFrom != nil || !slices.Equal(a.ReplyMessageIDs, receipt.MessageIDs) {
			return false
		}
	} else if !sameSessionParent(a.RedirectedFrom, receipt.RedirectFrom) || len(a.ReplyMessageIDs) == 0 || !sessionSubsetIDs(a.ReplyMessageIDs, receipt.MessageIDs) {
		return false
	}
	return a.Scope == b.Scope && a.Ref == b.Ref && a.RunID == b.RunID && a.Agent == b.Agent && sameSessionParent(a.Parent, b.Parent) && a.Version == b.Version && a.FileName == b.FileName && a.Digest == digest && a.Size == size
}

func sessionSubsetIDs(subset, all []int) bool {
	for _, id := range subset {
		if !slices.Contains(all, id) {
			return false
		}
	}
	return true
}

// GetPublication resolves publication using a WATCH-validated read-only snapshot.
func (r *AgentV3SessionRepository) GetPublication(ctx context.Context, scope session.Scope, runID string) (*session.Node, error) {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return nil, err
	}
	var out *session.Node
	err = r.atomic(ctx, func(t *sessionTxn) error {
		out = nil
		if err := t.check(map[string]string{s.runs: sessionRedisHash}); err != nil {
			return err
		}
		run, err := t.run(s, runID)
		if err != nil || run == nil {
			return err
		}
		d := s.dag(run.Ref.DAGID)
		if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil {
			return session.ErrCorrupt
		}
		var node session.Node
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, run.Ref.NodeID), &node)
		if err != nil {
			return err
		}
		if run.Status == sessionIntentPublished {
			if !found || sessionValidateNode(node, scope, d.id, true) != nil || node.Ref != run.Ref || node.RunID != runID {
				return session.ErrCorrupt
			}
			out = &node
			return nil
		}
		if found {
			return session.ErrCorrupt
		}
		var intent session.Intent
		found, err = sessionReadJSON(ctx, t.tx.HGet(ctx, d.intents, runID), &intent)
		if err != nil {
			return err
		}
		if !found || sessionValidateIntent(intent, meta, runID) != nil || intent.Node.Ref != run.Ref || intent.Status != run.Status {
			return session.ErrCorrupt
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// AbortIntent fences an unpublished attempt; false never authorizes compensation.
func (r *AgentV3SessionRepository) AbortIntent(ctx context.Context, scope session.Scope, intent session.Intent) (bool, error) {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return false, err
	}
	if intent.Node.Ref.Validate() != nil {
		return false, session.ErrCorrupt
	}
	d := s.dag(intent.Node.Ref.DAGID)
	var aborted bool
	err = r.atomic(ctx, func(t *sessionTxn) error {
		aborted = false
		if err := t.check(map[string]string{s.runs: sessionRedisHash, s.pending: sessionRedisSet, d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash}); err != nil {
			return err
		}
		run, err := t.run(s, intent.Node.RunID)
		if err != nil {
			return err
		}
		if run == nil || run.Ref != intent.Node.Ref {
			return session.ErrFence
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil {
			return session.ErrCorrupt
		}
		if run.Status == sessionIntentPublished {
			var node session.Node
			found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, run.Ref.NodeID), &node)
			if err != nil {
				return err
			}
			if !found || sessionValidateNode(node, scope, d.id, true) != nil || node.RunID != intent.Node.RunID || node.Ref != run.Ref {
				return session.ErrCorrupt
			}
			return nil
		}
		if meta.State != sessionDAGActive {
			return session.ErrFence
		}
		var unexpected session.Node
		present, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, run.Ref.NodeID), &unexpected)
		if err != nil {
			return err
		}
		if present {
			return session.ErrCorrupt
		}
		var stored session.Intent
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.intents, intent.Node.RunID), &stored)
		if err != nil {
			return err
		}
		if !found || sessionValidateIntent(stored, meta, intent.Node.RunID) != nil || stored.Node.Ref != run.Ref || stored.Status != run.Status {
			return session.ErrCorrupt
		}
		aborted = true
		if stored.Status != sessionIntentAborted {
			stored.Status = sessionIntentAborted
			t.write("hset", d.intents, stored.Node.RunID, sessionEncode(stored))
			t.write("hset", s.runs, stored.Node.RunID, sessionEncode(sessionRunIndex{Ref: stored.Node.Ref, Status: stored.Status}))
		}
		return t.ensurePending(s, d)
	})
	if err != nil {
		return false, err
	}
	return aborted, nil
}

// FinishIntent clears recovery metadata after publication or completed compensation.
func (r *AgentV3SessionRepository) FinishIntent(ctx context.Context, scope session.Scope, intent session.Intent) error {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return err
	}
	if intent.Node.Ref.Validate() != nil {
		return session.ErrCorrupt
	}
	d := s.dag(intent.Node.Ref.DAGID)
	return r.atomic(ctx, func(t *sessionTxn) error {
		if err := t.check(map[string]string{s.runs: sessionRedisHash, s.pending: sessionRedisSet, d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash, d.leases: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		var stored session.Intent
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.intents, intent.Node.RunID), &stored)
		if err != nil {
			return err
		}
		if !found {
			return nil
		}
		if meta == nil {
			return session.ErrCorrupt
		}
		if meta.State != sessionDAGActive {
			return session.ErrFence
		}
		if sessionValidateIntent(stored, meta, intent.Node.RunID) != nil || stored.Node.Ref != intent.Node.Ref {
			return session.ErrCorrupt
		}
		if stored.Status == sessionIntentPending {
			return session.ErrFence
		}
		run, err := t.run(s, stored.Node.RunID)
		if err != nil {
			return err
		}
		if run == nil || run.Ref != stored.Node.Ref || run.Status != stored.Status {
			return session.ErrCorrupt
		}
		var node session.Node
		found, err = sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, stored.Node.Ref.NodeID), &node)
		if err != nil {
			return err
		}
		if stored.Status == sessionIntentPublished {
			if !found || sessionValidateNode(node, scope, d.id, true) != nil || node.CommitSequence != stored.Node.CommitSequence || !sessionSamePublication(node, stored.Node, stored.Node.Digest, stored.Node.Size, session.DeliveryReceipt{MessageIDs: stored.Node.ReplyMessageIDs, RedirectFrom: stored.Node.RedirectedFrom}) {
				return session.ErrCorrupt
			}
		} else if found {
			return session.ErrCorrupt
		}
		t.write("hdel", d.intents, stored.Node.RunID)
		count, err := t.tx.HLen(ctx, d.intents).Result()
		if err != nil {
			return err
		}
		if count == 1 {
			t.write("srem", s.pending, d.id)
		} else if err := t.ensurePending(s, d); err != nil {
			return err
		}
		if stored.Status == sessionIntentAborted {
			t.write("hdel", s.runs, stored.Node.RunID)
		}
		if stored.Node.Parent == nil {
			var lease session.Lease
			found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.leases, stored.Lease.Token), &lease)
			if err != nil {
				return err
			}
			if found {
				if sessionValidateLease(lease, meta) != nil || lease.Token != stored.Lease.Token {
					return session.ErrCorrupt
				}
				t.write("hdel", d.leases, lease.Token)
			}
		}
		return nil
	})
}

// Scopes returns healthy catalog entries and safe per-field corruption diagnostics.
func (r *AgentV3SessionRepository) Scopes(ctx context.Context) ([]session.Scope, error) {
	entries, err := r.client.HGetAll(ctx, r.base+"scopes").Result()
	if err != nil {
		return nil, sessionRedisError(err)
	}
	out := make([]session.Scope, 0, len(entries))
	var failures []error
	for key, entry := range entries {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var scope session.Scope
		if json.Unmarshal([]byte(entry), &scope) != nil || scope.Validate() != nil || scope.Namespace != r.namespace || scope.Key() != key {
			failures = append(failures, fmt.Errorf("%w: catalog field %q", session.ErrCorrupt, key))
			continue
		}
		out = append(out, scope)
	}
	return out, errors.Join(failures...)
}
