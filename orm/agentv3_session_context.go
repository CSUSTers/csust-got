package orm

import (
	"context"
	"encoding/hex"
	"errors"
	"strings"

	"csust-got/agent/session"

	"github.com/redis/go-redis/v9"
)

func sessionContextField(contextKey, nodeID string) string {
	return hex.EncodeToString([]byte(contextKey)) + ":" + nodeID
}

func (t *sessionTxn) contextAccepted(d sessionDAGKeys, ref session.NodeRef, contextKey string) error {
	if err := t.check(map[string]string{d.rejectedContexts: sessionRedisHash}); err != nil {
		return err
	}
	value, err := t.tx.HGet(t.ctx, d.rejectedContexts, sessionContextField(contextKey, ref.NodeID)).Result()
	if errors.Is(err, redis.Nil) {
		return nil
	}
	if err != nil {
		return err
	}
	if value != "1" {
		return session.ErrCorrupt
	}
	return session.ErrContextRejected
}

// RejectContext records a candidate-specific rejection without changing its activity or archive.
func (r *AgentV3SessionRepository) RejectContext(ctx context.Context, scope session.Scope, ref session.NodeRef, contextKey string) error {
	if ref.Validate() != nil || contextKey == "" {
		return session.ErrCorrupt
	}
	s, err := r.scopeKeys(scope)
	if err != nil {
		return err
	}
	d := s.dag(ref.DAGID)
	return r.atomic(ctx, func(t *sessionTxn) error {
		if err := t.check(map[string]string{s.dags: sessionRedisSet, s.runs: sessionRedisHash, d.meta: "string", d.nodes: sessionRedisHash, d.rejectedContexts: sessionRedisHash}); err != nil {
			return err
		}
		meta, err := t.meta(d, scope)
		if err != nil {
			return err
		}
		if meta == nil || meta.State != sessionDAGActive {
			return session.ErrFence
		}
		member, err := t.member(s, d)
		if err != nil {
			return err
		}
		if !member {
			return session.ErrCorrupt
		}
		var node session.Node
		found, err := sessionReadJSON(ctx, t.tx.HGet(ctx, d.nodes, ref.NodeID), &node)
		if err != nil {
			return err
		}
		if !found {
			return session.ErrFence
		}
		if node.Ref != ref || sessionValidateNode(node, scope, d.id, true) != nil {
			return session.ErrCorrupt
		}
		run, err := t.run(s, node.RunID)
		if err != nil {
			return err
		}
		if run == nil || run.Ref != ref || run.Status != sessionIntentPublished {
			return session.ErrCorrupt
		}
		err = t.contextAccepted(d, ref, contextKey)
		if errors.Is(err, session.ErrContextRejected) {
			return nil
		}
		if err != nil {
			return err
		}
		t.write("hset", d.rejectedContexts, sessionContextField(contextKey, ref.NodeID), "1")
		return nil
	})
}

func sessionValidateRejectedContexts(ctx context.Context, raw map[string]string, nodes map[string]session.Node) error {
	for field, value := range raw {
		if err := ctx.Err(); err != nil {
			return err
		}
		encoded, nodeID, ok := strings.Cut(field, ":")
		key, err := hex.DecodeString(encoded)
		if !ok || err != nil || len(key) == 0 || hex.EncodeToString(key) != encoded || !session.ValidID(nodeID) || value != "1" {
			return session.ErrCorrupt
		}
		if _, ok := nodes[nodeID]; !ok {
			return session.ErrCorrupt
		}
	}
	return nil
}
