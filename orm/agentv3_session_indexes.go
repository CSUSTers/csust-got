package orm

import (
	"context"
	"errors"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

func (t *sessionTxn) ensurePending(s sessionScopeKeys, d sessionDAGKeys) error {
	member, err := t.tx.SIsMember(t.ctx, s.pending, d.id).Result()
	if err != nil {
		return err
	}
	if !member {
		t.write("sadd", s.pending, d.id)
	}
	return nil
}

func (t *sessionTxn) handoffDeleting(s sessionScopeKeys, d sessionDAGKeys) error {
	var pending, deleting *redis.BoolCmd
	_, err := t.tx.Pipelined(t.ctx, func(p redis.Pipeliner) error {
		pending = p.SIsMember(t.ctx, s.pending, d.id)
		deleting = p.SIsMember(t.ctx, s.deleting, d.id)
		return nil
	})
	if err != nil {
		return err
	}
	if pending.Val() {
		t.write("srem", s.pending, d.id)
	}
	if !deleting.Val() {
		t.write("sadd", s.deleting, d.id)
	}
	return nil
}

func (r *AgentV3SessionRepository) maintenanceIDs(ctx context.Context, index string) ([]string, error) {
	var ids []string
	err := r.atomic(ctx, func(t *sessionTxn) error {
		ids = nil
		if err := t.check(map[string]string{index: sessionRedisSet}); err != nil {
			return err
		}
		var err error
		ids, err = t.tx.SMembers(ctx, index).Result()
		return err
	})
	if err != nil {
		return nil, sessionMaintenanceFailure(err)
	}
	return ids, nil
}

func (r *AgentV3SessionRepository) deletingBatch(ctx context.Context, s sessionScopeKeys, scope session.Scope, ids []string) ([]session.Deletion, []error, error) {
	var out []session.Deletion
	var failures []error
	err := r.atomic(ctx, func(t *sessionTxn) error {
		out, failures = nil, nil
		// Per-DAG wrong types must not prevent healthy manifests in this batch.
		if err := t.check(map[string]string{s.dags: sessionRedisSet, s.deleting: sessionRedisSet}); err != nil {
			return err
		}
		for _, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			indexed, err := t.tx.SIsMember(ctx, s.deleting, id).Result()
			if err != nil {
				return err
			}
			if !indexed {
				continue
			}
			d := s.dag(id)
			current, err := t.indexedDeletion(s, d, scope)
			if err != nil {
				if !sessionPureCorruption(err) {
					return err
				}
				failures = append(failures, sessionDAGError(scope, id, err))
				continue
			}
			out = append(out, current)
		}
		return nil
	})
	if err != nil {
		return nil, nil, sessionMaintenanceFailure(err)
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	return out, failures, nil
}

func (t *sessionTxn) indexedDeletion(s sessionScopeKeys, d sessionDAGKeys, scope session.Scope) (session.Deletion, error) {
	if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash, d.intents: sessionRedisHash, d.leases: sessionRedisHash, d.rejectedContexts: sessionRedisHash}); err != nil {
		return session.Deletion{}, err
	}
	member, err := t.member(s, d)
	if err != nil {
		return session.Deletion{}, err
	}
	if !member {
		return session.Deletion{}, session.ErrCorrupt
	}
	meta, err := t.meta(d, scope)
	if err != nil {
		return session.Deletion{}, err
	}
	if meta == nil || meta.State != sessionDAGDeleting {
		return session.Deletion{}, session.ErrCorrupt
	}
	return t.manifest(d, meta)
}

func (t *sessionTxn) finalMaintenanceIndexes(s sessionScopeKeys, dagID string) error {
	for _, key := range []string{s.pending, s.deleting} {
		ids, err := t.tx.SMembers(t.ctx, key).Result()
		if err != nil {
			return err
		}
		for _, id := range ids {
			if err := t.ctx.Err(); err != nil {
				return err
			}
			if id != dagID {
				return session.ErrCorrupt
			}
		}
	}
	return nil
}

func (r *AgentV3SessionRepository) indexedDeletions(ctx context.Context, scope session.Scope) ([]session.Deletion, error) {
	s, err := r.scopeKeys(scope)
	if err != nil {
		return nil, err
	}
	ids, err := r.maintenanceIDs(ctx, s.deleting)
	if err != nil {
		return nil, err
	}
	var out []session.Deletion
	var failures []error
	var candidates []string
	for _, id := range ids {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if !session.ValidID(id) {
			failures = append(failures, sessionDAGError(scope, id, session.ErrCorrupt))
		} else {
			candidates = append(candidates, id)
		}
	}
	for start := 0; start < len(candidates); start += sessionPendingBatchSize {
		current, corrupt, err := r.deletingBatch(ctx, s, scope, candidates[start:min(start+sessionPendingBatchSize, len(candidates))])
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
