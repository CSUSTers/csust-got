package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

func (r *AgentV3SessionRepository) sessionDAGs(ctx context.Context, s sessionScopeKeys, scope session.Scope) ([]string, map[string]*sessionMeta, map[string]error, int64, error) {
	var ids []string
	err := r.atomic(ctx, func(t *sessionTxn) error {
		ids = nil
		if err := t.check(map[string]string{s.dags: sessionRedisSet}); err != nil {
			return err
		}
		var err error
		ids, err = t.tx.SMembers(ctx, s.dags).Result()
		return err
	})
	if err != nil {
		return nil, nil, nil, 0, err
	}
	commands := map[string]*redis.StringCmd{}
	var clock *redis.TimeCmd
	_, pipeErr := r.client.Pipelined(ctx, func(p redis.Pipeliner) error {
		clock = p.Time(ctx)
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			if session.ValidID(id) {
				commands[id] = p.Get(ctx, s.dag(id).meta)
			}
		}
		return nil
	})
	var now int64
	if clock != nil {
		value, err := clock.Result()
		if err != nil {
			return nil, nil, nil, 0, err
		}
		now = value.UnixMilli()
	}
	metas := map[string]*sessionMeta{}
	failures := map[string]error{}
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, nil, nil, 0, err
		}
		if !session.ValidID(id) {
			failures[id] = session.ErrCorrupt
			continue
		}
		meta, err := sessionReadMeta(ctx, commands[id], s.dag(id), scope)
		if err != nil && !sessionPureCorruption(err) {
			return nil, nil, nil, 0, err
		}
		if err != nil || meta == nil {
			failures[id] = session.ErrCorrupt
		} else {
			metas[id] = meta
		}
	}
	if pipeErr != nil && !errors.Is(pipeErr, redis.Nil) && !sessionPureCorruption(sessionRedisError(pipeErr)) {
		return nil, nil, nil, 0, pipeErr
	}
	return ids, metas, failures, now, nil
}

const sessionLastCollectionKey = "last_collection"

// LastCollection returns the stored local day of the last completed collection, or "" when none.
func (r *AgentV3SessionRepository) LastCollection(ctx context.Context) (string, error) {
	value, err := r.client.Get(ctx, r.base+sessionLastCollectionKey).Result()
	if errors.Is(err, redis.Nil) {
		return "", nil
	}
	if err != nil {
		return "", sessionRedisError(err)
	}
	return value, nil
}

// MarkCollection stores the local day of a completed collection in the layout's global area.
func (r *AgentV3SessionRepository) MarkCollection(ctx context.Context, day string) error {
	if strings.TrimSpace(day) == "" {
		return session.ErrCorrupt
	}
	if err := r.client.Set(ctx, r.base+sessionLastCollectionKey, day, 0).Err(); err != nil {
		return sessionRedisError(err)
	}
	return nil
}

var _ session.CollectionMarker = (*AgentV3SessionRepository)(nil)

func sessionDAGError(scope session.Scope, id string, err error) error {
	return fmt.Errorf("%w: scope %q DAG %q", err, scope.Key(), id)
}

func (t *sessionTxn) member(s sessionScopeKeys, d sessionDAGKeys) (bool, error) {
	return t.tx.SIsMember(t.ctx, s.dags, d.id).Result()
}

func (t *sessionTxn) intents(d sessionDAGKeys, meta *sessionMeta) ([]session.Intent, error) {
	raw, err := t.tx.HGetAll(t.ctx, d.intents).Result()
	if err != nil {
		return nil, sessionRedisError(err)
	}
	return sessionDecodeIntents(t.ctx, raw, meta)
}

func sessionDecodeIntents(ctx context.Context, raw map[string]string, meta *sessionMeta) ([]session.Intent, error) {
	intents := make([]session.Intent, 0, len(raw))
	for run, value := range raw {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		var intent session.Intent
		if json.Unmarshal([]byte(value), &intent) != nil || sessionValidateIntent(intent, meta, run) != nil {
			return nil, session.ErrCorrupt
		}
		intents = append(intents, intent)
	}
	return intents, nil
}

