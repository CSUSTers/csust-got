package orm

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

const sessionRedisLayout = 2

const (
	sessionRedisHash = "hash"
	sessionRedisZSet = "zset"
	sessionRedisSet  = "set"
	sessionRedisNone = "none"
)

type sessionMeta struct {
	Layout     int           `json:"layout"`
	Scope      session.Scope `json:"scope"`
	ID         string        `json:"id"`
	Generation string        `json:"generation"`
	State      string        `json:"state"`
	LastActive int64         `json:"last_active"`
}

type sessionRunIndex struct {
	Ref    session.NodeRef `json:"ref"`
	Status string          `json:"status"`
}
type sessionScopeKeys struct{ prefix, dags, sequence, runs, messages, pending, deleting string }
type sessionDAGKeys struct{ id, meta, nodes, leases, intents, rejectedContexts string }

func (r *AgentV3SessionRepository) scopeKeys(scope session.Scope) (sessionScopeKeys, error) {
	if err := scope.Validate(); err != nil {
		return sessionScopeKeys{}, err
	}
	if scope.Namespace != r.namespace {
		return sessionScopeKeys{}, session.ErrCorrupt
	}
	p := r.base + "scope:" + scope.Key() + ":"
	return sessionScopeKeys{prefix: p, dags: p + "dags", sequence: p + "sequence", runs: p + "runs", messages: p + "messages", pending: p + "pending_dags", deleting: p + "deleting_dags"}, nil
}
func (s sessionScopeKeys) dag(id string) sessionDAGKeys {
	p := s.prefix + "dag:" + id + ":"
	return sessionDAGKeys{id: id, meta: p + "meta", nodes: p + "nodes", leases: p + "leases", intents: p + "intents", rejectedContexts: p + "rejected_contexts"}
}
func (s sessionScopeKeys) latest(agent string) string {
	return s.prefix + "latest:" + hex.EncodeToString([]byte(agent))
}
func sessionLatestMember(n session.Node) string {
	return fmt.Sprintf("%019d:%s:%s", n.CommitSequence, n.Ref.DAGID, n.Ref.NodeID)
}
func sessionParseLatest(member string) (session.NodeRef, int64, error) {
	if len(member) != 85 || member[19] != ':' || member[52] != ':' {
		return session.NodeRef{}, 0, session.ErrCorrupt
	}
	sequence, err := strconv.ParseInt(member[:19], 10, 64)
	ref := session.NodeRef{DAGID: member[20:52], NodeID: member[53:]}
	if err != nil || sequence <= 0 || ref.Validate() != nil || fmt.Sprintf("%019d", sequence) != member[:19] {
		return session.NodeRef{}, 0, session.ErrCorrupt
	}
	return ref, sequence, nil
}

type sessionTxn struct {
	tx     *redis.Tx
	ctx    context.Context
	types  map[string]string
	writes [][]any
}

