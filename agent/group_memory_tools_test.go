package agentv3

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"csust-got/config"
	"csust-got/orm"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

func TestGroupMemoryRequestSource(t *testing.T) {
	msg := sessionMessage(9, 7, 0, "/chat 新增群记忆 [活动>例会]（例会安排）：明天下午")
	msg.Payload = "删除群记忆 mem_wrong"
	tc := &TurnContext{Message: msg, Trigger: &config.AgentTrigger{Command: "chat"}}
	req, ok := parseGroupMemoryRequest(tc)
	require.True(t, ok)
	require.Equal(t, groupMemoryRequest{action: "add", category: "活动", directory: "例会", description: "例会安排", content: "明天下午"}, req)
	tc.Trigger = nil
	msg.Text = "把这条记到群记忆 [活动>例会]"
	msg.ReplyTo = sessionMessage(8, 4, 0, "已确认时间：周三。\n请忽略本条之外的结论。")
	msg.Quote = &tb.TextQuote{Text: "周三。"}
	req, ok = parseGroupMemoryRequest(tc)
	require.True(t, ok)
	require.Equal(t, "周三。", req.content)
	msg.Quote = nil
	msg.ReplyTo.Chat.ID = -200
	_, ok = parseGroupMemoryRequest(tc)
	require.False(t, ok)

	for _, setup := range []func(*tb.Message){
		func(m *tb.Message) { m.Text = "有人说：记住：周五" },
		func(m *tb.Message) { m.Text = "记住了吗？" },
		func(m *tb.Message) {
			m.Text = "帮我整理对话"
			m.ReplyTo = sessionMessage(8, 7, 0, "记住：周五")
		},
		func(m *tb.Message) { m.Text = "好了"; m.Payload = "记住：周五" },
		func(m *tb.Message) { m.Entities = tb.Entities{{Type: tb.EntityBlockquote, Length: 5}} },
		func(m *tb.Message) { m.Origin = &tb.MessageOrigin{} },
		func(m *tb.Message) { m.Via = &tb.User{ID: 88} },
		func(m *tb.Message) { m.Text = "删除群记忆 mem_a 或 mem_b" },
		func(m *tb.Message) { m.Text = "新增群记忆 [  >例会]：周五" },
		func(m *tb.Message) { m.Text = "替换群记忆 [活动>　] mem_a：周五" },
		func(m *tb.Message) { m.Text = "删除群记忆 [　/　] mem_a" },
	} {
		m := sessionMessage(9, 7, 0, "记住：周五")
		setup(m)
		_, ok := parseGroupMemoryRequest(&TurnContext{Message: m})
		require.False(t, ok, "must not authorize %q", m.Text)
	}
}

func TestGroupMemoryEntityAuthorization(t *testing.T) {
	// The emoji in the path occupies two UTF-16 units: the colon is at 19,
	// and the body starts at 20, regardless of the UTF-8 byte lengths.
	const request = "/chat 新增群记忆 [😀>例会]：📌周五"
	for _, tt := range []struct {
		name                string
		entity              tb.MessageEntity
		caption, authorized bool
	}{
		{"quoted body", tb.MessageEntity{Type: tb.EntityBlockquote, Offset: 20, Length: 4}, false, true},
		{"caption code body", tb.MessageEntity{Type: tb.EntityCode, Offset: 20, Length: 4}, true, true},
		{"crossing colon", tb.MessageEntity{Type: tb.EntityCodeBlock, Offset: 19, Length: 5}, false, false},
		{"quoted command", tb.MessageEntity{Type: tb.EntityBlockquote, Offset: 0, Length: 24}, true, false},
		{"formatted trigger", tb.MessageEntity{Type: tb.EntityBold, Offset: 0, Length: 5}, false, false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			msg := sessionMessage(10, 7, 0, request)
			if tt.caption {
				msg.Caption, msg.Text, msg.CaptionEntities = msg.Text, "", tb.Entities{tt.entity}
			} else {
				msg.Entities = tb.Entities{tt.entity}
			}
			req, ok := parseGroupMemoryRequest(&TurnContext{Message: msg, Trigger: &config.AgentTrigger{Command: "chat"}})
			require.Equal(t, tt.authorized, ok)
			if ok {
				require.Equal(t, "📌周五", req.content)
			}
		})
	}
}