func (t *sessionTxn) manifest(d sessionDAGKeys, meta *sessionMeta) (session.Deletion, error) {
	var nodesCmd *redis.MapStringStringCmd
	var leasesCmd *redis.MapStringStringCmd
	var rejectedCmd *redis.MapStringStringCmd
	_, err := t.tx.Pipelined(t.ctx, func(p redis.Pipeliner) error {
		nodesCmd = p.HGetAll(t.ctx, d.nodes)
		leasesCmd = p.HGetAll(t.ctx, d.leases)
		rejectedCmd = p.HGetAll(t.ctx, d.rejectedContexts)
		return nil
	})
	if err != nil {
		return session.Deletion{}, sessionRedisError(err)
	}
	nodes, err := sessionDecodeNodes(t.ctx, nodesCmd.Val(), meta.Scope, d.id)
	if err != nil {
		return session.Deletion{}, err
	}
	if err := sessionValidateRejectedContexts(t.ctx, rejectedCmd.Val(), nodes); err != nil {
		return session.Deletion{}, err
	}
	for token, value := range leasesCmd.Val() {
		if err := t.ctx.Err(); err != nil {
			return session.Deletion{}, err
		}
		var l session.Lease
		if json.Unmarshal([]byte(value), &l) != nil || l.Token != token || sessionValidateLease(l, meta) != nil {
			return session.Deletion{}, session.ErrCorrupt
		}
	}
	intents, err := t.intents(d, meta)
	if err != nil {
		return session.Deletion{}, err
	}
	manifest := session.Deletion{DAGID: d.id, Generation: meta.Generation, Intents: intents}
	for _, node := range nodes {
		if err := t.ctx.Err(); err != nil {
			return session.Deletion{}, err
		}
		if node.Parent != nil {
			if _, ok := nodes[node.Parent.NodeID]; !ok {
				return session.Deletion{}, session.ErrCorrupt
			}
		}
		manifest.Nodes = append(manifest.Nodes, node)
	}
	for _, intent := range intents {
		if err := t.ctx.Err(); err != nil {
			return session.Deletion{}, err
		}
		node, published := nodes[intent.Node.Ref.NodeID]
		if intent.Status == sessionIntentPublished {
			if !published || node.CommitSequence != intent.Node.CommitSequence || !sessionSamePublication(node, intent.Node, intent.Node.Digest, intent.Node.Size, session.DeliveryReceipt{MessageIDs: intent.Node.ReplyMessageIDs}) {
				return session.Deletion{}, session.ErrCorrupt
			}
		} else if published {
			return session.Deletion{}, session.ErrCorrupt
		}
		if intent.Node.Parent != nil {
			if _, ok := nodes[intent.Node.Parent.NodeID]; !ok {
				return session.Deletion{}, session.ErrCorrupt
			}
		}
	}
	return manifest, nil
}

// Pending returns healthy active intents and isolates per-DAG corruption.
func (r *AgentV3SessionRepository) Pending(ctx context.Context, scope session.Scope) ([]session.Intent, error) {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return nil, err
	}
	ids, err := r.maintenanceIDs(ctx, s.pending)
	if err != nil {
		return nil, sessionMaintenanceFailure(err)
	}
	var out []session.Intent
	var failures []error
	var candidates []string
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !session.ValidID(id) {
			failures = append(failures, sessionDAGError(scope, id, session.ErrCorrupt))
			continue
		}
		candidates = append(candidates, id)
	}
	for start := 0; start < len(candidates); start += sessionPendingBatchSize {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		current, corrupt, err := r.pendingBatch(ctx, s, scope, candidates[start:min(start+sessionPendingBatchSize, len(candidates))])
		if err != nil {
			return nil, err
		}
		out = append(out, current...)
		failures = append(failures, corrupt...)
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, errors.Join(failures...)
}

// Deleting reads frozen tombstones without claiming idle active DAGs.
func (r *AgentV3SessionRepository) Deleting(ctx context.Context, scope session.Scope) ([]session.Deletion, error) {
	return r.indexedDeletions(ctx, scope)
}

