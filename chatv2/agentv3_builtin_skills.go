//go:build !386 && !arm

package chatv2

import (
	"sort"
	"strings"

	"csust-got/config"
)

type agentV3BuiltinSkill struct {
	Name        string
	Description string
	Content     string
}

func buildAgentV3BuiltinSkills(tc *TurnContext, cfg *config.AgentV3Config) []agentV3BuiltinSkill {
	if tc == nil || tc.Config == nil || cfg == nil || !cfg.Skills.BuiltinInjectionEnabled() {
		return nil
	}

	var skills []agentV3BuiltinSkill
	if agentV3RichSkillAvailable(tc.Config, cfg) {
		skills = append(skills, agentV3BuiltinSkill{
			Name:        "rich-message",
			Description: "Render Telegram rich Markdown. Call load_skill before rich output, then finish with one rich envelope.",
			Content:     agentV3RichMessageSkillContract(true),
		})
	}
	sort.SliceStable(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills
}

func agentV3RichSkillAvailable(chatCfg *config.ChatConfigSingle, cfg *config.AgentV3Config) bool {
	if chatCfg == nil || cfg == nil || !cfg.Skills.BuiltinInjectionEnabled() {
		return false
	}
	return chatCfg.Agent != nil && chatCfg.Agent.Enable && chatCfg.Agent.V3 && chatCfg.Agent.Rich
}

func agentV3BuiltinSkillByName(name string, tc *TurnContext) (agentV3BuiltinSkill, bool) {
	cfg := (*config.AgentV3Config)(nil)
	if config.BotConfig != nil {
		cfg = config.BotConfig.AgentV3
	}
	if cfg == nil {
		cfg = &config.AgentV3Config{}
	}
	normalized := normalizeAgentV3SkillName(name)
	for _, skill := range buildAgentV3BuiltinSkills(tc, cfg) {
		if normalizeAgentV3SkillName(skill.Name) == normalized {
			return skill, true
		}
	}
	return agentV3BuiltinSkill{}, false
}

func normalizeAgentV3SkillName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	name = strings.ReplaceAll(name, "_", "-")
	return name
}

func buildAgentV3SkillPromptBlock(skills []agentV3BuiltinSkill) string {
	filtered := make([]agentV3BuiltinSkill, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		if name == "" || strings.TrimSpace(skill.Content) == "" {
			continue
		}
		filtered = append(filtered, agentV3BuiltinSkill{
			Name:        name,
			Description: strings.TrimSpace(skill.Description),
			Content:     strings.TrimSpace(skill.Content),
		})
	}
	if len(filtered) == 0 {
		return ""
	}

	sort.SliceStable(filtered, func(i, j int) bool {
		return filtered[i].Name < filtered[j].Name
	})
	var b strings.Builder
	b.WriteString("<agent_v3_skills>\n")
	b.WriteString("The following built-in skills are available for this chat, but they are not active until loaded with load_skill. Do not use read/grep to load skills from /skills.\n")
	b.WriteString("Rich output gate: before a final <telegram_rich_message> answer, call load_skill with name=\"rich-message\" during this turn.\n")
	for _, skill := range filtered {
		b.WriteString("<skill name=\"")
		b.WriteString(escapeAgentV3SkillAttr(skill.Name))
		b.WriteString("\" description=\"")
		b.WriteString(escapeAgentV3SkillAttr(skill.Description))
		b.WriteString("\" status=\"available\" activation=\"call_load_skill_before_final_output\" />\n")
	}
	b.WriteString("</agent_v3_skills>")
	return b.String()
}

func escapeAgentV3SkillAttr(s string) string {
	replacer := strings.NewReplacer(
		"&", "&amp;",
		"\"", "&quot;",
		"<", "&lt;",
		">", "&gt;",
	)
	return replacer.Replace(strings.TrimSpace(s))
}
