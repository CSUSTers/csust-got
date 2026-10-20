package config

import (
	"strings"
	"testing"

	"github.com/spf13/viper"
	"github.com/stretchr/testify/require"
)

func TestSubAgentSkillBundlesDecodeAndDiagnostics(t *testing.T) {
	viper.Reset()
	t.Cleanup(viper.Reset)
	viper.SetConfigType("yaml")
	require.NoError(t, viper.ReadConfig(strings.NewReader(`
agents:
  - name: parent
    agent:
      subagents:
        - name: bundled
          skills: [searxng]
          skill_bundles:
            - name: vision
              tools: [analyze_image]
              mcp_servers:
                - enable: true
                  type: mcpo
                  url: http://fixture.invalid
                  tools: [time]
              system_prompt_addon: ["First.", "Second."]
              tool_models:
                analyze_image:
                  model: vision
                  proxy: http://proxy.invalid
                  prompt_limit: 42
                  typo_model: true
              typo_bundle: true
        - name: legacy
          skills: [rich-message]
        - name: plain
        - name: empty
          skill_bundles: []
`)))
	var agents AgentV3Configs
	agents.readConfig()
	subs := agents[0].Agent.SubAgents
	require.Len(t, subs, 4)
	require.Equal(t, []string{"searxng"}, subs[0].Skills)
	require.Len(t, subs[0].SkillBundles, 1)
	bundle := subs[0].SkillBundles[0]
	require.Equal(t, "vision", bundle.Name)
	require.Equal(t, []string{"analyze_image"}, bundle.Tools)
	require.Len(t, bundle.McpServers, 1)
	require.Equal(t, "http://fixture.invalid", bundle.McpServers[0].Url)
	require.Equal(t, "First.Second.", bundle.SystemPromptAddon.String())
	require.Equal(t, "vision", bundle.ToolModels["analyze_image"].Model)
	require.Equal(t, []string{"rich-message"}, subs[1].Skills)
	for _, sub := range subs[1:] {
		require.Empty(t, sub.SkillBundles)
	}
	require.Equal(t, []string{
		"agents[0].agent.subagents[0].skill_bundles[0].tool_models.analyze_image.typo_model",
		"agents[0].agent.subagents[0].skill_bundles[0].typo_bundle",
	}, UnknownConfigKeys())
	subs[0].SkillBundles = append([]*SkillConfig{nil}, subs[0].SkillBundles...)
	require.Equal(t, []string{
		"agents[0].agent.subagents[0].skill_bundles[1].tool_models.analyze_image.proxy",
		"agents[0].agent.subagents[0].skill_bundles[1].tool_models.analyze_image.prompt_limit",
	}, UnimplementedConfigKeys(&agents, nil))
}

func TestSubAgentSkillBundlesMaxSteps(t *testing.T) {
	for _, tt := range []struct {
		name   string
		bundle *SkillConfig
		want   int
	}{
		{name: "builtin", bundle: &SkillConfig{Tools: []string{"get_context"}}, want: 4},
		{name: "mcp", bundle: &SkillConfig{McpServers: []*ToolServerConfig{{Enable: true}}}, want: 4},
		{name: "addon", bundle: &SkillConfig{SystemPromptAddon: "instructions"}, want: 1},
		{name: "model", bundle: &SkillConfig{ToolModels: map[string]*Model{"analyze_image": {Model: "vision"}}}, want: 1},
		{name: "nil", want: 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			cfg := &SubAgentConfig{MaxSteps: 1, SkillBundles: []*SkillConfig{nil, tt.bundle}}
			require.Equal(t, tt.want, cfg.GetMaxSteps())
		})
	}
}