// ClaimDeleting freezes eligible DAGs while preserving pinned and corrupt DAGs.
func (r *AgentV3SessionRepository) ClaimDeleting(ctx context.Context, scope session.Scope, ttl time.Duration) ([]session.Deletion, error) {
	if ttl <= 0 {
		return nil, session.ErrCorrupt
	}
	return r.deletions(ctx, scope, ttl)
}
func (r *AgentV3SessionRepository) deletions(ctx context.Context, scope session.Scope, ttl time.Duration) ([]session.Deletion, error) {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return nil, err
	}
	ids, metas, bad, now, err := r.sessionDAGs(ctx, s, scope)
	if err != nil {
		return nil, sessionMaintenanceFailure(err)
	}
	var out []session.Deletion
	var failures []error
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if bad[id] != nil {
			failures = append(failures, sessionDAGError(scope, id, bad[id]))
			continue
		}
		// Detached discovery can only defer non-candidates. Actual claims still
		// reread metadata, leases and server TIME under the original WATCH guards.
		if metas[id].State == sessionDAGActive && metas[id].LastActive > now-ttl.Milliseconds() {
			continue
		}
		d := s.dag(id)
		var current *session.Deletion
		err = r.atomic(ctx, func(t *sessionTxn) error {
			current = nil
			if err := t.check(map[string]string{s.dags: sessionRedisSet, s.pending: sessionRedisSet, s.deleting: sessionRedisSet, d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash, d.leases: sessionRedisHash, d.rejectedContexts: sessionRedisHash}); err != nil {
				return err
			}
			member, err := t.member(s, d)
			if err != nil || !member {
				return err
			}
			meta, err := t.meta(d, scope)
			if err != nil {
				return err
			}
			if meta == nil {
				return session.ErrCorrupt
			}
			if meta.State == sessionDAGActive {
				now, err := t.tx.Time(ctx).Result()
				if err != nil {
					return err
				}
				if meta.LastActive > now.UnixMilli()-ttl.Milliseconds() {
					return nil
				}
				leases, err := t.tx.HGetAll(ctx, d.leases).Result()
				if err != nil {
					return err
				}
				live := false
				for token, value := range leases {
					if err := ctx.Err(); err != nil {
						return err
					}
					var lease session.Lease
					if json.Unmarshal([]byte(value), &lease) != nil || lease.Token != token || sessionValidateLease(lease, meta) != nil {
						return session.ErrCorrupt
					}
					if lease.Deadline > now.UnixMilli() {
						live = true
					} else {
						t.write("hdel", d.leases, token)
					}
				}
				if live {
					return nil
				}
			}
			manifest, err := t.manifest(d, meta)
			if err != nil {
				return err
			}
			if meta.State == sessionDAGActive {
				keys := map[string]string{}
				for _, n := range manifest.Nodes {
					if err := ctx.Err(); err != nil {
						return err
					}
					keys[s.latest(n.Agent)] = sessionRedisZSet
				}
				if err := t.check(keys); err != nil {
					return err
				}
				scores := make([]*redis.FloatCmd, 0, len(manifest.Nodes))
				_, err := t.tx.Pipelined(ctx, func(p redis.Pipeliner) error {
					for _, n := range manifest.Nodes {
						if err := ctx.Err(); err != nil {
							return err
						}
						scores = append(scores, p.ZScore(ctx, s.latest(n.Agent), sessionLatestMember(n)))
					}
					return nil
				})
				if err != nil && !errors.Is(err, redis.Nil) {
					return err
				}
				for _, score := range scores {
					value, err := score.Result()
					if err != nil && !errors.Is(err, redis.Nil) {
						return err
					}
					if err == nil && value != 0 {
						return session.ErrCorrupt
					}
				}
				meta.State = sessionDAGDeleting
				t.write(sessionRedisSet, d.meta, sessionEncode(meta))
				for _, n := range manifest.Nodes {
					t.write("zrem", s.latest(n.Agent), sessionLatestMember(n))
				}
			}
			current = &manifest
			return t.handoffDeleting(s, d)
		})
		if err != nil {
			if !sessionPureCorruption(err) {
				return nil, sessionMaintenanceFailure(err)
			}
			failures = append(failures, sessionDAGError(scope, id, err))
			continue
		}
		if current != nil {
			out = append(out, *current)
		}
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return out, errors.Join(failures...)
}

