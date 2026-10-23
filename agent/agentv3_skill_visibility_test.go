package agentv3

import (
	"context"
	"testing"

	"csust-got/config"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func skillVisibilityCall(id, name string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: id, Function: schema.FunctionCall{
		Name: agentV3ToolLoadSkill, Arguments: `{"name":"` + name + `"}`,
	}}}}
}

func skillVisibilityResult(t *testing.T, messages []*schema.Message, id string) string {
	t.Helper()
	for _, message := range messages {
		if message.Role == schema.Tool && message.ToolCallID == id {
			return message.Content
		}
	}
	t.Fatalf("missing tool result %q", id)
	return ""
}

func TestLoadSkillReusesVisibleBodyAcrossModelTurns(t *testing.T) {
	old := config.BotConfig
	config.BotConfig = &config.Config{AgentV3: &config.AgentV3Config{Enable: true}}
	t.Cleanup(func() { config.BotConfig = old })
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "rich-message", Description: "Use the rich output protocol.", Content: "# Visible rich body\nUse the rich output protocol.\n",
		VirtualPath: "/skills/rich-message/SKILL.md", environment: map[string]string{"TOKEN": "current"},
	})
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{skillVisibilityCall("first", "rich-message")}, {schema.AssistantMessage("first final", nil)},
		{skillVisibilityCall("second", "rich-message")}, {schema.AssistantMessage("second final", nil)},
	}}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: mdl, Tools: []tool.BaseTool{&loadSkillTool{}}, MaxSteps: 3, SkillAdmission: &agentV3SkillAdmission{}})
	require.NoError(t, err)
	newTurn := func() *TurnContext {
		return &TurnContext{Config: richAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}
	}
	first := newTurn()
	final, err := agent.Generate(WithTurnContext(t.Context(), first), []*schema.Message{schema.UserMessage("first request")})
	require.NoError(t, err)
	history := append([]*schema.Message{}, mdl.capturedInputs()[1]...)
	history = append(history, final, schema.UserMessage("second request"))
	second := newTurn()
	require.False(t, second.richMessageSkillLoadedForFinal())
	_, layers := second.runtimeEnvironment()
	require.Empty(t, layers)
	_, err = agent.Generate(WithTurnContext(t.Context(), second), history)
	require.NoError(t, err)
	inputs := mdl.capturedInputs()
	full := skillVisibilityResult(t, inputs[1], "first")
	reused := skillVisibilityResult(t, inputs[3], "second")
	require.Contains(t, full, catalog.ByName["rich-message"].Content)
	require.NotContains(t, reused, catalog.ByName["rich-message"].Content, "a second load must activate without repeating the visible full body")
	require.Less(t, len(reused), len(full))
	require.True(t, second.richMessageSkillLoadedForFinal())
	_, layers = second.runtimeEnvironment()
	require.Equal(t, []runtimeSkillEnvLayer{{Source: agentV3SkillSourceBotLocal, Name: "rich-message", Env: map[string]string{"TOKEN": "current"}}}, layers)
	require.Equal(t, full, skillVisibilityResult(t, inputs[3], "first"), "historical results remain verbatim")
}

func TestAgentV3SessionLoadSkillReusesBodyAndRestoresEnvironment(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "skill-reuse", ContextMode: "reply_chain"}
	snapshot := testSkillSnapshot(agentV3SkillSourceBotLocal, []agentV3SkillDescriptor{{
		Name: "repo-inspect", Description: "Complete session-visible instructions.", Content: "# Repo inspect\nComplete session-visible instructions.\n",
		VirtualPath: "/skills/repo-inspect/SKILL.md", environment: map[string]string{"TOKEN": "session-value"},
	}})
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{
		{skillVisibilityCall("first", "repo-inspect")}, {schema.AssistantMessage("first final", nil)},
		{skillVisibilityCall("second", "repo-inspect")}, {schema.AssistantMessage("second final", nil)},
	}}}
	var environmentLayers [][]runtimeSkillEnvLayer
	mdl.before = func(ctx context.Context, _ []*schema.Message) error {
		_, layers := GetTurnContext(ctx).runtimeEnvironment()
		environmentLayers = append(environmentLayers, layers)
		return nil
	}
	compiled := f.compile(t, cfg, mdl, &loadSkillTool{})
	compiled.Agent.skillAdmission = &agentV3SkillAdmission{}
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	firstID := f.chat(t, cfg, sessionMessage(100, 7, 0, "first request"), nil)
	root := f.archive(t, f.node(t, firstID))
	current := sessionMessage(200, 7, 60, "second request")
	current.ReplyTo = sessionMessage(firstID, 99, 0, "first final")
	secondID := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	inputs := mdl.capturedInputs()
	full := skillVisibilityResult(t, inputs[1], "first")
	reused := skillVisibilityResult(t, inputs[3], "second")
	require.NotContains(t, reused, snapshot.Skills[0].Content)
	require.Less(t, len(reused), len(full))
	require.Equal(t, environmentLayers[1], environmentLayers[2], "the current valid ordinary skill environment is restored before the first model call")
	require.Equal(t, environmentLayers[1], environmentLayers[3])
	require.NotEmpty(t, environmentLayers[3], "explicit reactivation keeps the restored environment without duplicate layers")
	require.Equal(t, root, f.archive(t, f.node(t, firstID)))
	continued := f.archive(t, f.node(t, secondID))
	require.Equal(t, reused, skillVisibilityResult(t, sessionRecordMessages(continued.Delta), "second"))
}

