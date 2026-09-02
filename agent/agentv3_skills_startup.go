package agentv3

import (
	"context"
	"fmt"
	"strings"

	"csust-got/config"
)

type agentV3StartupSkillSnapshots struct {
	BotLocal      agentV3SkillSnapshot
	RuntimeGlobal agentV3SkillSnapshot
	SearXNG       *searXNGClient
}

func loadAgentV3StartupSkillSnapshots(ctx context.Context, cfg *config.AgentV3Config, runtime *RemoteRuntimeClient) (*agentV3StartupSkillSnapshots, error) {
	if cfg == nil {
		return nil, errAgentV3ConfigNil
	}
	var searxng *searXNGClient
	if cfg.Skills.SearXNG.Enable && cfg.Skills.BuiltinInjectionEnabled() {
		if err := cfg.ValidateSearXNG(); err != nil {
			return nil, fmt.Errorf("validate SearXNG skills: %w", err)
		}
		var err error
		searxng, err = newSearXNGClient(&cfg.Skills.SearXNG)
		if err != nil {
			return nil, fmt.Errorf("create SearXNG client: %w", err)
		}
	}

	botLocal := emptyAgentV3SkillSnapshot(agentV3SkillSourceBotLocal)
	if strings.TrimSpace(cfg.Skills.Root) != "" {
		var err error
		botLocal, err = loadAgentV3FilesystemSkillSnapshot(cfg.Skills.Root, agentV3SkillSourceBotLocal)
		if err != nil {
			return nil, fmt.Errorf("load bot-local skills: %w", err)
		}
	}

	if runtime == nil {
		runtime = NewRemoteRuntimeClient(&cfg.Runtime, cfg.RuntimeCommandTimeout(), cfg.RuntimeRequestTimeout())
	}

	runtimeGlobal := emptyAgentV3SkillSnapshot(agentV3SkillSourceRuntimeGlobal)
	if cfg.Skills.RuntimeGlobal {
		var err error
		runtimeGlobal, err = runtime.SkillsSnapshot(ctx)
		if err != nil {
			return nil, fmt.Errorf("load runtime-global skills: %w", err)
		}
	}

	return &agentV3StartupSkillSnapshots{
		BotLocal:      botLocal,
		RuntimeGlobal: runtimeGlobal,
		SearXNG:       searxng,
	}, nil
}
