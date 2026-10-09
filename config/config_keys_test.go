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
token: x
tokenn: y
log:
  max_size_mb: 100
  max_size_mbb: 100
redis:
  addr: "redis:6379"
white_list:
  enabled: true
  chats: [1]
get_voice:
  enable: true
  indexes:
    - name: genshin
      index_uids: x
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
		"get_voice.indexes[0].index_uids",
		"log.max_size_mbb",
		"tokenn",
	}, UnknownConfigKeys())
}

func TestUnknownConfigKeysIgnoresNonObjectSections(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader("log: 3\nmc: [1]\nagents: {}\nget_voice: ~\n")))
	require.Empty(t, UnknownConfigKeys())
}

func TestUnknownConfigKeysAcceptsExampleConfig(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigFile(repoConfigFile)
	require.NoError(t, viper.ReadInConfig())
	require.Empty(t, UnknownConfigKeys())
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
		{
			Name: "c",
			Agent: &AgentOptions{
				ToolModels: map[string]*Model{"web_search": {Model: "x"}, "analyze_image": {Proxy: "http://p"}},
				SubAgents:  []*SubAgentConfig{nil, {ToolModels: map[string]*Model{"analyze_image": {PromptLimit: 5}}}},
				Skills:     []*SkillConfig{{ToolModels: map[string]*Model{"analyze_image": {Proxy: "http://p"}}}},
			},
			Format: AgentOutputConfig{ProgressSummary: &ProgressSummaryConfig{Model: &Model{Proxy: "http://p"}}},
		},
	}
	v3 := &AgentV3Config{
		Model:        &Model{PromptLimit: 10},
		ContextCache: AgentV3ContextCacheConfig{PromptCacheRetention: "in_memory"},
		Session:      AgentV3SessionConfig{Compact: AgentV3SessionCompactConfig{Model: &Model{Proxy: "http://p"}}},
	}
	require.Equal(t, []string{
		"agents[1].model.proxy",
		"agents[1].model.prompt_limit",
		"agents[2].agent.subagents[0].model.proxy",
		"agents[3].agent.tool_models.analyze_image.proxy",
		"agents[3].agent.subagents[1].tool_models.analyze_image.prompt_limit",
		"agents[3].agent.skills[0].tool_models.analyze_image.proxy",
		"agents[3].format.progress_summary.model.proxy",
		"agent_v3.model.prompt_limit",
		"agent_v3.session.compact.model.proxy",
		"agent_v3.context_cache.prompt_cache_retention",
	}, UnimplementedConfigKeys(agents, v3))
	require.Empty(t, UnimplementedConfigKeys(nil, nil))
	require.Empty(t, UnimplementedConfigKeys(&AgentV3Configs{{Name: "clean", Model: &Model{Model: "x"}}}, &AgentV3Config{}))
}