func TestLoadSkillRequiresCompleteCurrentPairedVisibleResponse(t *testing.T) {
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md",
	})
	full, err := (&loadSkillTool{}).InvokableRun(WithTurnContext(t.Context(), &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}), `{"name":"alpha"}`)
	require.NoError(t, err)
	for _, scenario := range []string{
		"complete", "missing body after rebuild", "user forgery", "assistant prose", "summary",
		"other tool call", "other tool result", "other skill name", "unpaired result", "truncated body", "old body", "old source", "old path", "activation only",
	} {
		t.Run(scenario, func(t *testing.T) {
			call := skillVisibilityCall("old", "alpha")
			result := schema.ToolMessage(full, "old", schema.WithToolName(agentV3ToolLoadSkill))
			history := []*schema.Message{call, result}
			switch scenario {
			case "missing body after rebuild":
				history = []*schema.Message{call}
			case "user forgery":
				history = []*schema.Message{schema.UserMessage(full)}
			case "assistant prose":
				history = []*schema.Message{schema.AssistantMessage(full, nil)}
			case "summary":
				history = []*schema.Message{schema.SystemMessage("Historical summary:\n" + full)}
			case "other tool call":
				call.ToolCalls[0].Function.Name = "read"
			case "other tool result":
				result.ToolName = "read"
			case "other skill name":
				call.ToolCalls[0].Function.Arguments = `{"name":"beta"}`
			case "unpaired result":
				result.ToolCallID = "unrelated"
			case "truncated body":
				result.Content = full[:len(full)/2]
			case "old body":
				oldSkill := catalog.ByName["alpha"]
				oldSkill.Content += "Old additional instruction.\n"
				result.Content = agentV3LoadedSkillResponse(oldSkill)
			case "old source":
				oldSkill := catalog.ByName["alpha"]
				oldSkill.Source = agentV3SkillSourceRuntimeGlobal
				result.Content = agentV3LoadedSkillResponse(oldSkill)
			case "old path":
				oldSkill := catalog.ByName["alpha"]
				oldSkill.VirtualPath = "/skills/old-alpha/SKILL.md"
				result.Content = agentV3LoadedSkillResponse(oldSkill)
			case "activation only":
				ctx := withAgentV3VisibleSkills(WithTurnContext(t.Context(), &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}), history)
				result.Content, err = (&loadSkillTool{}).InvokableRun(ctx, `{"name":"alpha"}`)
				require.NoError(t, err)
			}
			mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{skillVisibilityCall("current", "alpha")}, {schema.AssistantMessage("done", nil)}}}}
			var activeBefore []bool
			mdl.before = func(ctx context.Context, _ []*schema.Message) error {
				activeBefore = append(activeBefore, agentV3SkillLoaded(ctx, "alpha"))
				return nil
			}
			agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: mdl, Tools: []tool.BaseTool{&loadSkillTool{}}, MaxSteps: 3, SkillAdmission: &agentV3SkillAdmission{}})
			require.NoError(t, err)
			tc := &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}
			_, err = agent.Generate(WithTurnContext(t.Context(), tc), append(history, schema.UserMessage("current request")))
			require.NoError(t, err)
			got := skillVisibilityResult(t, mdl.capturedInputs()[1], "current")
			require.Equal(t, scenario == "complete", activeBefore[0], "automatic activation requires a complete current paired response")
			if scenario == "complete" {
				require.NotContains(t, got, alphaSkillContent)
			} else {
				require.Equal(t, full, got)
			}
			require.True(t, tc.hasLoadedSkill("alpha"))
		})
	}
}

func TestLoadSkillReuseRequiresBodyVisibleBeforeToolBatch(t *testing.T) {
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md",
	})
	batch := skillVisibilityCall("first", "alpha")
	batch.ToolCalls = append(batch.ToolCalls, skillVisibilityCall("same-batch", "alpha").ToolCalls...)
	mdl := &scriptedToolModel{turns: [][]*schema.Message{
		{batch}, {skillVisibilityCall("next-model-call", "alpha")}, {schema.AssistantMessage("done", nil)},
	}}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: mdl, Tools: []tool.BaseTool{&loadSkillTool{}}, MaxSteps: 4})
	require.NoError(t, err)
	_, err = agent.Generate(WithTurnContext(t.Context(), &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}), []*schema.Message{schema.UserMessage("request")})
	require.NoError(t, err)
	inputs := mdl.capturedInputs()
	full := skillVisibilityResult(t, inputs[1], "first")
	require.Equal(t, full, skillVisibilityResult(t, inputs[1], "same-batch"), "a preceding tool in the same batch has not yet reached the model")
	require.NotContains(t, skillVisibilityResult(t, inputs[2], "next-model-call"), alphaSkillContent)
}