// FinishDelete compare-deletes verified references and conditionally removes the catalog field.
func (r *AgentV3SessionRepository) FinishDelete(ctx context.Context, scope session.Scope, deletion session.Deletion) error {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return err
	}
	if !session.ValidID(deletion.DAGID) {
		return session.ErrCorrupt
	}
	d := s.dag(deletion.DAGID)
	return r.atomic(ctx, func(t *sessionTxn) error {
		if err := t.check(map[string]string{s.dags: sessionRedisSet, s.pending: sessionRedisSet, s.deleting: sessionRedisSet, s.runs: sessionRedisHash, s.messages: sessionRedisHash, d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash, d.leases: sessionRedisHash, d.rejectedContexts: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil {
			return nil
		}
		if meta.State != sessionDAGDeleting || meta.Generation != deletion.Generation {
			return session.ErrFence
		}
		member, err := t.member(s, d)
		if err != nil {
			return err
		}
		if !member {
			return session.ErrCorrupt
		}
		manifest, err := t.manifest(d, meta)
		if err != nil {
			return err
		}
		count, err := t.tx.SCard(ctx, s.dags).Result()
		if err != nil {
			return err
		}
		latest := map[string]string{}
		runOwners := map[string]sessionRunIndex{}
		messageOwners, err := sessionManifestMessageOwners(ctx, manifest.Nodes)
		if err != nil {
			return err
		}
		for _, i := range manifest.Intents {
			if err := ctx.Err(); err != nil {
				return err
			}
			runOwners[i.Node.RunID] = sessionRunIndex{Ref: i.Node.Ref, Status: i.Status}
		}
		for _, n := range manifest.Nodes {
			if err := ctx.Err(); err != nil {
				return err
			}
			runOwners[n.RunID] = sessionRunIndex{Ref: n.Ref, Status: sessionIntentPublished}
			latest[s.latest(n.Agent)] = sessionRedisZSet
		}
		if err := t.check(latest); err != nil {
			return err
		}
		var runs, messages map[string]string
		if count == 1 {
			runs, messages, err = t.finalScopeIndexes(s, scope, r.base+"scopes", manifest, runOwners, messageOwners, latest)
		} else {
			runIDs := make([]string, 0, len(runOwners))
			for id := range runOwners {
				runIDs = append(runIDs, id)
			}
			messageIDs := make([]string, 0, len(messageOwners))
			for id := range messageOwners {
				messageIDs = append(messageIDs, id)
			}
			runs, err = t.indexFields(s.runs, runIDs)
			if err != nil {
				return err
			}
			messages, err = t.indexFields(s.messages, messageIDs)
		}
		if err != nil {
			return err
		}
		for id, value := range runs {
			if err := ctx.Err(); err != nil {
				return err
			}
			var run sessionRunIndex
			if json.Unmarshal([]byte(value), &run) != nil || run.Ref.Validate() != nil || !sessionStatus(run.Status) {
				return session.ErrCorrupt
			}
			owner := runOwners[id]
			if run.Ref == owner.Ref {
				if run.Status != owner.Status {
					return session.ErrCorrupt
				}
				t.write("hdel", s.runs, id)
			} else if count == 1 {
				return session.ErrCorrupt
			}
		}
		for id, value := range messages {
			if err := ctx.Err(); err != nil {
				return err
			}
			var ref session.NodeRef
			if json.Unmarshal([]byte(value), &ref) != nil || ref.Validate() != nil {
				return session.ErrCorrupt
			}
			if messageOwners.owns(id, ref) {
				t.write("hdel", s.messages, id)
			} else if count == 1 {
				return session.ErrCorrupt
			}
		}
		for _, n := range manifest.Nodes {
			t.write("zrem", s.latest(n.Agent), sessionLatestMember(n))
		}
		t.write("del", d.meta, d.nodes, d.intents, d.leases, d.rejectedContexts)
		t.write("srem", s.dags, d.id)
		t.write("srem", s.pending, d.id)
		t.write("srem", s.deleting, d.id)
		if count == 1 {
			t.write("del", s.runs, s.messages, s.sequence, s.pending, s.deleting)
			t.write("hdel", r.base+"scopes", scope.Key())
		}
		return nil
	})
}

func (t *sessionTxn) indexFields(key string, fields []string) (map[string]string, error) {
	out := map[string]string{}
	if len(fields) == 0 {
		return out, nil
	}
	values, err := t.tx.HMGet(t.ctx, key, fields...).Result()
	if err != nil {
		return nil, err
	}
	for i, value := range values {
		if err := t.ctx.Err(); err != nil {
			return nil, err
		}
		if value != nil {
			out[fields[i]] = value.(string)
		}
	}
	return out, nil
}

func (t *sessionTxn) finalScopeIndexes(s sessionScopeKeys, scope session.Scope, catalog string, manifest session.Deletion, runOwners map[string]sessionRunIndex, messageOwners sessionMessageOwners, latest map[string]string) (map[string]string, map[string]string, error) {
	if err := t.check(map[string]string{catalog: sessionRedisHash, s.sequence: "string"}); err != nil {
		return nil, nil, err
	}
	if _, err := t.catalog(catalog, scope); err != nil {
		return nil, nil, err
	}
	if err := t.finalMaintenanceIndexes(s, manifest.DAGID); err != nil {
		return nil, nil, err
	}
	if _, err := t.sequence(s); err != nil {
		return nil, nil, err
	}
	runs, err := t.tx.HGetAll(t.ctx, s.runs).Result()
	if err != nil {
		return nil, nil, err
	}
	messages, err := t.tx.HGetAll(t.ctx, s.messages).Result()
	if err != nil {
		return nil, nil, err
	}
	for id := range runs {
		if err := t.ctx.Err(); err != nil {
			return nil, nil, err
		}
		if _, ok := runOwners[id]; !ok {
			return nil, nil, session.ErrCorrupt
		}
	}
	for id := range messages {
		if err := t.ctx.Err(); err != nil {
			return nil, nil, err
		}
		if _, ok := messageOwners[id]; !ok {
			return nil, nil, session.ErrCorrupt
		}
	}
	for key := range latest {
		if err := t.finalLatest(s, manifest, key); err != nil {
			return nil, nil, err
		}
	}
	return runs, messages, nil
}

type sessionMessageOwners map[string]map[session.NodeRef]struct{}

func sessionManifestMessageOwners(ctx context.Context, nodes []session.Node) (sessionMessageOwners, error) {
	owners := sessionMessageOwners{}
	for _, node := range nodes {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		for _, id := range node.ReplyMessageIDs {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			field := strconv.Itoa(id)
			if owners[field] == nil {
				owners[field] = map[session.NodeRef]struct{}{}
			}
			owners[field][node.Ref] = struct{}{}
		}
	}
	return owners, nil
}

func (owners sessionMessageOwners) owns(id string, ref session.NodeRef) bool {
	_, ok := owners[id][ref]
	return ok
}

func (t *sessionTxn) finalLatest(s sessionScopeKeys, manifest session.Deletion, key string) error {
	entries, err := t.tx.ZRangeWithScores(t.ctx, key, 0, -1).Result()
	if err != nil {
		return err
	}
	owned := map[string]bool{}
	for _, n := range manifest.Nodes {
		if err := t.ctx.Err(); err != nil {
			return err
		}
		if s.latest(n.Agent) == key {
			owned[sessionLatestMember(n)] = true
		}
	}
	for _, entry := range entries {
		if err := t.ctx.Err(); err != nil {
			return err
		}
		if entry.Score != 0 || !owned[fmt.Sprint(entry.Member)] {
			return session.ErrCorrupt
		}
	}
	return nil
}