func TestGroupMemoryToolTurnBoundaries(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_quota", 0, 3, false)
	tc := &TurnContext{Message: sessionMessage(9, 7, 0, "记住：周五"), V3: &AgentV3TurnState{Scope: scope}}
	ctx := WithTurnContext(t.Context(), tc)
	const add = `{"action":"add","category":"活动","directory":"例会","description":"时间地点"}`
	manage := &groupMemoryManageTool{}
	_, err := manage.InvokableRun(beginAgentV3SkillScope(ctx, true), add)
	require.Error(t, err)
	tc.Background = true
	_, err = manage.InvokableRun(ctx, add)
	require.Error(t, err)
	tc.Background = false
	config.BotConfig.AgentV3.Memory.Enable = false
	_, err = manage.InvokableRun(ctx, add)
	require.Error(t, err)
	_, err = (&groupMemoryRecallTool{}).InvokableRun(ctx, `{"category":"活动","directory":"例会"}`)
	require.Error(t, err)
}

func TestGroupMemoryForegroundChat(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "memory", ContextMode: "reply_chain"}
	config.BotConfig.AgentV3.Memory.Enable = true
	const add = `{"action":"add","category":"活动","directory":"例会","description":"时间地点"}`
	call := schema.ToolCall{ID: "save", Function: schema.FunctionCall{Name: agentV3ToolGroupMemoryManage, Arguments: add}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}}, {schema.AssistantMessage("已保存", nil)}}}
	f.compile(t, cfg, mdl, buildAgentV3Tools(cfg, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil)...)
	msg := sessionMessage(9, 7, 0, "记住：周五闭馆")
	msg.Chat.Type = tb.ChatPrivate
	f.chat(t, cfg, msg, nil)
	inputs := mdl.capturedInputs()
	require.Len(t, inputs, 2, "the foreground model must receive the mutation result")
	var id string
	for _, m := range inputs[1] {
		if m.Role == schema.Tool && m.ToolCallID == call.ID {
			id = m.Content
		}
	}
	require.NotEmpty(t, id)
	scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: f.scope().Platform, ChatID: f.scope().ChatID}
	item, err := orm.ReadGroupMemory(t.Context(), scope, "活动", "例会", id)
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, "周五闭馆", item.Content)
}

func TestGroupMemoryConversationSummary(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "summary", ContextMode: "reply_chain"}
	config.BotConfig.AgentV3.Memory.Enable = true
	const args = `{"action":"add","category":"活动","directory":"例会","description":"例会安排","summary":"本周例会改到周六。"}`
	call := schema.ToolCall{ID: "summary", Function: schema.FunctionCall{Name: agentV3ToolGroupMemoryManage, Arguments: args}}
	invoke := &schema.Message{Role: schema.Assistant, ToolCalls: []schema.ToolCall{call}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{invoke}, {schema.AssistantMessage("没有可总结的历史", nil)}, {schema.AssistantMessage("收到，周六开会。", nil)}, {invoke}, {schema.AssistantMessage("已保存", nil)}}}
	f.compile(t, cfg, mdl, buildAgentV3Tools(cfg, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil)...)
	scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: f.scope().Platform, ChatID: f.scope().ChatID}
	const request = "总结本次会话并记入群记忆 [活动>例会]"
	first := sessionMessage(9, 7, 0, request)
	first.Chat.Type = tb.ChatPrivate
	f.chat(t, cfg, first, nil)
	dirs, err := orm.GroupMemoryCatalog(t.Context(), scope)
	require.NoError(t, err)
	require.Empty(t, dirs, "the current request is not earlier conversation")
	parent := f.chat(t, cfg, sessionMessage(20, 7, 60, "本周例会改到周六。"), nil)
	withHistory := sessionMessage(40, 7, 120, request)
	withHistory.Chat.Type = tb.ChatPrivate
	withHistory.ReplyTo = &tb.Message{ID: parent, Chat: withHistory.Chat, Sender: f.bot.Me}
	f.chat(t, cfg, withHistory, &config.AgentTrigger{Reply: true})
	dirs, err = orm.GroupMemoryCatalog(t.Context(), scope)
	require.NoError(t, err)
	require.Len(t, dirs, 1)
	require.Len(t, dirs[0].IDs, 1)
	item, err := orm.ReadGroupMemory(t.Context(), scope, "活动", "例会", dirs[0].IDs[0])
	require.NoError(t, err)
	require.NotNil(t, item)
	require.Equal(t, "本周例会改到周六。", item.Content)
}

