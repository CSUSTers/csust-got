package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"text/template"
	"time"

	"csust-got/agent/session"
	"csust-got/config"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

var (
	errAgentSessionFixtureModel  = errors.New("fixture model error")
	errAgentSessionFixtureParent = errors.New("expected a loaded parent")
)

type agentSessionFixture struct {
	mini         *miniredis.Miniredis
	client       *redis.Client
	repo         *orm.AgentV3SessionRepository
	files        *session.FileStore
	service      *session.Service
	directory    string
	bot          *tb.Bot
	mu           sync.Mutex
	nextID       int
	finalID      int
	failDelivery bool
}

func newAgentSessionFixture(t *testing.T) *agentSessionFixture {
	t.Helper()
	f := &agentSessionFixture{mini: setupReplySessionRedis(t), directory: t.TempDir(), nextID: 1000}
	config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
	config.BotConfig.AgentV3.Enable = true
	config.BotConfig.AgentV3.Session.Directory = f.directory
	config.BotConfig.WhiteListConfig.Enabled = false
	f.client = redis.NewClient(&redis.Options{Addr: f.mini.Addr()})
	t.Cleanup(func() { _ = f.client.Close() })
	var err error
	f.repo, err = orm.NewAgentV3SessionRepository(f.client, config.BotConfig.RedisConfig.KeyPrefix)
	require.NoError(t, err)
	f.files, err = session.NewFileStore(f.directory)
	require.NoError(t, err)
	f.service, err = session.NewService(f.repo, f.files, session.Options{TTL: time.Hour})
	require.NoError(t, err)
	oldService := agentSessionService.Load()
	done := make(chan struct{})
	close(done)
	agentSessionService.Store(&agentV3SessionService{service: f.service, cancel: func() {}, done: done})
	oldAgents := snapshotCompiledAgents()
	clearCompiledAgents()
	t.Cleanup(func() {
		_ = f.service.Close()
		agentSessionService.Store(oldService)
		restoreCompiledAgents(oldAgents)
	})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "bad fixture request", http.StatusBadRequest)
			return
		}
		f.mu.Lock()
		defer f.mu.Unlock()
		if strings.HasSuffix(r.URL.Path, "/sendChatAction") || strings.HasSuffix(r.URL.Path, "/deleteMessage") {
			_, _ = w.Write([]byte(`{"ok":true,"result":true}`))
			return
		}
		if f.failDelivery {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"fixture send failure"}`))
			return
		}
		f.nextID++
		id := f.nextID
		if value, ok := payload["message_id"]; ok {
			_, _ = fmt.Sscan(fmt.Sprint(value), &id)
		}
		f.finalID = id
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":-100},"from":{"id":99,"is_bot":true}}}`, id)
	}))
	t.Cleanup(server.Close)
	f.bot, err = tb.NewBot(tb.Settings{Token: "session-fixture-token", Offline: true, URL: server.URL, Client: server.Client()})
	require.NoError(t, err)
	f.bot.Me = &tb.User{ID: 99, Username: "sessionbot", IsBot: true}
	config.BotConfig.Bot = f.bot
	return f
}

func (f *agentSessionFixture) scope() session.Scope {
	return session.Scope{Namespace: f.repo.Namespace(), Bot: f.bot.Me.Username, Platform: agentV3Platform, ChatID: -100}
}

func (f *agentSessionFixture) compile(t *testing.T, cfg *config.AgentConfig, mdl model.ToolCallingChatModel, tools ...tool.BaseTool) *CompiledAgent {
	t.Helper()
	cfg.Model = &config.Model{Model: "fixture"}
	if cfg.Agent == nil {
		cfg.Agent = &config.AgentOptions{Enable: true}
	}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: cfg.Name, Model: mdl, Tools: tools, MaxSteps: 4})
	require.NoError(t, err)
	compiled := &CompiledAgent{Name: cfg.Name, Config: cfg, Agent: agent, SystemTemplate: template.Must(template.New("system").Parse("SYSTEM_" + cfg.Name))}
	compiledAgents.Store(cfg.Name, compiled)
	return compiled
}

func (f *agentSessionFixture) chat(t *testing.T, cfg *config.AgentConfig, message *tb.Message, trigger *config.AgentTrigger) int {
	t.Helper()
	if trigger != nil && trigger.Command != "" {
		message.Payload = message.Text
	}
	f.mu.Lock()
	if message.ID <= f.nextID {
		message.ID = f.nextID + 100
	}
	f.nextID = message.ID
	f.mu.Unlock()
	require.NotEmpty(t, extractInput(message, trigger))
	require.NoError(t, Chat(f.bot.NewContext(tb.Update{Message: message}), cfg, trigger))
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.finalID
}

