package orm

import (
	"context"
	"errors"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

const sessionPendingBatchSize = 64

type sessionPendingRead struct {
	dag                   sessionDAGKeys
	metaType, intentsType *redis.StatusCmd
	member                *redis.BoolCmd
	meta                  *redis.StringCmd
	intents               *redis.MapStringStringCmd
	corrupt               bool
}

func (r *AgentV3SessionRepository) pendingBatch(ctx context.Context, s sessionScopeKeys, scope session.Scope, ids []string) ([]session.Intent, []error, error) {
	var out []session.Intent
	var failures []error
	err := r.atomic(ctx, func(t *sessionTxn) error {
		out = nil
		failures = nil
		if err := t.check(map[string]string{s.dags: sessionRedisSet}); err != nil {
			return err
		}
		reads := make([]sessionPendingRead, len(ids))
		keys := make([]string, 0, 2*len(ids))
		for i, id := range ids {
			if err := ctx.Err(); err != nil {
				return err
			}
			reads[i].dag = s.dag(id)
			keys = append(keys, reads[i].dag.meta, reads[i].dag.intents)
		}
		if err := t.tx.Watch(ctx, keys...).Err(); err != nil {
			return err
		}
		_, err := t.tx.Pipelined(ctx, func(p redis.Pipeliner) error {
			for i := range reads {
				if err := ctx.Err(); err != nil {
					return err
				}
				entry := &reads[i]
				entry.metaType = p.Type(ctx, entry.dag.meta)
				entry.intentsType = p.Type(ctx, entry.dag.intents)
			}
			return nil
		})
		if err != nil {
			return err
		}
		for i := range reads {
			entry := &reads[i]
			entry.corrupt = entry.metaType.Val() != sessionRedisNone && entry.metaType.Val() != "string" || entry.intentsType.Val() != sessionRedisNone && entry.intentsType.Val() != sessionRedisHash
		}
		_, err = t.tx.Pipelined(ctx, func(p redis.Pipeliner) error {
			for i := range reads {
				if err := ctx.Err(); err != nil {
					return err
				}
				entry := &reads[i]
				entry.member = p.SIsMember(ctx, s.dags, entry.dag.id)
				if !entry.corrupt {
					entry.meta = p.Get(ctx, entry.dag.meta)
					entry.intents = p.HGetAll(ctx, entry.dag.intents)
				}
			}
			return nil
		})
		if err != nil && !errors.Is(err, redis.Nil) && !sessionPureCorruption(sessionRedisError(err)) {
			return err
		}
		for i := range reads {
			if err := ctx.Err(); err != nil {
				return err
			}
			entry := &reads[i]
			member, err := entry.member.Result()
			if err != nil {
				return err
			}
			if !member {
				continue
			}
			current, err := sessionPendingEntry(ctx, scope, entry)
			if err != nil {
				if !sessionPureCorruption(err) {
					return err
				}
				failures = append(failures, sessionDAGError(scope, entry.dag.id, err))
				continue
			}
			out = append(out, current...)
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

func sessionMaintenanceFailure(err error) error {
	if !errors.Is(err, session.ErrCorrupt) || sessionPureCorruption(err) {
		return err
	}
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		var operational []error
		for _, cause := range joined.Unwrap() {
			if !sessionPureCorruption(cause) {
				operational = append(operational, sessionMaintenanceFailure(cause))
			}
		}
		return errors.Join(operational...)
	}
	return sessionMaintenanceFailure(errors.Unwrap(err))
}

func sessionPendingEntry(ctx context.Context, scope session.Scope, entry *sessionPendingRead) ([]session.Intent, error) {
	if entry.corrupt {
		return nil, session.ErrCorrupt
	}
	meta, err := sessionReadMeta(ctx, entry.meta, entry.dag, scope)
	if err != nil {
		return nil, err
	}
	if meta == nil {
		return nil, session.ErrCorrupt
	}
	raw, err := entry.intents.Result()
	if err != nil {
		return nil, sessionRedisError(err)
	}
	if meta.State != sessionDAGActive {
		return nil, nil
	}
	return sessionDecodeIntents(ctx, raw, meta)
}
