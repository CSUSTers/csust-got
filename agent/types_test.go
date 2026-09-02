package agentv3

import (
	"testing"

	"csust-got/config"

	"github.com/stretchr/testify/assert"
)

func TestTurnContextTracksMultipleLoadedSkillNames(t *testing.T) {
	tc := &TurnContext{V3: &AgentV3TurnState{loadedSkillNames: map[string]struct{}{}}}
	tc.markSkillLoaded(" Repo_Inspect ")
	tc.markSkillLoaded("rich-message")

	assert.True(t, tc.hasLoadedSkill("repo-inspect"))
	assert.True(t, tc.hasLoadedSkill("RICH_MESSAGE"))
	assert.False(t, tc.hasLoadedSkill("missing"))
}

func TestRichMessageAuthorizationPersistsAfterOtherToolsButRejectsOrdinarySkill(t *testing.T) {
	oldConfig := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = oldConfig })
	tc := &TurnContext{
		Config: &config.AgentConfig{Agent: &config.AgentOptions{Enable: true, Rich: true}},
		V3:     &AgentV3TurnState{loadedSkillNames: map[string]struct{}{}},
	}

	tc.markSkillLoaded("repo-inspect")
	assert.False(t, tc.richMessageSkillLoadedForFinal())

	tc.markSkillLoaded("rich-message")
	assert.True(t, tc.richMessageSkillLoadedForFinal())

	tc.markSkillLoaded("another-skill")
	assert.True(t, tc.richMessageSkillLoadedForFinal())
}
