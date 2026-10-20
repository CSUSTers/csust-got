package agentv3

import (
	"context"
	"encoding/json"

	"github.com/cloudwego/eino/schema"
)

type agentV3VisibleSkillsKey struct{}

type agentV3VisibleSkills struct {
	responses map[string]string
	ordered   []string
}

// Visibility belongs to one model input, not the TurnContext shared with subagents.
func withAgentV3VisibleSkills(ctx context.Context, input []*schema.Message) context.Context {
	visible := agentV3VisibleSkills{responses: make(map[string]string)}
	tc := GetTurnContext(ctx)
	if tc != nil && tc.V3 != nil {
		var calls map[string]schema.ToolCall
		for _, message := range input {
			if message == nil {
				calls = nil
				continue
			}
			switch message.Role {
			case schema.Assistant:
				calls = make(map[string]schema.ToolCall, len(message.ToolCalls))
				for _, call := range message.ToolCalls {
					if call.ID != "" {
						calls[call.ID] = call
					}
				}
			case schema.Tool:
				call, ok := calls[message.ToolCallID]
				delete(calls, message.ToolCallID)
				if !ok || call.Function.Name != agentV3ToolLoadSkill || message.ToolName != agentV3ToolLoadSkill {
					continue
				}
				var args loadSkillArgs
				if json.Unmarshal([]byte(call.Function.Arguments), &args) != nil {
					continue
				}
				name, err := parseAgentV3CanonicalSkillName(args.Name)
				if err != nil {
					continue
				}
				skill, ok := tc.V3.SkillCatalog.ByName[name]
				if ok && message.Content == agentV3LoadedSkillResponse(skill) {
					if _, seen := visible.responses[name]; !seen {
						visible.ordered = append(visible.ordered, name)
					}
					visible.responses[name] = message.Content
				}
			default:
				calls = nil
			}
		}
	}
	return context.WithValue(ctx, agentV3VisibleSkillsKey{}, visible)
}

func restoreAgentV3VisibleSkills(ctx context.Context, admission *agentV3SkillAdmission) {
	visible, _ := ctx.Value(agentV3VisibleSkillsKey{}).(agentV3VisibleSkills)
	state := agentV3SkillsForContext(ctx)
	if state == nil {
		return
	}
	for _, name := range visible.ordered {
		if name == agentV3RichMessageSkillName {
			continue
		}
		skill, ok := admission.skill(GetTurnContext(ctx), name)
		if ok && visible.responses[name] == agentV3LoadedSkillResponse(skill) {
			state.activate(skill)
		}
	}
}