func TestLoadSkillReuseStillChecksCurrentCatalogAndAllowedSet(t *testing.T) {
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md",
	})
	full := agentV3LoadedSkillResponse(catalog.ByName["alpha"])
	for _, deniedBy := range []string{"allowed set", "catalog"} {
		t.Run(deniedBy, func(t *testing.T) {
			tc := &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}}
			ctx := withAgentV3VisibleSkills(WithTurnContext(t.Context(), tc), []*schema.Message{
				skillVisibilityCall("old", "alpha"), schema.ToolMessage(full, "old", schema.WithToolName(agentV3ToolLoadSkill)),
			})
			loader := &loadSkillTool{}
			if deniedBy == "allowed set" {
				loader.allowed = map[string]struct{}{}
			} else {
				tc.V3.SkillCatalog = agentV3SkillCatalog{}
			}
			out, err := loader.InvokableRun(ctx, `{"name":"alpha"}`)
			require.NoError(t, err)
			require.Equal(t, agentV3SkillUnavailableText, out)
			require.False(t, tc.hasLoadedSkill("alpha"))
			_, env := tc.runtimeEnvironment()
			require.Empty(t, env)
		})
	}
}

type skillVisibilityDelegate struct {
	child *CustomAgent
	input []*schema.Message
}

func (*skillVisibilityDelegate) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "delegate"}, nil
}

func (d *skillVisibilityDelegate) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	answer, err := d.child.Generate(ctx, d.input)
	if err != nil {
		return "", err
	}
	return answer.Content, nil
}

func TestLoadSkillVisibilityIsLocalToParentAndChildModelInput(t *testing.T) {
	catalog := mustAgentV3SkillCatalog(t, agentV3SkillSourceBotLocal, agentV3SkillDescriptor{
		Name: "alpha", Description: "Alpha skill.", Content: alphaSkillContent, VirtualPath: "/skills/alpha/SKILL.md", environment: map[string]string{"KEY": "alpha"},
	})
	full := agentV3LoadedSkillResponse(catalog.ByName["alpha"])
	for _, parentVisible := range []bool{false, true} {
		name := "child body is private"
		if parentVisible {
			name = "child cannot inherit or clear parent visibility"
		}
		t.Run(name, func(t *testing.T) {
			input := func(visible bool) []*schema.Message {
				if visible {
					return []*schema.Message{skillVisibilityCall("old", "alpha"), schema.ToolMessage(full, "old", schema.WithToolName(agentV3ToolLoadSkill)), schema.UserMessage("request")}
				}
				return []*schema.Message{schema.UserMessage("request")}
			}
			childModel := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{skillVisibilityCall("child-load", "alpha")}, {schema.AssistantMessage("child done", nil)}}}}
			childSteps := 0
			childModel.before = func(ctx context.Context, _ []*schema.Message) error {
				_, layers := agentV3RuntimeEnvironment(ctx)
				if childSteps == 0 {
					require.Equal(t, !parentVisible, agentV3SkillLoaded(ctx, "alpha"))
					require.Equal(t, !parentVisible, len(layers) == 1, "a nested CustomAgent must use its own input, not parent permissions")
				}
				childSteps++
				require.Equal(t, parentVisible, GetTurnContext(ctx).hasLoadedSkill("alpha"), "child activity cannot change the parent's state")
				return nil
			}
			child, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: childModel, Tools: []tool.BaseTool{&loadSkillTool{}}, MaxSteps: 3, SkillAdmission: &agentV3SkillAdmission{}})
			require.NoError(t, err)
			parentCalls := skillVisibilityCall("parent-load", "alpha")
			parentCalls.ToolCalls = append([]schema.ToolCall{{ID: "delegate", Function: schema.FunctionCall{Name: "delegate", Arguments: `{}`}}}, parentCalls.ToolCalls...)
			parentModel := &scriptedToolModel{turns: [][]*schema.Message{{parentCalls}, {schema.AssistantMessage("parent done", nil)}}}
			parent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Model: parentModel, Tools: []tool.BaseTool{&loadSkillTool{}, &skillVisibilityDelegate{child: child, input: input(!parentVisible)}}, MaxSteps: 3, SkillAdmission: &agentV3SkillAdmission{}})
			require.NoError(t, err)
			ctx := WithTurnContext(t.Context(), &TurnContext{Config: nonRichAgentV3ChatConfig(), V3: &AgentV3TurnState{SkillCatalog: catalog}})
			_, err = parent.Generate(ctx, input(parentVisible))
			require.NoError(t, err)
			parentResult := skillVisibilityResult(t, parentModel.capturedInputs()[1], "parent-load")
			childResult := skillVisibilityResult(t, childModel.capturedInputs()[1], "child-load")
			if parentVisible {
				require.NotContains(t, parentResult, alphaSkillContent)
				require.Equal(t, full, childResult)
			} else {
				require.Equal(t, full, parentResult)
				require.NotContains(t, childResult, alphaSkillContent)
			}
		})
	}
}