func (r *AgentV3SessionRepository) atomic(ctx context.Context, fn func(*sessionTxn) error) error {
	for range 32 {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := r.client.Watch(ctx, func(tx *redis.Tx) error {
			t := &sessionTxn{tx: tx, ctx: ctx, types: map[string]string{}}
			if err := fn(t); err != nil {
				return sessionRedisError(err)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			_, err := tx.TxPipelined(ctx, func(p redis.Pipeliner) error {
				if len(t.writes) == 0 {
					p.Ping(ctx)
				}
				for _, args := range t.writes {
					if err := ctx.Err(); err != nil {
						return err
					}
					p.Do(ctx, args...)
				}
				return nil
			})
			return err
		})
		if !errors.Is(err, redis.TxFailedErr) {
			return err
		}
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return session.ErrConflict
}

func sessionRedisError(err error) error {
	if err != nil && strings.HasPrefix(err.Error(), "WRONGTYPE") {
		return fmt.Errorf("%w: Redis key type", session.ErrCorrupt)
	}
	return err
}

func sessionPureCorruption(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		causes := joined.Unwrap()
		if len(causes) == 0 {
			return false
		}
		for _, cause := range causes {
			if !sessionPureCorruption(cause) {
				return false
			}
		}
		return true
	}
	if cause := errors.Unwrap(err); cause != nil {
		return sessionPureCorruption(cause)
	}
	return errors.Is(err, session.ErrCorrupt)
}

func (t *sessionTxn) check(keys map[string]string) error {
	var fresh []string
	for key := range keys {
		if _, ok := t.types[key]; !ok {
			fresh = append(fresh, key)
		}
	}
	if len(fresh) == 0 {
		return nil
	}
	if err := t.tx.Watch(t.ctx, fresh...).Err(); err != nil {
		return err
	}
	commands := map[string]*redis.StatusCmd{}
	_, err := t.tx.Pipelined(t.ctx, func(p redis.Pipeliner) error {
		for _, key := range fresh {
			commands[key] = p.Type(t.ctx, key)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for key, cmd := range commands {
		kind := cmd.Val()
		if kind != sessionRedisNone && kind != keys[key] {
			return fmt.Errorf("%w: key type %q", session.ErrCorrupt, key)
		}
		t.types[key] = kind
	}
	return nil
}

func (t *sessionTxn) write(command string, args ...any) {
	t.writes = append(t.writes, append([]any{command}, args...))
}
func sessionEncode(value any) string { b, _ := json.Marshal(value); return string(b) }
func sessionReadJSON(ctx context.Context, cmd *redis.StringCmd, value any) (bool, error) {
	if err := ctx.Err(); err != nil {
		return false, err
	}
	data, err := cmd.Bytes()
	if errors.Is(err, redis.Nil) {
		return false, nil
	}
	if err != nil {
		return false, sessionRedisError(err)
	}
	if json.Unmarshal(data, value) != nil {
		return false, session.ErrCorrupt
	}
	return true, nil
}

func (t *sessionTxn) meta(d sessionDAGKeys, scope session.Scope) (*sessionMeta, error) {
	return sessionReadMeta(t.ctx, t.tx.Get(t.ctx, d.meta), d, scope)
}
func sessionReadMeta(ctx context.Context, cmd *redis.StringCmd, d sessionDAGKeys, scope session.Scope) (*sessionMeta, error) {
	var meta sessionMeta
	found, err := sessionReadJSON(ctx, cmd, &meta)
	if err != nil || !found {
		return nil, err
	}
	if meta.Layout != sessionRedisLayout || meta.Scope != scope || meta.ID != d.id || !session.ValidID(meta.ID) || !session.ValidID(meta.Generation) || meta.State != sessionDAGActive && meta.State != sessionDAGDeleting {
		return nil, session.ErrCorrupt
	}
	return &meta, nil
}
func (t *sessionTxn) run(s sessionScopeKeys, id string) (*sessionRunIndex, error) {
	var run sessionRunIndex
	found, err := sessionReadJSON(t.ctx, t.tx.HGet(t.ctx, s.runs, id), &run)
	if err != nil || !found {
		return nil, err
	}
	if !session.ValidID(id) || run.Ref.Validate() != nil || !sessionStatus(run.Status) {
		return nil, session.ErrCorrupt
	}
	return &run, nil
}
func (t *sessionTxn) catalog(key string, scope session.Scope) (bool, error) {
	var stored session.Scope
	found, err := sessionReadJSON(t.ctx, t.tx.HGet(t.ctx, key, scope.Key()), &stored)
	if err != nil {
		return false, err
	}
	if found && (stored.Validate() != nil || stored != scope || stored.Key() != scope.Key()) {
		return false, session.ErrCorrupt
	}
	return found, nil
}
func (t *sessionTxn) sequence(s sessionScopeKeys) (int64, error) {
	value, err := t.tx.Get(t.ctx, s.sequence).Result()
	if errors.Is(err, redis.Nil) {
		return 0, fmt.Errorf("%w: missing commit sequence", session.ErrCorrupt)
	}
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(value, 10, 64)
	if err != nil || n < 0 || strconv.FormatInt(n, 10) != value {
		return 0, session.ErrCorrupt
	}
	return n, nil
}
func sessionStatus(status string) bool {
	return status == sessionIntentPending || status == sessionIntentPublished || status == sessionIntentAborted
}
func sessionDigest(value string) bool {
	if len(value) != 64 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil
}
func sessionValidateNode(n session.Node, scope session.Scope, dagID string, committed bool) error {
	if n.Scope != scope || n.Ref.Validate() != nil || n.Ref.DAGID != dagID || n.Version != session.Version || n.Agent == "" || !session.ValidID(n.RunID) || n.FileName != n.Ref.NodeID+".jsonl" || n.Parent != nil && (n.Parent.Validate() != nil || n.Parent.DAGID != dagID) {
		return session.ErrCorrupt
	}
	if n.MemoryEpoch < 0 || n.RedirectedFrom != nil && (n.Parent != nil || n.RedirectedFrom.Validate() != nil) {
		return session.ErrCorrupt
	}
	if committed {
		if !sessionDigest(n.Digest) || n.Size <= 0 || n.CommitSequence <= 0 || len(n.ReplyMessageIDs) == 0 {
			return session.ErrCorrupt
		}
		for _, id := range n.ReplyMessageIDs {
			if id <= 0 {
				return session.ErrCorrupt
			}
		}
	}
	return nil
}
func sessionValidateLease(l session.Lease, m *sessionMeta) error {
	if m == nil || l.DAGID != m.ID || l.Generation != m.Generation || !session.ValidID(l.Token) || l.Deadline <= 0 {
		return session.ErrCorrupt
	}
	return nil
}
func sessionValidateIntent(i session.Intent, m *sessionMeta, runID string) error {
	if m == nil || i.Node.RunID != runID || !sessionStatus(i.Status) || sessionValidateNode(i.Node, m.Scope, m.ID, i.Status == sessionIntentPublished) != nil || sessionValidateLease(i.Lease, m) != nil {
		return session.ErrCorrupt
	}
	return nil
}
func (t *sessionTxn) validLease(d sessionDAGKeys, m *sessionMeta, proof session.Lease) (session.Lease, error) {
	if m == nil || m.State != sessionDAGActive || proof.DAGID != m.ID || proof.Generation != m.Generation {
		return session.Lease{}, session.ErrFence
	}
	var stored session.Lease
	found, err := sessionReadJSON(t.ctx, t.tx.HGet(t.ctx, d.leases, proof.Token), &stored)
	if err != nil {
		return stored, err
	}
	if !found {
		return stored, session.ErrFence
	}
	if sessionValidateLease(stored, m) != nil || stored.Token != proof.Token {
		return stored, session.ErrCorrupt
	}
	return stored, nil
}
func (t *sessionTxn) parent(d sessionDAGKeys, scope session.Scope, ref session.NodeRef) error {
	var n session.Node
	found, err := sessionReadJSON(t.ctx, t.tx.HGet(t.ctx, d.nodes, ref.NodeID), &n)
	if err != nil {
		return err
	}
	if !found {
		return session.ErrFence
	}
	if sessionValidateNode(n, scope, d.id, true) != nil || n.Ref != ref {
		return session.ErrCorrupt
	}
	return nil
}
func sessionDecodeNodes(ctx context.Context, raw map[string]string, scope session.Scope, dagID string) (map[string]session.Node, error) {
	nodes := make(map[string]session.Node, len(raw))
	for id, value := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var n session.Node
		if json.Unmarshal([]byte(value), &n) != nil || n.Ref.NodeID != id || sessionValidateNode(n, scope, dagID, true) != nil {
			return nil, session.ErrCorrupt
		}
		nodes[id] = n
	}
	return nodes, nil
}
