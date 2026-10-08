package orm

import (
	"fmt"
	"time"

	"csust-got/agent/session"
)

func (t *sessionTxn) reserveParent(d sessionDAGKeys, req session.Reservation) (*sessionMeta, error) {
	if req.Parent.Validate() != nil {
		return nil, session.ErrCorrupt
	}
	if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash, d.leases: sessionRedisHash, d.intents: sessionRedisHash}); err != nil {
		return nil, err
	}
	meta, err := t.meta(d, req.Scope)
	if err != nil {
		return nil, err
	}
	stored, err := t.validLease(d, meta, *req.Lease)
	if err != nil {
		return nil, err
	}
	now, err := t.tx.Time(t.ctx).Result()
	if err != nil {
		return nil, err
	}
	if stored.Deadline <= now.UnixMilli() {
		return nil, session.ErrFence
	}
	if err := t.parent(d, req.Scope, *req.Parent); err != nil {
		return nil, err
	}
	return meta, nil
}

func (r *AgentV3SessionRepository) reserveRoot(t *sessionTxn, s sessionScopeKeys, d sessionDAGKeys, req session.Reservation, ids []string, duration time.Duration) (*sessionMeta, session.Lease, error) {
	if err := t.check(map[string]string{d.meta: "string", d.nodes: sessionRedisHash, d.leases: sessionRedisHash, d.intents: sessionRedisHash, d.rejectedContexts: sessionRedisHash, s.dags: sessionRedisSet, s.sequence: "string", s.pending: sessionRedisSet, s.deleting: sessionRedisSet, r.base + "scopes": sessionRedisHash}); err != nil {
		return nil, session.Lease{}, err
	}
	for _, key := range []string{d.meta, d.nodes, d.leases, d.intents, d.rejectedContexts} {
		if t.types[key] != sessionRedisNone {
			return nil, session.Lease{}, session.ErrCorrupt
		}
	}
	registered, err := t.catalog(r.base+"scopes", req.Scope)
	if err != nil {
		return nil, session.Lease{}, err
	}
	count, err := t.tx.SCard(t.ctx, s.dags).Result()
	if err != nil {
		return nil, session.Lease{}, err
	}
	if count == 0 {
		if err := t.check(map[string]string{s.messages: sessionRedisHash}); err != nil {
			return nil, session.Lease{}, err
		}
		if t.types[s.sequence] != sessionRedisNone || t.types[s.runs] != sessionRedisNone || t.types[s.messages] != sessionRedisNone || t.types[s.pending] != sessionRedisNone || t.types[s.deleting] != sessionRedisNone {
			return nil, session.Lease{}, fmt.Errorf("%w: orphan scope indexes", session.ErrCorrupt)
		}
		t.write(sessionRedisSet, s.sequence, "0")
	} else if _, err := t.sequence(s); err != nil {
		return nil, session.Lease{}, err
	}
	now, err := t.tx.Time(t.ctx).Result()
	if err != nil {
		return nil, session.Lease{}, err
	}
	meta := &sessionMeta{Layout: sessionRedisLayout, Scope: req.Scope, ID: ids[1], Generation: ids[2], State: sessionDAGActive, LastActive: now.UnixMilli()}
	lease := session.Lease{DAGID: meta.ID, Generation: meta.Generation, Token: ids[3], Deadline: now.UnixMilli() + duration.Milliseconds()}
	t.write(sessionRedisSet, d.meta, sessionEncode(meta))
	t.write("hset", d.leases, lease.Token, sessionEncode(lease))
	t.write("sadd", s.dags, meta.ID)
	if !registered {
		t.write("hset", r.base+"scopes", req.Scope.Key(), sessionEncode(req.Scope))
	}
	return meta, lease, nil
}
