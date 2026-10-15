package orm

import (
	"testing"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionProductionFactoryUsesPrivateClientAndPrefix(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	oldClient, oldConfig := rc, config.BotConfig
	t.Cleanup(func() { rc, config.BotConfig = oldClient, oldConfig })
	rc = f.repo.client
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.RedisConfig.KeyPrefix = "production-factory:"
	svc, err := NewProductionAgentV3SessionService(f.dir, session.Options{TTL: time.Hour})
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, svc.Close()) })
	scope := session.Scope{Bot: "bot", Platform: "telegram", ChatID: -100}
	node, err := svc.Commit(t.Context(), sessionRequest(t, scope, "agent", nil, 101, "factory"))
	require.NoError(t, err)
	require.Equal(t, session.StorageNamespace("production-factory:"), node.Scope.Namespace)
	loaded, err := svc.Load(t.Context(), session.Selection{Scope: scope, Mode: session.SelectReply, ReplyMessageID: 101})
	require.NoError(t, err)
	require.Equal(t, node.Ref, loaded.Parent.Ref())
	require.NoError(t, loaded.Parent.Close())
	_, err = NewProductionAgentV3SessionService(f.dir, session.Options{TTL: -time.Second})
	require.Error(t, err)
	rc = nil
	_, err = NewProductionAgentV3SessionRepository()
	require.Error(t, err)
	config.BotConfig = nil
	_, err = NewProductionAgentV3SessionRepository()
	require.Error(t, err)
}

func TestAgentV3SessionProductionFactoryAcceptsValidatedTinyTTL(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	oldClient, oldConfig := rc, config.BotConfig
	t.Cleanup(func() { rc, config.BotConfig = oldClient, oldConfig })
	rc = f.repo.client
	config.BotConfig = config.NewBotConfig()
	config.BotConfig.RedisConfig.KeyPrefix = "tiny-ttl-factory:"
	cfg := config.AgentV3SessionConfig{TTL: "1ns"}
	require.NoError(t, cfg.Validate())
	svc, err := NewProductionAgentV3SessionService(f.dir, session.Options{TTL: cfg.IdleTTL()})
	require.NoError(t, err, "configuration accepted by Validate must initialize storage")
	require.NoError(t, svc.Close())
}
