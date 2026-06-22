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
	if tc.Config.IsAgentV3RichEnabled() {
		skills = append(skills, agentV3BuiltinSkill{
			Name:        "rich-message",
			Description: "Render Telegram rich Markdown responses when useful.",
			Content:     agentV3RichMessageSkillContract(true),
		})
	}
	sort.SliceStable(skills, func(i, j int) bool {
		return skills[i].Name < skills[j].Name
	})
	return skills
}

func buildAgentV3SkillPromptBlock(skills []agentV3BuiltinSkill) string {
	filtered := make([]agentV3BuiltinSkill, 0, len(skills))
	for _, skill := range skills {
		name := strings.TrimSpace(skill.Name)
		content := strings.TrimSpace(skill.Content)
		if name == "" || content == "" {
			continue
		}
		filtered = append(filtered, agentV3BuiltinSkill{
			Name:        name,
			Description: strings.TrimSpace(skill.Description),
			Content:     content,
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
	b.WriteString("The following skills are already loaded into this system prompt. Do not use read/grep to load skills from /skills.\n")
	for _, skill := range filtered {
		b.WriteString("\n<skill name=\"")
		b.WriteString(escapeAgentV3SkillAttr(skill.Name))
		b.WriteString("\" description=\"")
		b.WriteString(escapeAgentV3SkillAttr(skill.Description))
		b.WriteString("\">\n")
		b.WriteString(skill.Content)
		b.WriteString("\n</skill>\n")
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
