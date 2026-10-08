package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestUnknownConfigKeysReportsOnlyUnacceptedPaths(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(`
agent_v3:
  enable: true
  memory:
    write_policy: explicit_quota
    bogus: 1
  runtime:
    env:
      FOO: bar
  skills:
    searxng:
      base_url: https://search.example.org
agents:
  - name: assistant
    typo: true
    model:
      name: m
      features:
        image: true
    trigger:
      - command: chat
      - command: sear
        hints: wrong
    session:
      save_context: true
    agent:
      tool_models:
        analyze_image:
          name: vision
      subagents:
        - name: r
          runtime: true
          skills: [searxng]
          max_result_chars: 100
          unknown_sub: 1
      mcp_servers:
        - enable: true
          tools:
            - time
            - searxng: [search]
`)))
	require.Equal(t, []string{
		"agent_v3.memory.bogus",
		"agents[0].agent.subagents[0].unknown_sub",
		"agents[0].trigger[1].hints",
		"agents[0].typo",
	}, UnknownConfigKeys())
}

func TestUnknownConfigKeysIgnoresMissingSections(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("token: x\n")))
	require.Empty(t, UnknownConfigKeys())
}

func TestUnimplementedConfigKeys(t *testing.T) {
	temperature := float32(0.5)
	agents := &AgentV3Configs{
		nil,
		{Name: "a", Temperature: &temperature, ReasoningEffort: "medium", Model: &Model{Proxy: "http://p", PromptLimit: 1000}},
		{Name: "b", Agent: &AgentOptions{SubAgents: []*SubAgentConfig{{Model: &Model{Proxy: "socks5://p"}}}}},
	}
	v3 := &AgentV3Config{Model: &Model{PromptLimit: 10}, ContextCache: AgentV3ContextCacheConfig{PromptCacheRetention: "in_memory"}}
	require.Equal(t, []string{
		"agents[1].model.proxy",
		"agents[1].model.prompt_limit",
		"agents[2].agent.subagents[0].model.proxy",
		"agent_v3.model.prompt_limit",
		"agent_v3.context_cache.prompt_cache_retention",
	}, UnimplementedConfigKeys(agents, v3))
	require.Empty(t, UnimplementedConfigKeys(nil, nil))
	require.Empty(t, UnimplementedConfigKeys(&AgentV3Configs{{Name: "clean", Model: &Model{Model: "x"}}}, &AgentV3Config{}))
}