func (f *agentSessionFixture) node(t *testing.T, id int) session.Node {
	t.Helper()
	token, err := session.NewID()
	require.NoError(t, err)
	pinned, err := f.repo.ResolveAndPin(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: id}, token, time.Minute)
	require.NoError(t, err)
	require.NoError(t, f.repo.Release(t.Context(), f.scope(), pinned.Lease))
	return pinned.Nodes[len(pinned.Nodes)-1]
}

func (f *agentSessionFixture) archive(t *testing.T, node session.Node) session.TurnCapture {
	t.Helper()
	var capture session.TurnCapture
	require.NoError(t, f.files.WithScopeLock(t.Context(), f.scope(), func(files *session.ScopeFiles) error {
		var err error
		capture, err = files.Read(node)
		return err
	}))
	return capture
}

func sessionRecordMessages(records []session.Record) []*schema.Message {
	messages := make([]*schema.Message, 0, len(records))
	for _, record := range records {
		messages = append(messages, record.Message)
	}
	return messages
}

func sessionConversation(messages []*schema.Message) []*schema.Message {
	var out []*schema.Message
	for _, message := range messages {
		if message.Role != schema.System {
			out = append(out, message)
		}
	}
	return out
}

func sessionToolMessage(id string) *schema.Message {
	return &schema.Message{Role: schema.Assistant, ReasoningContent: "tool reasoning " + id, ToolCalls: []schema.ToolCall{{ID: id, Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{"query":"` + id + `"}`}}}}
}

func TestAgentV3SessionChatToolReplayAndBranches(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		t.Run(fmt.Sprintf("stream=%t", streaming), func(t *testing.T) {
			f := newAgentSessionFixture(t)
			cfg := &config.AgentConfig{Name: "first", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: streaming}}
			firstCalls := sessionToolMessage("first-call")
			firstCalls.ToolCalls = append(firstCalls.ToolCalls, schema.ToolCall{ID: "parallel-call", Type: "function", Function: schema.FunctionCall{Name: "lookup", Arguments: `{}`}})
			firstFinal := schema.AssistantMessage("FIRST_FINAL", nil)
			firstFinal.ReasoningContent = "FIRST_REASONING"
			mdl := &scriptedToolModel{turns: [][]*schema.Message{{firstCalls}, {firstFinal}, {sessionToolMessage("second-call")}, {schema.AssistantMessage("SECOND_FINAL", nil)}, {schema.AssistantMessage("SIBLING_FINAL", nil)}}}
			counter := &countingLookupTool{}
			f.compile(t, cfg, mdl, counter)
			firstID := f.chat(t, cfg, sessionMessage(10, 7, 0, "FIRST_INPUT"), &config.AgentTrigger{Command: "ask"})
			root := f.node(t, firstID)
			require.Nil(t, root.Parent)
			rootCapture := f.archive(t, root)
			require.Len(t, rootCapture.Delta, 5)
			require.Equal(t, firstCalls, rootCapture.Delta[1].Message)
			require.Equal(t, firstFinal, rootCapture.Delta[4].Message)
			second := sessionMessage(20, 8, 60, "SECOND_INPUT")
			second.ReplyTo = &tb.Message{ID: firstID, Text: "UNTRUSTED_EMBEDDED_ANCESTOR"}
			secondID := f.chat(t, cfg, second, &config.AgentTrigger{Reply: true})
			child := f.node(t, secondID)
			require.Equal(t, &root.Ref, child.Parent)
			childCapture := f.archive(t, child)
			require.Empty(t, childCapture.Bootstrap)
			require.Len(t, childCapture.Delta, 4)
			input := mdl.capturedInputs()[2]
			conversation := sessionConversation(input)
			require.Equal(t, sessionRecordMessages(rootCapture.Delta), conversation[:len(rootCapture.Delta)])
			text := replySessionSchemaText(input)
			require.Equal(t, 1, strings.Count(text, "SECOND_INPUT"))
			require.NotContains(t, text, "UNTRUSTED_EMBEDDED_ANCESTOR")
			require.NotContains(t, text, "<agent_runtime_guidance>", "replayed tool rounds must not consume the new invocation's budget")
			counter.mu.Lock()
			require.Equal(t, 3, counter.calls)
			counter.mu.Unlock()
			sibling := sessionMessage(30, 9, 120, "SIBLING_INPUT")
			sibling.ReplyTo = &tb.Message{ID: firstID}
			siblingID := f.chat(t, cfg, sibling, &config.AgentTrigger{Reply: true})
			require.Equal(t, &root.Ref, f.node(t, siblingID).Parent)
			require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[4]), "SECOND_INPUT")
			cross := &config.AgentConfig{Name: "cross", ContextMode: "reply_chain"}
			crossModel := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("CROSS_FINAL", nil)}}}
			f.compile(t, cross, crossModel)
			crossID := f.chat(t, cross, second, &config.AgentTrigger{Reply: true})
			require.Equal(t, &root.Ref, f.node(t, crossID).Parent)
			crossInput := crossModel.capturedInputs()[0]
			require.Contains(t, crossInput[0].Content, "SYSTEM_cross")
			require.NotContains(t, crossInput[0].Content, "SYSTEM_first")
			require.Equal(t, sessionRecordMessages(rootCapture.Delta), sessionConversation(crossInput)[:len(rootCapture.Delta)])
		})
	}
}

func TestAgentV3SessionDefaultsLatestAndReplySelection(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "defaults", ContextMode: "reply_chain"}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("one", nil)}, {schema.AssistantMessage("two", nil)}, {schema.AssistantMessage("three", nil)}, {schema.AssistantMessage("four", nil)}, {schema.AssistantMessage("five", nil)}, {schema.AssistantMessage("six", nil)}}}
	f.compile(t, cfg, mdl)
	one := f.chat(t, cfg, sessionMessage(10, 7, 0, "one input"), nil)
	twoMessage := sessionMessage(20, 7, 60, "two input")
	two := f.chat(t, cfg, twoMessage, nil)
	require.NotEqual(t, f.node(t, one).Ref.DAGID, f.node(t, two).Ref.DAGID)
	cfg.Session.LoadContext = true
	noSave := false
	cfg.Session.SaveContext = &noSave
	command := sessionMessage(30, 7, 120, "three input")
	command.ReplyTo = &tb.Message{ID: one}
	three := f.chat(t, cfg, command, &config.AgentTrigger{Command: "ask"})
	require.Contains(t, replySessionSchemaText(mdl.capturedInputs()[2]), "two input")
	require.Contains(t, replySessionSchemaText(mdl.capturedInputs()[2]), "current_user_quote", "latest selected another root: direct quote must remain")
	_, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: three})
	require.ErrorIs(t, err, session.ErrMiss)
	cfg.Session.LoadContext = false
	fourMessage := sessionMessage(40, 7, 180, "four input")
	fourMessage.ReplyTo = &tb.Message{ID: one}
	four := f.chat(t, cfg, fourMessage, &config.AgentTrigger{Reply: true})
	oneNode := f.node(t, one)
	require.Equal(t, &oneNode.Ref, f.node(t, four).Parent)
	require.False(t, cfg.Session.SaveEnabled())
	require.False(t, cfg.Session.LoadEnabled())
	missing := sessionMessage(50, 7, 240, "five input")
	missing.ReplyTo = &tb.Message{ID: 999999}
	five := f.chat(t, cfg, missing, &config.AgentTrigger{Reply: true})
	require.Nil(t, f.node(t, five).Parent)
	require.NotContains(t, replySessionSchemaText(mdl.capturedInputs()[4]), "four input")
	disabled := sessionMessage(60, 7, 300, "disabled input")
	disabled.ReplyTo = &tb.Message{ID: one}
	six := f.chat(t, cfg, disabled, &config.AgentTrigger{Command: "ask"})
	_, err = f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: six})
	require.ErrorIs(t, err, session.ErrMiss, "a command with ReplyTo must not force saving")
}

func TestAgentV3SessionFallbackRootIncludesLegacyBaselineAndFreshMemory(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "fallback", ContextMode: "chat", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("root final", nil)}, {schema.AssistantMessage("continued", nil)}}}
	f.compile(t, cfg, mdl, lookupTool{})
	scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
	config.BotConfig.AgentV3.Memory.Enable = true
	require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "OLD_MEMORY"))
	require.NoError(t, orm.AgentV3SetSummary(t.Context(), scope, orm.AgentV3Summary{Content: "LEGACY_SUMMARY"}, time.Hour))
	require.NoError(t, orm.AgentV3AppendTurnPair(t.Context(), scope, orm.AgentV3Turn{Role: "user", Content: "LEGACY_USER"}, orm.AgentV3Turn{Role: "assistant", Content: "LEGACY_ASSISTANT"}, 12, time.Hour))
	first := f.chat(t, cfg, sessionMessage(10, 7, 0, "<group_memory_snapshot>REAL_USER"), nil)
	root := f.node(t, first)
	capture := f.archive(t, root)
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(capture.Bootstrap)), "LEGACY_SUMMARY")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(capture.Bootstrap)), "LEGACY_USER")
	require.NotContains(t, replySessionSchemaText(sessionRecordMessages(capture.Bootstrap)), "OLD_MEMORY")
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(capture.Delta)), "REAL_USER")
	items, err := orm.AgentV3ListMemory(t.Context(), scope)
	require.NoError(t, err)
	require.NoError(t, orm.AgentV3ForgetMemory(t.Context(), scope, items[0].ID))
	require.NoError(t, rebuildAgentV3MemorySnapshot(t.Context(), scope, time.Hour))
	require.NoError(t, addAgentV3Memory(t.Context(), scope, 7, "NEW_MEMORY"))
	// A session hit must bypass every old summary/raw-turn read, even if those keys are broken.
	keys, err := f.client.Keys(t.Context(), "*:hot:raw_turns").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NoError(t, f.client.Del(t.Context(), keys[0]).Err())
	require.NoError(t, f.client.Set(t.Context(), keys[0], "wrong type", 0).Err())
	keys, err = f.client.Keys(t.Context(), "*:summary:current").Result()
	require.NoError(t, err)
	require.Len(t, keys, 1)
	require.NoError(t, f.client.Del(t.Context(), keys[0]).Err())
	require.NoError(t, f.client.RPush(t.Context(), keys[0], "wrong type").Err())
	second := f.chat(t, cfg, sessionMessage(20, 8, 60, "NEW_INPUT"), nil)
	require.Equal(t, &root.Ref, f.node(t, second).Parent, "legacy save failure must not prevent session commit")
	text := replySessionSchemaText(mdl.capturedInputs()[1])
	require.Contains(t, text, "NEW_MEMORY")
	require.NotContains(t, text, "OLD_MEMORY")
	require.Equal(t, 1, strings.Count(text, "LEGACY_SUMMARY"))
	require.Equal(t, 1, strings.Count(text, "LEGACY_USER"))
}

func TestAgentV3SessionBadArchiveFallsBackWithoutParent(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "badfile", ContextMode: "chat", Session: config.AgentSessionConfig{LoadContext: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("FIRST_FINAL", nil)}, {schema.AssistantMessage("NEW_ROOT_FINAL", nil)}}}
	f.compile(t, cfg, mdl)
	first := f.chat(t, cfg, sessionMessage(10, 7, 0, "FIRST_INPUT"), nil)
	root := f.node(t, first)
	var archivePath string
	require.NoError(t, filepath.WalkDir(f.directory, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == root.Ref.NodeID+".jsonl" {
			archivePath = path
		}
		return nil
	}))
	require.NotEmpty(t, archivePath)
	require.NoError(t, os.WriteFile(archivePath, []byte("bad JSONL\n"), 0600))
	scope := orm.AgentV3Scope{Bot: f.scope().Bot, Platform: agentV3Platform, ChatID: -100}
	require.NoError(t, orm.AgentV3SetSummary(t.Context(), scope, orm.AgentV3Summary{Content: "FALLBACK_SUMMARY"}, time.Hour))
	second := f.chat(t, cfg, sessionMessage(20, 7, 60, "SECOND_INPUT"), nil)
	newRoot := f.node(t, second)
	require.Nil(t, newRoot.Parent)
	require.NotEqual(t, root.Ref.DAGID, newRoot.Ref.DAGID)
	require.Contains(t, replySessionSchemaText(sessionRecordMessages(f.archive(t, newRoot).Bootstrap)), "FALLBACK_SUMMARY")
	require.Contains(t, replySessionSchemaText(mdl.capturedInputs()[1]), "FIRST_INPUT")
}

func TestAgentV3SessionReplyChainCaptionMultimodalCurrentOnce(t *testing.T) {
	f := newAgentSessionFixture(t)
	oldEncoder := encodeTelegramPhotoDataURL
	encodeTelegramPhotoDataURL = func(*TurnContext, *tb.Photo) (string, error) { return "data:image/jpeg;base64,aA==", nil }
	t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })
	cfg := &config.AgentConfig{Name: "photo", ContextMode: "reply_chain", Features: config.FeatureSetting{Image: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("photo one", nil)}, {schema.AssistantMessage("photo two", nil)}}}
	compiled := f.compile(t, cfg, mdl)
	cfg.Model.Features.Image = true
	compiled.PromptTemplate = template.Must(template.New("prompt").Parse("CURRENT_TEMPLATE"))
	photo := sessionMessage(10, 7, 0, "")
	photo.Caption = "FIRST_CAPTION"
	photo.Photo = &tb.Photo{File: tb.File{FileID: "first-photo"}}
	first := f.chat(t, cfg, photo, nil)
	second := sessionMessage(20, 7, 60, "")
	second.Caption = "SECOND_CAPTION"
	second.Photo = &tb.Photo{File: tb.File{FileID: "second-photo"}}
	second.ReplyTo = &tb.Message{ID: first, Caption: "DO_NOT_REBUILD_ANCESTOR", Photo: &tb.Photo{File: tb.File{FileID: "forbidden-parent-photo"}}}
	secondID := f.chat(t, cfg, second, &config.AgentTrigger{Reply: true})
	rootCapture := f.archive(t, f.node(t, first))
	childCapture := f.archive(t, f.node(t, secondID))
	require.Len(t, rootCapture.Delta[0].Message.UserInputMultiContent, 2)
	require.Len(t, childCapture.Delta[0].Message.UserInputMultiContent, 2)
	require.Equal(t, "data:image/jpeg;base64,aA==", *childCapture.Delta[0].Message.UserInputMultiContent[1].Image.URL)
	input := mdl.capturedInputs()[1]
	require.Equal(t, rootCapture.Delta[0].Message, input[1])
	text := replySessionSchemaText(input)
	require.Equal(t, 1, strings.Count(text, "SECOND_CAPTION"))
	require.NotContains(t, text, "DO_NOT_REBUILD_ANCESTOR")
}

type agentSessionModel struct {
	*scriptedToolModel
	before func(context.Context, []*schema.Message) error
}

func (m *agentSessionModel) Stream(ctx context.Context, input []*schema.Message, opts ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	if m.before != nil {
		if err := m.before(ctx, input); err != nil {
			return nil, err
		}
	}
	return m.scriptedToolModel.Stream(ctx, input, opts...)
}

func (m *agentSessionModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestAgentV3SessionFailedTurnsAreNotPublished(t *testing.T) {
	for _, streaming := range []bool{false, true} {
		for _, failure := range []string{"empty", "model error", "send failure", "no receipt", "unfinished tool"} {
			t.Run(fmt.Sprintf("stream=%t/%s", streaming, failure), func(t *testing.T) {
				f := newAgentSessionFixture(t)
				cfg := &config.AgentConfig{Name: "failure", ContextMode: "reply_chain", Format: config.AgentOutputConfig{StreamOutput: streaming}}
				mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("answer", nil)}}}}
				switch failure {
				case "empty":
					mdl.turns = nil
				case "model error":
					mdl.before = func(context.Context, []*schema.Message) error { return errAgentSessionFixtureModel }
				case "send failure":
					f.failDelivery = true
				case "no receipt":
					f.nextID = -1
				case "unfinished tool":
					mdl.turns = [][]*schema.Message{{sessionToolMessage("one")}, {sessionToolMessage("two")}, {sessionToolMessage("three")}, {sessionToolMessage("four")}}
				}
				f.compile(t, cfg, mdl, lookupTool{})
				_ = Chat(f.bot.NewContext(tb.Update{Message: sessionMessage(10, 7, 0, "input")}), cfg, nil)
				scopes, err := f.repo.Scopes(t.Context())
				require.NoError(t, err)
				require.Empty(t, scopes)
			})
		}
	}
}

func TestAgentV3SessionCommitIsIndependentOfModelDeadline(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "deadline", ContextMode: "reply_chain"}
	compiled := f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("answer", nil)}}})
	tc := &TurnContext{Bot: f.bot, BotUser: f.bot.Me, Message: sessionMessage(10, 7, 0, "input"), ChatID: -100, Config: cfg}
	ctx, cancel := context.WithCancel(WithTurnContext(t.Context(), tc))
	defer cancel()
	setupAgentV3SessionTurn(tc)
	defer closeAgentV3SessionTurn(tc)
	messages, err := prepareAgentV3Turn(ctx, compiled, tc, nil)
	require.NoError(t, err)
	_, err = compiled.Agent.Generate(WithSessionCapture(ctx, tc.Session.capture), messages)
	require.NoError(t, err)
	cancel()
	commitAgentV3Session(tc, &tb.Message{ID: 42})
	require.Nil(t, f.node(t, 42).Parent)
}

func TestAgentV3SessionArchiveRejectsChangedBaseline(t *testing.T) {
	input := []*schema.Message{schema.SystemMessage("system"), schema.UserMessage("REAL_INPUT")}
	state := &agentV3SessionTurn{input: input, kinds: []agentV3SessionInputKind{agentV3SessionFrame, agentV3SessionCurrent}}
	snapshot := SessionCaptureResult{Input: cloneScriptedToolMessages(input), ModelInput: cloneScriptedToolMessages(input), Complete: true, Messages: []*schema.Message{schema.AssistantMessage("final", nil)}}
	_, err := agentV3SessionArchive(state, snapshot)
	require.NoError(t, err)
	snapshot.ModelInput[1].Content = "CHANGED"
	_, err = agentV3SessionArchive(state, snapshot)
	require.ErrorContains(t, err, "changed non-system")
	snapshot.ModelInput = snapshot.ModelInput[:1]
	_, err = agentV3SessionArchive(state, snapshot)
	require.ErrorContains(t, err, "omitted")
}

func TestAgentV3SessionStandalonePrepareNilHistoryDoesNotLoadLegacyContext(t *testing.T) {
	for _, withState := range []bool{false, true} {
		t.Run(fmt.Sprintf("session_state=%t", withState), func(t *testing.T) {
			setupReplySessionRedis(t)
			config.BotConfig.AgentV3 = setupReplySessionTurnConfig()
			cfg := &config.AgentConfig{Name: "standalone", ContextMode: "chat", Model: &config.Model{Model: "test-model"}}
			tc := &TurnContext{Message: &tb.Message{ID: 42, Text: "standalone input"}, ChatID: -100, Config: cfg, BotUser: &tb.User{Username: "bot"}}
			if withState {
				tc.Session = &agentV3SessionTurn{}
			}
			compiled := &CompiledAgent{Name: cfg.Name, Config: cfg, SystemTemplate: template.Must(template.New("system").Parse("standalone soul"))}
			messages, err := prepareAgentV3Turn(t.Context(), compiled, tc, nil)
			require.NoError(t, err)
			require.NotEmpty(t, messages)
			require.Contains(t, replySessionSchemaText(messages), "standalone input")
		})
	}
}

func TestAgentV3SessionLostParentLeaseForksFullRoot(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "fence", ContextMode: "reply_chain"}
	mdl := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("old-tool")}, {schema.AssistantMessage("old final", nil)}, {schema.AssistantMessage("new final", nil)}}}}
	f.compile(t, cfg, mdl, lookupTool{})
	first := f.chat(t, cfg, sessionMessage(100, 7, 0, "old input"), nil)
	root := f.node(t, first)
	rootCapture := f.archive(t, root)
	mdl.before = func(ctx context.Context, _ []*schema.Message) error {
		tc := GetTurnContext(ctx)
		if tc.Session.parent == nil {
			return errAgentSessionFixtureParent
		}
		return tc.Session.parent.Close()
	}
	current := sessionMessage(200, 7, 60, "new input")
	current.ReplyTo = &tb.Message{ID: first}
	second := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	fork := f.node(t, second)
	require.Nil(t, fork.Parent)
	require.NotEqual(t, root.Ref.DAGID, fork.Ref.DAGID)
	require.Equal(t, rootCapture.Delta, f.archive(t, fork).Bootstrap)
}

type agentSessionDelegateTool struct{ child *CustomAgent }

func (d *agentSessionDelegateTool) Info(context.Context) (*schema.ToolInfo, error) {
	return &schema.ToolInfo{Name: "delegate", Desc: "private delegate fixture"}, nil
}

func (d *agentSessionDelegateTool) InvokableRun(ctx context.Context, _ string, _ ...tool.Option) (string, error) {
	answer, err := d.child.Generate(ctx, []*schema.Message{schema.UserMessage("PRIVATE_CHILD_REQUEST")})
	if err != nil {
		return "", err
	}
	return answer.Content, nil
}

func TestAgentV3SessionDelegatePrivateHistoryIsNotArchived(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "parent", ContextMode: "reply_chain"}
	childModel := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("PRIVATE_CHILD_TOOL")}, {schema.AssistantMessage("child result", nil)}}}}
	child, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "child", Model: childModel, Tools: []tool.BaseTool{lookupTool{}}, MaxSteps: 4})
	require.NoError(t, err)
	parentModel := &scriptedToolModel{turns: [][]*schema.Message{{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "TOP_CHILD_CALL", Function: schema.FunctionCall{Name: "delegate", Arguments: `{"request":"private request"}`}}}}}, {schema.AssistantMessage("parent final", nil)}}}
	f.compile(t, cfg, parentModel, &agentSessionDelegateTool{child: child})
	id := f.chat(t, cfg, sessionMessage(100, 7, 0, "parent input"), nil)
	capture := f.archive(t, f.node(t, id))
	require.Len(t, childModel.capturedInputs(), 2)
	require.Len(t, capture.Delta, 4)
	require.Equal(t, "TOP_CHILD_CALL", capture.Delta[1].Message.ToolCalls[0].ID)
	require.Equal(t, "child result", capture.Delta[2].Message.Content)
	encoded, err := json.Marshal(capture)
	require.NoError(t, err)
	require.NotContains(t, string(encoded), "PRIVATE_CHILD_TOOL")
	require.NotContains(t, string(encoded), "PRIVATE_CHILD_REQUEST")
	require.Equal(t, 1, strings.Count(string(encoded), "private request"), "only the top-level tool arguments are archived")
}

func TestAgentV3SessionChatInputDoesNotRebuildReplyImagesOrTemplateHistory(t *testing.T) {
	f := newAgentSessionFixture(t)
	oldEncoder := encodeTelegramPhotoDataURL
	var encoded []string
	encodeTelegramPhotoDataURL = func(_ *TurnContext, photo *tb.Photo) (string, error) {
		encoded = append(encoded, photo.FileID)
		return "data:image/jpeg;base64,aA==", nil
	}
	t.Cleanup(func() { encodeTelegramPhotoDataURL = oldEncoder })
	cfg := &config.AgentConfig{Name: "chat-photo", ContextMode: "chat", Features: config.FeatureSetting{Image: true}}
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("first final", nil)}, {schema.AssistantMessage("second final", nil)}}}
	compiled := f.compile(t, cfg, mdl)
	cfg.Model.Features.Image = true
	compiled.PromptTemplate = template.Must(template.New("prompt").Parse("{{.Input}} {{.ContextText}} {{.ReplyToXml}}"))
	first := sessionMessage(100, 7, 0, "")
	first.Caption, first.Photo = "FIRST_CAPTION", &tb.Photo{File: tb.File{FileID: "first-image"}}
	firstID := f.chat(t, cfg, first, nil)
	rootCapture := f.archive(t, f.node(t, firstID))
	encoded = nil
	second := sessionMessage(200, 8, 60, "")
	second.Caption, second.Photo = "SECOND_CAPTION", &tb.Photo{File: tb.File{FileID: "second-image"}}
	second.ReplyTo = &tb.Message{ID: firstID, Text: "FORBIDDEN_ANCESTOR", Photo: &tb.Photo{File: tb.File{FileID: "forbidden-image"}}}
	secondID := f.chat(t, cfg, second, &config.AgentTrigger{Reply: true})
	require.Equal(t, []string{"second-image"}, encoded)
	childCapture := f.archive(t, f.node(t, secondID))
	require.Len(t, childCapture.Delta, 2)
	require.Len(t, childCapture.Delta[0].Message.UserInputMultiContent, 2)
	require.Equal(t, rootCapture.Delta[0].Message, mdl.capturedInputs()[1][1])
	text := replySessionSchemaText(mdl.capturedInputs()[1])
	require.Equal(t, 1, strings.Count(childCapture.Delta[0].Message.UserInputMultiContent[0].Text, "<dynamic_suffix>\n"), "image manifests retain caption metadata, but the input is appended once")
	require.Contains(t, childCapture.Delta[0].Message.UserInputMultiContent[0].Text, "SECOND_CAPTION")
	require.NotContains(t, text, "FORBIDDEN_ANCESTOR")
}

func TestAgentV3SessionReplayDoesNotRestoreRichOrRuntimePermissions(t *testing.T) {
	f := newAgentSessionFixture(t)
	cfg := &config.AgentConfig{Name: "rich-parent", ContextMode: "reply_chain", Agent: &config.AgentOptions{Enable: true, Rich: true}}
	snapshot := buildAgentV3BuiltinSkillSnapshot(cfg, config.BotConfig.AgentV3)
	catalog, _, err := mergeAgentV3SkillSnapshots(snapshot)
	require.NoError(t, err)
	raw := "original preface\n" + mustTelegramRichEnvelope("**RICH_FINAL**") + "\noriginal suffix"
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "rich-load", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"name":"rich-message"}`}}}}}, {schema.AssistantMessage(raw, nil)}}}
	compiled := f.compile(t, cfg, mdl, buildAgentV3Tools(cfg, config.BotConfig.AgentV3, catalog, nil)...)
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	first := f.chat(t, cfg, sessionMessage(100, 7, 0, "first input"), nil)
	rootCapture := f.archive(t, f.node(t, first))
	require.Equal(t, raw, rootCapture.Delta[len(rootCapture.Delta)-1].Message.Content)
	config.BotConfig.AgentV3.Runtime.Env = map[string]string{"CURRENT_ENV": "current"}
	cross := &config.AgentConfig{Name: "current-agent", ContextMode: "reply_chain", Agent: &config.AgentOptions{Enable: true, Rich: true}}
	currentModel := &agentSessionModel{scriptedToolModel: &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("current final", nil)}}}}
	var richLoaded bool
	var environment map[string]string
	currentModel.before = func(ctx context.Context, _ []*schema.Message) error {
		tc := GetTurnContext(ctx)
		richLoaded = tc.richMessageSkillLoadedForFinal()
		environment, _ = tc.runtimeEnvironment()
		return nil
	}
	f.compile(t, cross, currentModel)
	current := sessionMessage(200, 8, 60, "current input")
	current.ReplyTo = &tb.Message{ID: first}
	f.chat(t, cross, current, &config.AgentTrigger{Reply: true})
	require.False(t, richLoaded)
	require.Equal(t, map[string]string{"CURRENT_ENV": "current"}, environment)
	require.Equal(t, sessionRecordMessages(rootCapture.Delta), sessionConversation(currentModel.capturedInputs()[0])[:len(rootCapture.Delta)])
}

