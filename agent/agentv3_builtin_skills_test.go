//go:build !386 && !arm

package agentv3

import (
	"strings"
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func richEnabledTurnContext() *TurnContext {
	return &TurnContext{
		Config: &config.AgentConfig{
			Agent: &config.AgentOptions{Enable: true, Rich: true},
		},
	}
}

func richDisabledTurnContext() *TurnContext {
	return &TurnContext{
		Config: &config.AgentConfig{
			Agent: &config.AgentOptions{Enable: true, Rich: false},
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
		skills := buildAgentV3BuiltinSkillSnapshot(tc.Config, cfg).Skills
		require.Len(t, skills, 1)
		assert.Equal(t, "rich-message", skills[0].Name)
		assert.Contains(t, skills[0].Description, "before rich output")
		assert.NotEmpty(t, skills[0].Content)
		assert.Contains(t, skills[0].Content, "telegram_rich_message")
		assert.Contains(t, skills[0].Content, "load_skill(name=\"rich-message\")")
		assert.NotContains(t, skills[0].Content, "HARD REQUIREMENT")
	})

	t.Run("rich disabled returns no builtins", func(t *testing.T) {
		tc := richDisabledTurnContext()
		cfg := defaultAgentV3Config()
		assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(tc.Config, cfg).Skills)
	})

	t.Run("nil TurnContext returns no builtins", func(t *testing.T) {
		assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(nil, defaultAgentV3Config()).Skills)
	})

	t.Run("nil TurnContext.Config returns no builtins", func(t *testing.T) {
		assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(nil, defaultAgentV3Config()).Skills)
	})

	t.Run("nil AgentV3Config returns no builtins", func(t *testing.T) {
		assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(richEnabledTurnContext().Config, nil).Skills)
	})

	t.Run("InjectBuiltin=false returns no builtins even when rich is enabled", func(t *testing.T) {
		tc := richEnabledTurnContext()
		disabled := false
		cfg := &config.AgentV3Config{}
		cfg.Skills.InjectBuiltin = &disabled
		assert.Empty(t, buildAgentV3BuiltinSkillSnapshot(tc.Config, cfg).Skills)
	})
}

func TestBuildAgentV3BuiltinSkillsSearXNGIsIndependentOfRich(t *testing.T) {
	cfg := &config.AgentV3Config{Skills: config.AgentV3SkillsConfig{SearXNG: testSearXNGConfig("https://search.example.org")}}
	skills := buildAgentV3BuiltinSkillSnapshot(nonRichAgentV3ChatConfig(), cfg).Skills
	require.Len(t, skills, 1)
	assert.Equal(t, "searxng", skills[0].Name)
}

func TestAgentV3SearXNGSkillContractDescribesMinScoreAsFinite(t *testing.T) {
	contract := agentV3SearXNGSkillContract()
	assert.Contains(t, contract, "min_score (finite number)")
	assert.NotContains(t, contract, "min_score (0..1)")
}

