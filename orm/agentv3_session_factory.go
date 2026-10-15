package orm

import (
	"errors"

	"csust-got/agent/session"
	"csust-got/config"
)

var errSessionConfigRequired = errors.New("bot configuration is not initialized")

// NewProductionAgentV3SessionRepository borrows orm's initialized client and configured prefix.
func NewProductionAgentV3SessionRepository() (*AgentV3SessionRepository, error) {
	if config.BotConfig == nil {
		return nil, errSessionConfigRequired
	}
	return NewAgentV3SessionRepository(rc, config.BotConfig.RedisConfig.KeyPrefix)
}

// NewProductionAgentV3SessionService assembles storage without taking ownership of orm's client.
func NewProductionAgentV3SessionService(directory string, options session.Options) (*session.Service, error) {
	repo, err := NewProductionAgentV3SessionRepository()
	if err != nil {
		return nil, err
	}
	files, err := session.NewFileStore(directory)
	if err != nil {
		return nil, err
	}
	service, err := session.NewService(repo, files, options)
	if err != nil {
		return nil, errors.Join(err, files.Close())
	}
	return service, nil
}