func TestAgentV3SessionRestartReplayAndWholeDAGCollection(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	cfg := &config.AgentConfig{Name: "restart", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{sessionToolMessage("persisted-tool")}, {schema.AssistantMessage("persisted final", nil)}}}, lookupTool{})
	first := f.chat(t, cfg, sessionMessage(100, 7, 0, "persisted input"), nil)
	root := f.node(t, first)
	rootCapture := f.archive(t, root)
	require.NoError(t, f.service.Close())
	var err error
	f.files, err = session.NewFileStore(f.directory)
	require.NoError(t, err)
	f.service, err = session.NewService(f.repo, f.files, session.Options{TTL: time.Hour})
	require.NoError(t, err)
	done := make(chan struct{})
	close(done)
	agentSessionService.Store(&agentV3SessionService{service: f.service, cancel: func() {}, done: done})
	mdl := &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("after restart", nil)}}}
	f.compile(t, cfg, mdl)
	current := sessionMessage(200, 8, 60, "restart input")
	current.ReplyTo = &tb.Message{ID: first}
	second := f.chat(t, cfg, current, &config.AgentTrigger{Reply: true})
	child := f.node(t, second)
	require.Equal(t, &root.Ref, child.Parent)
	require.Equal(t, sessionRecordMessages(rootCapture.Delta), sessionConversation(mdl.capturedInputs()[0])[:len(rootCapture.Delta)])
	f.mini.SetTime(now.Add(59 * time.Minute))
	other := &config.AgentConfig{Name: "survivor", ContextMode: "reply_chain"}
	f.compile(t, other, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("surviving final", nil)}}})
	otherID := f.chat(t, other, sessionMessage(300, 7, 120, "surviving input"), nil)
	otherNode := f.node(t, otherID)
	f.mini.SetTime(now.Add(90 * time.Minute))
	require.NoError(t, f.service.Collect(t.Context()))
	for _, id := range []int{first, second} {
		_, err = f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Mode: session.SelectReply, ReplyMessageID: id})
		require.ErrorIs(t, err, session.ErrMiss)
	}
	loaded, err := f.service.Load(t.Context(), session.Selection{Scope: f.scope(), Agent: "survivor", Mode: session.SelectLatest})
	require.NoError(t, err)
	require.Equal(t, otherNode.Ref, loaded.Parent.Ref())
	require.NoError(t, loaded.Parent.Close())
	var remaining []string
	require.NoError(t, filepath.WalkDir(f.directory, func(_ string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.HasSuffix(entry.Name(), ".jsonl") {
			remaining = append(remaining, entry.Name())
		}
		return nil
	}))
	require.Equal(t, []string{otherNode.Ref.NodeID + ".jsonl"}, remaining)
}
