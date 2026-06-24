//go:build !386 && !arm

package chatv2

import (
	"strings"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func richEnabledTurnContext() *TurnContext {
	return &TurnContext{
		Config: &config.ChatConfigSingle{
			Agent: &config.AgentConfig{Enable: true, V3: true, Rich: true},
		},
	}
}

func richDisabledTurnContext() *TurnContext {
	return &TurnContext{
		Config: &config.ChatConfigSingle{
			Agent: &config.AgentConfig{Enable: true, V3: true, Rich: false},
		},
	}
}

func defaultAgentV3Config() *config.AgentV3Config {
	return &config.AgentV3Config{}
}

func TestBuildAgentV3BuiltinSkillsRichGate(t *testing.T) {
	old := config.BotConfig
	t.Cleanup(func() { config.BotConfig = old })
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}

	t.Run("rich enabled returns exactly one builtin named rich-message", func(t *testing.T) {
		tc := richEnabledTurnContext()
		cfg := defaultAgentV3Config()
		skills := buildAgentV3BuiltinSkills(tc, cfg)
		require.Len(t, skills, 1)
		assert.Equal(t, "rich-message", skills[0].Name)
		assert.NotEmpty(t, skills[0].Content)
		assert.Contains(t, skills[0].Content, "telegram_rich_message")
	})

	t.Run("rich disabled returns no builtins", func(t *testing.T) {
		tc := richDisabledTurnContext()
		cfg := defaultAgentV3Config()
		assert.Empty(t, buildAgentV3BuiltinSkills(tc, cfg))
	})

	t.Run("nil TurnContext returns no builtins", func(t *testing.T) {
		assert.Empty(t, buildAgentV3BuiltinSkills(nil, defaultAgentV3Config()))
	})

	t.Run("nil TurnContext.Config returns no builtins", func(t *testing.T) {
		tc := &TurnContext{Config: nil}
		assert.Empty(t, buildAgentV3BuiltinSkills(tc, defaultAgentV3Config()))
	})

	t.Run("nil AgentV3Config returns no builtins", func(t *testing.T) {
		assert.Empty(t, buildAgentV3BuiltinSkills(richEnabledTurnContext(), nil))
	})

	t.Run("InjectBuiltin=false returns no builtins even when rich is enabled", func(t *testing.T) {
		tc := richEnabledTurnContext()
		disabled := false
		cfg := &config.AgentV3Config{}
		cfg.Skills.InjectBuiltin = &disabled
		assert.Empty(t, buildAgentV3BuiltinSkills(tc, cfg))
	})
}

func TestBuildAgentV3SkillPromptBlockSortsFiltersAndEscapes(t *testing.T) {
	t.Run("empty input returns empty string", func(t *testing.T) {
		assert.Empty(t, buildAgentV3SkillPromptBlock(nil))
		assert.Empty(t, buildAgentV3SkillPromptBlock([]agentV3BuiltinSkill{}))
	})

	t.Run("entries with blank name are filtered out", func(t *testing.T) {
		skills := []agentV3BuiltinSkill{
			{Name: "", Content: "some content"},
			{Name: "   ", Content: "another content"},
			{Name: "valid", Content: "real content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, "valid")
		assert.NotContains(t, got, "some content")
		assert.NotContains(t, got, "another content")
	})

	t.Run("entries with blank content are filtered out", func(t *testing.T) {
		skills := []agentV3BuiltinSkill{
			{Name: "no-content", Content: ""},
			{Name: "whitespace-content", Content: "   "},
			{Name: "good", Content: "real content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, "good")
		assert.NotContains(t, got, "no-content")
		assert.NotContains(t, got, "whitespace-content")
	})

	t.Run("remaining skills are sorted by trimmed Name", func(t *testing.T) {
		skills := []agentV3BuiltinSkill{
			{Name: "zebra", Content: "z content"},
			{Name: "alpha", Content: "a content"},
			{Name: "middle", Content: "m content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		alphaIdx := strings.Index(got, "alpha")
		middleIdx := strings.Index(got, "middle")
		zebraIdx := strings.Index(got, "zebra")
		assert.Less(t, alphaIdx, middleIdx)
		assert.Less(t, middleIdx, zebraIdx)
	})

	t.Run("Name and Description attributes escape special XML characters", func(t *testing.T) {
		skills := []agentV3BuiltinSkill{
			{
				Name:        `a&b"c<d>e`,
				Description: `x&y"z<w>v`,
				Content:     "content here",
			},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, `a&amp;b&quot;c&lt;d&gt;e`)
		assert.Contains(t, got, `x&amp;y&quot;z&lt;w&gt;v`)
		assert.NotContains(t, got, `name="a&b`)
		assert.NotContains(t, got, `description="x&y`)
	})

	t.Run("content is not embedded in availability block", func(t *testing.T) {
		raw := "  <b>bold</b> & stuff  "
		skills := []agentV3BuiltinSkill{
			{Name: "test", Content: raw},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, `name="test"`)
		assert.NotContains(t, got, "<b>bold</b>")
		assert.NotContains(t, got, "& stuff")
	})

	t.Run("block contains the skills prohibition line", func(t *testing.T) {
		skills := []agentV3BuiltinSkill{
			{Name: "test", Content: "content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		assert.Contains(t, got, "Do not use read/grep to load skills from /skills")
		assert.Contains(t, got, "load_skill")
	})
}