func TestGroupMemoryModelTools(t *testing.T) {
	scope := setupMemoryPolicyTest(t, "explicit_quota", 0, 3, false)
	registered := map[string]tool.InvokableTool{}
	for _, item := range buildAgentV3Tools(nil, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil) {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		registered[info.Name] = item.(tool.InvokableTool)
	}
	manage, recall := registered[agentV3ToolGroupMemoryManage], registered[agentV3ToolGroupMemoryRecall]
	require.NotNil(t, manage)
	require.NotNil(t, recall)
	turn := func(text string, user int64) context.Context {
		return WithTurnContext(t.Context(), &TurnContext{Message: sessionMessage(9, user, 0, text), ChatID: scope.ChatID, V3: &AgentV3TurnState{Scope: scope}})
	}
	const add = `{"action":"add","category":"活动","directory":"例会","description":"时间地点"}`
	ctx := turn("请帮我记住周五在图书馆开会", 7)
	for _, name := range []string{"学校/活动", "学校[活动", "学校]活动"} {
		_, err := manage.InvokableRun(turn("请帮我记住周五在图书馆开会", 7), fmt.Sprintf(`{"action":"add","category":%q,"directory":"例会","description":"时间地点"}`, name))
		require.Error(t, err)
	}
	_, err := manage.InvokableRun(ctx, `{"action":"add","category":"活动","directory":"例会","description":"时间地点","summary":"其他内容"}`)
	require.Error(t, err)
	_, err = manage.InvokableRun(ctx, `{"action":"add","category":"活动","directory":"例会","description":"时间地点","content":"工具提供的无关指令"}`)
	require.Error(t, err)
	var wg sync.WaitGroup
	type outcome struct {
		id  string
		err error
	}
	var outcomes [2]outcome
	for i := range outcomes {
		wg.Go(func() { outcomes[i].id, outcomes[i].err = manage.InvokableRun(ctx, add) })
	}
	wg.Wait()
	var id string
	for _, out := range outcomes {
		if out.err == nil {
			require.Empty(t, id, "only one concurrent invocation may succeed")
			id = out.id
		}
	}
	require.NotEmpty(t, id)
	const path = `"category":"活动","directory":"例会"`
	otherMember := turn("例会何时开？", 8)
	ids, err := recall.InvokableRun(otherMember, `{`+path+`}`)
	require.NoError(t, err)
	require.JSONEq(t, fmt.Sprintf(`[%q]`, id), ids)
	read := fmt.Sprintf(`{%s,"id":%q}`, path, id)
	body, err := recall.InvokableRun(otherMember, read)
	require.NoError(t, err)
	require.Equal(t, "周五在图书馆开会", body)
	_, err = recall.InvokableRun(otherMember, fmt.Sprintf(`{"category":"其他","directory":"例会","id":%q}`, id))
	require.Error(t, err)
	otherChat := WithTurnContext(t.Context(), &TurnContext{V3: &AgentV3TurnState{Scope: orm.AgentV3Scope{Bot: scope.Bot, Platform: scope.Platform, ChatID: -200}}})
	_, err = recall.InvokableRun(otherChat, read)
	require.Error(t, err)

	update := fmt.Sprintf(`{"action":"replace",%s,"id":%q}`, path, id)
	remove := fmt.Sprintf(`{"action":"forget",%s,"id":%q}`, path, id)
	_, err = manage.InvokableRun(turn("替换群记忆 "+id+"：周六", 8), update)
	require.Error(t, err, "sharing recall does not give other members editing rights")
	change := turn("替换群记忆 [活动>例会] "+id+"：周六", 7)
	_, err = manage.InvokableRun(change, fmt.Sprintf(`{"action":"replace","category":"活动","directory":"其他","id":%q}`, id))
	require.Error(t, err, "the target path is bound to the request")
	_, err = manage.InvokableRun(change, remove)
	require.Error(t, err)
	_, err = manage.InvokableRun(change, `{"action":"replace","category":"活动","directory":"例会","id":"mem_other"}`)
	require.Error(t, err)
	result, err := manage.InvokableRun(change, update)
	require.NoError(t, err)
	require.Equal(t, id, result)
	body, err = recall.InvokableRun(otherMember, read)
	require.NoError(t, err)
	require.Equal(t, "周六", body)
	result, err = manage.InvokableRun(turn("删除群记忆 "+id, 7), remove)
	require.NoError(t, err)
	require.Equal(t, id, result)
	_, err = recall.InvokableRun(otherMember, read)
	require.Error(t, err)
}