func TestBuildAgentV3SkillPromptBlockSortsFiltersAndEscapes(t *testing.T) {
	t.Run("empty input returns empty string", func(t *testing.T) {
		assert.Empty(t, buildAgentV3SkillPromptBlock(nil))
		assert.Empty(t, buildAgentV3SkillPromptBlock([]agentV3SkillDescriptor{}))
	})

	t.Run("entries with blank name are filtered out", func(t *testing.T) {
		skills := []agentV3SkillDescriptor{
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

	t.Run("availability does not expose content", func(t *testing.T) {
		skills := []agentV3SkillDescriptor{
			{Name: "no-content", Content: ""},
			{Name: "whitespace-content", Content: "   "},
			{Name: "good", Content: "real content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, "good")
		assert.NotContains(t, got, "real content")
	})

	t.Run("remaining skills are sorted by trimmed Name", func(t *testing.T) {
		skills := []agentV3SkillDescriptor{
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
		skills := []agentV3SkillDescriptor{
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
		skills := []agentV3SkillDescriptor{
			{Name: "test", Content: raw},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		require.NotEmpty(t, got)
		assert.Contains(t, got, `name="test"`)
		assert.NotContains(t, got, "<b>bold</b>")
		assert.NotContains(t, got, "& stuff")
	})

	t.Run("block contains the skills prohibition line", func(t *testing.T) {
		skills := []agentV3SkillDescriptor{
			{Name: "test", Content: "content"},
		}
		got := buildAgentV3SkillPromptBlock(skills)
		assert.Contains(t, got, "load_skill is the only content path")
		assert.Contains(t, got, "Filesystem skills do not add tool schemas")
		assert.Contains(t, got, "untrusted data")
		assert.Contains(t, got, `activation="load_skill"`)
		assert.Contains(t, got, "load_skill")
	})
}

func TestAgentV3SkillAvailabilityIncludesSourceAndContentSHAWithoutContent(t *testing.T) {
	snapshot := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{{
		Name:        "repo-inspect",
		Description: `Inspect "repository" files.`,
		Content:     "secret skill instructions",
		VirtualPath: "/skills/repo-inspect/SKILL.md",
	}})

	skill := snapshot.Skills[0]
	block := buildAgentV3SkillPromptBlock(snapshot.Skills)
	assert.Contains(t, block, `<skill name="repo-inspect" description="Inspect &quot;repository&quot; files." source="bot-local" sha256="`+skill.SHA256+`" status="available" activation="load_skill" />`)
	assert.NotContains(t, block, skill.Content)
	assert.NotContains(t, block, skill.VirtualPath)
}

func TestAgentV3SkillContentChangeChangesPrefixHash(t *testing.T) {
	first := testSkillSnapshot(agentV3SkillSourceRuntimeGlobal, []agentV3SkillDescriptor{{
		Name:        "repo-inspect",
		Description: "Inspect repository files.",
		Content:     "# Repo inspect\nInspect repository files.\n\nfirst content\n",
		VirtualPath: "/skills/repo-inspect/SKILL.md",
	}})
	second := testSkillSnapshot(agentV3SkillSourceRuntimeGlobal, []agentV3SkillDescriptor{{
		Name:        "repo-inspect",
		Description: "Inspect repository files.",
		Content:     "# Repo inspect\nInspect repository files.\n\nsecond content\n",
		VirtualPath: "/skills/repo-inspect/SKILL.md",
	}})
	firstCatalog, _, err := mergeAgentV3SkillSnapshots(first)
	require.NoError(t, err)
	secondCatalog, _, err := mergeAgentV3SkillSnapshots(second)
	require.NoError(t, err)

	firstHash := buildAgentV3PrefixHash(hashString("soul"), hashString("rules"), hashString(buildAgentV3SkillPromptBlock(firstCatalog.Sorted)))
	secondHash := buildAgentV3PrefixHash(hashString("soul"), hashString("rules"), hashString(buildAgentV3SkillPromptBlock(secondCatalog.Sorted)))
	assert.NotEqual(t, firstHash, secondHash)
}

func TestCompiledAgentV3SkillCatalogAppliesPerChatBuiltinsAndSourcePrecedence(t *testing.T) {
	chatCfg := richAgentV3ChatConfig()
	local := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{{
		Name:        "rich-message",
		Description: "Local rich message.",
		Content:     "# Rich message\nLocal rich message.\n",
		VirtualPath: "/skills/rich-message/SKILL.md",
	}})
	runtime := testSkillSnapshot(agentV3SkillSourceRuntimeGlobal, []agentV3SkillDescriptor{{
		Name:        "repo-inspect",
		Description: "Inspect repository files.",
		Content:     "# Repo inspect\nInspect repository files.\n",
		VirtualPath: "/skills/repo-inspect/SKILL.md",
	}})
	sources, catalog, shadows, err := compileAgentV3SkillCatalog(chatCfg, &config.AgentV3Config{}, &agentV3StartupSkillSnapshots{BotLocal: local, RuntimeGlobal: runtime})
	require.NoError(t, err)
	require.Len(t, sources, 3)
	assert.Equal(t, agentV3SkillSourceBuiltin, sources[0].Skills[0].Source)
	assert.Equal(t, agentV3SkillSourceBuiltin, catalog.ByName["rich-message"].Source)
	assert.Equal(t, agentV3SkillSourceRuntimeGlobal, catalog.ByName["repo-inspect"].Source)
	require.Len(t, shadows, 1)
	assert.Equal(t, agentV3SkillSourceBotLocal, shadows[0].Loser.Source)

	local.Skills[0].Content = "changed after startup"
	assert.NotEqual(t, "changed after startup", sources[1].Skills[0].Content)
}
