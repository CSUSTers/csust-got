package agentv3

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"text/template"
	"time"

	"csust-got/config"
	"csust-got/cronjob"
	"csust-got/orm"

	"github.com/alicebob/miniredis/v2"
	"github.com/cloudwego/eino/components/model"
	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
	tb "gopkg.in/telebot.v3"
)

const testCronPrompt = "## Context\nA self-contained scheduled task.\n## Steps\nRead the workspace status.\n## Goal\nReturn the final status."

var (
	errCronSecretProviderUnderTest = errors.New("secret provider URL and token")
	errCronRedisSecretUnderTest    = cronFixtureError("Redis URL secret")
	errCronTelegramUnderTest       = cronFixtureError("Telegram unavailable")
)

type cronTestContextKey struct{}
type cronFixtureError string

func (e cronFixtureError) Error() string {
	return string(e)
}

type cronFixture struct {
	s     *agentCronService
	tc    *TurnContext
	now   time.Time
	redis *miniredis.Miniredis
}

func newCronFixture(t *testing.T) *cronFixture {
	t.Helper()
	redis := setupReplySessionRedis(t)
	oldAgents := snapshotCompiledAgents()
	oldService := cronService.Load()
	t.Cleanup(func() { restoreCompiledAgents(oldAgents); cronService.Store(oldService) })
	clearCompiledAgents()
	cc := &config.AgentConfig{Name: "runner", Agent: &config.AgentOptions{Enable: true}, ContextMode: "reply_chain"}
	source := &config.AgentConfig{Name: "source", Agent: &config.AgentOptions{Enable: true}}
	config.BotConfig.Agents = &config.AgentV3Configs{source, cc}
	config.BotConfig.AgentV3 = &config.AgentV3Config{Enable: true, Runtime: config.AgentV3RuntimeConfig{Enable: true, Mode: "remote_http"}, Cron: config.DefaultAgentV3CronConfig()}
	config.BotConfig.AgentV3.Cron.RunnerAgent = "runner"
	config.BotConfig.WhiteListConfig.Enabled = false
	bot, err := tb.NewBot(tb.Settings{Token: "fixture-token", Offline: true})
	require.NoError(t, err)
	bot.Me = &tb.User{ID: 100, Username: "cronbot"}
	f := &cronFixture{now: time.Now().UTC().Truncate(time.Minute), redis: redis}
	s := &agentCronService{cfg: config.BotConfig.AgentV3.CronConfig(), store: orm.NewAgentCronStore(), bot: bot, coordinator: cronjob.Coordinator{Bot: "cronbot", Platform: "tg"}, policy: orm.AgentCronPolicy, now: func() time.Time { return f.now }}
	s.run, s.report = s.generate, s.sendReport
	f.s = s
	f.tc = &TurnContext{Bot: bot, BotUser: bot.Me, ChatID: -100, Config: source, Message: &tb.Message{ID: 50, ThreadID: 77, Chat: &tb.Chat{ID: -100, Type: tb.ChatSuperGroup}, Sender: &tb.User{ID: 7}}}
	compiledAgents.Store("source", &CompiledAgent{Name: "source", Config: source})
	compiledAgents.Store("runner", &CompiledAgent{Name: "runner", Config: cc, PromptTemplate: template.Must(template.New("prompt").Parse("runner template"))})
	cronService.Store(s)
	return f
}

func (f *cronFixture) invoke(t *testing.T, manage bool, args any) map[string]any {
	t.Helper()
	input, err := json.Marshal(args)
	require.NoError(t, err)
	// Use the actual fixed-tool construction seam, not a service-only facade.
	items := buildAgentV3Tools(f.tc.Config, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil)
	name := "delegate"
	if manage {
		name = "cron_tasks"
	}
	for _, item := range items {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		if info.Name != name {
			continue
		}
		out, err := item.(tool.InvokableTool).InvokableRun(WithTurnContext(t.Context(), f.tc), string(input))
		require.NoError(t, err)
		var result map[string]any
		require.NoError(t, json.Unmarshal([]byte(out), &result))
		return result
	}
	t.Fatalf("missing %s", name)
	return nil
}

func (f *cronFixture) create(t *testing.T) cronjob.Task {
	return f.createSchedule(t, "* * * * *")
}

func (f *cronFixture) createSchedule(t *testing.T, expression string) cronjob.Task {
	t.Helper()
	out := f.invoke(t, false, map[string]any{"cron": expression, "prompt": testCronPrompt})
	require.NotContains(t, out, "code")
	task, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: cronjob.Scope{Bot: "cronbot", Platform: "tg", ChatID: -100}, TaskID: out["task_id"].(string)})
	require.NoError(t, err)
	return task
}

func (f *cronFixture) runTask(t *testing.T, task cronjob.Task) cronjob.Task {
	t.Helper()
	f.now = task.NextRunAt
	candidates, err := f.s.store.Due(t.Context(), cronjob.DueRequest{Coordinator: f.s.coordinator, Now: f.now, Limit: 20})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	f.s.executeCandidate(t.Context(), candidates[0])
	got, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.NotNil(t, got.LatestResult)
	return got
}

func (f *cronFixture) deliver(t *testing.T, task cronjob.Task) cronjob.Task {
	t.Helper()
	candidates, err := f.s.store.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: f.s.coordinator, Now: f.now, Limit: 20})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	f.s.deliverCandidate(t.Context(), candidates[0])
	got, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	return got
}

func TestCronToolsValidationOwnershipAndScope(t *testing.T) {
	f := newCronFixture(t)
	for _, args := range []map[string]any{
		{"cron": "@daily", "prompt": testCronPrompt},
		{"cron": "* * * * *", "prompt": "missing sections"},
		{"cron": "* * * * *", "prompt": testCronPrompt, "chat_id": 9},
	} {
		require.Equal(t, "invalid_argument", f.invoke(t, false, args)["code"])
	}
	task := f.create(t)
	require.Equal(t, int64(77), task.ThreadID)
	require.Equal(t, "source", task.SourceAgent)
	require.True(t, task.NextRunAt.After(f.now))
	require.Equal(t, true, f.invoke(t, false, map[string]any{"cron": "* * * * *", "prompt": testCronPrompt})["deduplicated"])
	out := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
	require.Equal(t, testCronPrompt, out["prompt"])
	f.tc.Message.Sender.ID = 8
	require.Equal(t, "forbidden", f.invoke(t, true, map[string]any{"action": "delete", "task_id": task.ID, "expected_version": task.Version})["code"])
	f.tc.Message.Sender.ID = 7
	require.Equal(t, "conflict", f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": 99, "cron": "0 * * * *"})["code"])
	updated := f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": task.Version, "cron": "0 * * * *"})
	require.Equal(t, float64(task.Version+1), updated["version"])
	f.tc.ChatID, f.tc.Message.Chat.ID = -200, -200
	require.Equal(t, "not_found", f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})["code"])
	f.tc.ChatID, f.tc.Message.Chat.ID = -100, -100
	require.Equal(t, true, f.invoke(t, true, map[string]any{"action": "delete", "task_id": task.ID, "expected_version": task.Version + 1})["deleted"])
	defs := agentV3ToolDefinitionsText(false, false, false, true)
	require.Contains(t, defs, "expected_version")
	require.Contains(t, defs, "## Context")
	require.NotContains(t, agentV3ToolDefinitionsText(false, false, false), "delegate")
}

func TestCronDedicatedRunnerRuntimeAndTelegramOutbox(t *testing.T) {
	for _, expression := range []string{"* * * * *", "@at 90s"} {
		t.Run(expression, func(t *testing.T) {
			f := newCronFixture(t)
			var runtimeRequests []runtimeBashRequest
			runtimeServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var req runtimeBashRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					http.Error(w, "bad JSON", 400)
					return
				}
				runtimeRequests = append(runtimeRequests, req)
				_ = json.NewEncoder(w).Encode(runtimeBashResponse{Stdout: "workspace ready"})
			}))
			t.Cleanup(runtimeServer.Close)
			config.BotConfig.AgentV3.Runtime.Endpoint = runtimeServer.URL
			mdl := &scriptedToolModel{turns: [][]*schema.Message{
				{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "bash-call", Type: "function", Function: schema.FunctionCall{Name: "bash", Arguments: `{"command":"pwd"}`}}}}},
				{{Role: schema.Assistant, Content: strings.Repeat("🙂", 2200), ReasoningContent: "PRIVATE_REASONING"}},
			}}
			agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "runner", Model: mdl, Tools: buildAgentV3Tools(nil, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil), MaxSteps: 4})
			require.NoError(t, err)
			value, _ := compiledAgents.Load("runner")
			value.(*CompiledAgent).Agent = agent
			var mu sync.Mutex
			var sends []map[string]any
			telegram := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				var payload map[string]any
				if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
					http.Error(w, "bad JSON", 400)
					return
				}
				mu.Lock()
				defer mu.Unlock()
				sends = append(sends, payload)
				if len(sends) == 2 {
					_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"temporary failure"}`))
					return
				}
				_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":-100}}}`, len(sends)+100)
			}))
			t.Cleanup(telegram.Close)
			f.s.bot.URL = telegram.URL
			task := f.runTask(t, f.createSchedule(t, expression))
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			require.NotContains(t, task.LatestResult.Text, "PRIVATE_REASONING")
			require.Len(t, runtimeRequests, 1)
			require.Equal(t, "cronbot:tg:-100", runtimeRequests[0].Namespace)
			require.Equal(t, task.LatestResult.RunID, runtimeRequests[0].RunID)
			inputs := mdl.capturedInputs()
			require.Len(t, inputs, 2)
			allInput, _ := json.Marshal(inputs[0])
			require.Contains(t, string(allInput), "runner template")
			require.Contains(t, string(allInput), "A self-contained scheduled task")
			turns, err := orm.AgentV3LoadTurns(t.Context(), orm.AgentV3Scope{Bot: "cronbot", Platform: "tg", ChatID: -100}, 20)
			require.NoError(t, err)
			require.Empty(t, turns)
			task = f.deliver(t, task)
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			require.Equal(t, cronjob.DeliveryPending, task.Report.State)
			require.Len(t, task.Report.Receipt.MessageIDs, 1)
			f.now = task.Report.NextAttemptAt
			task = f.deliver(t, task)
			require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
			require.Len(t, task.Report.Receipt.MessageIDs, 2)
			require.Len(t, mdl.capturedInputs(), 2, "delivery must not call model")
			if expression == "@at 90s" {
				require.True(t, task.NextRunAt.IsZero())
				view := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
				require.Contains(t, view, "next_run_at")
				require.Nil(t, view["next_run_at"])
				require.Contains(t, cronReportText(task), "无下一次计划（一次性已结束）")
				require.NotContains(t, cronReportText(task), "0001-")
				f.now = f.now.Add(24 * time.Hour)
				f.s.pollDue(t.Context(), t.Context(), make(chan struct{}, 1))
				f.s.wg.Wait()
				require.Len(t, mdl.capturedInputs(), 2, "completed once must not invoke the model on later polls")
				require.Len(t, runtimeRequests, 1)
			}
			mu.Lock()
			defer mu.Unlock()
			require.Len(t, sends, 3)
			require.Equal(t, sends[1]["text"], sends[2]["text"], "retry only failed chunk")
			require.NotEqual(t, sends[0]["text"], sends[2]["text"])
			for _, payload := range sends {
				require.Equal(t, "-100", payload["chat_id"])
				require.Equal(t, "77", payload["message_thread_id"])
				require.Empty(t, payload["parse_mode"])
			}
		})
	}
}

type cronFailingModel struct{ scriptedToolModel }

func (*cronFailingModel) Stream(context.Context, []*schema.Message, ...model.Option) (*schema.StreamReader[*schema.Message], error) {
	return nil, errCronSecretProviderUnderTest
}
func (m *cronFailingModel) WithTools([]*schema.ToolInfo) (model.ToolCallingChatModel, error) {
	return m, nil
}

func TestCronGenerationFailurePersistsSafeFailureAndExplicitRetry(t *testing.T) {
	f := newCronFixture(t)
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "runner", Model: &cronFailingModel{}, MaxSteps: 4})
	require.NoError(t, err)
	value, _ := compiledAgents.Load("runner")
	value.(*CompiledAgent).Agent = agent
	task := f.runTask(t, f.create(t))
	require.Equal(t, cronjob.OutcomeFailed, task.LatestResult.Outcome)
	require.True(t, task.LatestResult.SideEffectsPossible)
	require.NotContains(t, task.LatestResult.ErrorMessage, "secret")
	require.Contains(t, cronReportText(task), "Retry cron task "+task.ID+" run "+task.LatestResult.RunID)
	f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
		return &cronjob.DeliveryReceipt{MessageIDs: []int{1}, DeliveredAt: f.now}, nil
	}
	task = f.deliver(t, task)
	require.Equal(t, cronjob.OutcomeFailed, task.LatestResult.Outcome)
	out := f.invoke(t, true, map[string]any{"action": "retry", "task_id": task.ID, "expected_version": task.Version, "run_id": task.LatestResult.RunID})
	require.Equal(t, "execution", out["mode"])
	require.Equal(t, "conflict", f.invoke(t, true, map[string]any{"action": "retry", "task_id": task.ID, "expected_version": task.Version, "run_id": task.LatestResult.RunID})["code"])
}

func TestCronPolicyFailClosedAndBackgroundBuiltinGuard(t *testing.T) {
	f := newCronFixture(t)
	items, err := BuildBuiltinTools([]string{"delegate", "cron_tasks"}, nil)
	require.NoError(t, err)
	ctx := WithTurnContext(context.WithValue(t.Context(), cronTestContextKey{}, "nested"), &TurnContext{Background: true})
	for _, item := range items {
		out, err := item.(tool.InvokableTool).InvokableRun(ctx, `{}`)
		require.NoError(t, err)
		require.Contains(t, out, "background_forbidden")
	}
	task := f.create(t)
	f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) {
		return orm.AgentCronPolicyState{}, errCronRedisSecretUnderTest
	}
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		t.Fatal("must not generate")
		return agentCronRunResult{}
	}
	task = f.runTask(t, task)
	require.Equal(t, cronjob.OutcomeSkipped, task.LatestResult.Outcome)
	f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
		t.Fatal("must not report")
		return nil, nil
	}
	task = f.deliver(t, task)
	require.Equal(t, cronjob.DeliverySuppressed, task.Report.State)
	for _, state := range []orm.AgentCronPolicyState{{Shutdown: true}, {Banned: true}, {BlockList: []string{"7"}}, {BlockList: []string{"-100"}}} {
		f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) { return state, nil }
		require.ErrorIs(t, f.s.allowed(t.Context(), task), cronjob.ErrForbidden)
	}
	config.BotConfig.WhiteListConfig.Enabled = true
	f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) {
		return orm.AgentCronPolicyState{}, nil
	}
	require.ErrorIs(t, f.s.allowed(t.Context(), task), cronjob.ErrForbidden)
	f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) {
		return orm.AgentCronPolicyState{WhiteList: []string{"-100"}}, nil
	}
	require.NoError(t, f.s.allowed(t.Context(), task))
	(*config.BotConfig.Agents)[0].Agent.Enable = false
	require.ErrorIs(t, f.s.allowed(t.Context(), task), cronjob.ErrForbidden)
}

func TestCronLivePolicyRevocationSuppressesCompletedOutput(t *testing.T) {
	f := newCronFixture(t)
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: "success before revocation"}
	}
	task := f.runTask(t, f.create(t))
	// Change Redis after generation without touching the startup/config cache.
	f.redis.SAdd(config.BotConfig.RedisConfig.KeyPrefix+"black_list", "7")
	f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
		t.Fatal("revoked permission must prevent report")
		return nil, nil
	}
	task = f.deliver(t, task)
	require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
	require.Equal(t, cronjob.DeliverySuppressed, task.Report.State)
	// A wrong-type Redis policy value is not treated as an empty allow-list.
	f.redis.Del(config.BotConfig.RedisConfig.KeyPrefix + "black_list")
	require.NoError(t, f.redis.Set(config.BotConfig.RedisConfig.KeyPrefix+"black_list", "wrong type"))
	require.ErrorIs(t, f.s.allowed(t.Context(), task), cronjob.ErrUnavailable)
}

func TestCronStartupValidationWithoutEnabledAgents(t *testing.T) {
	newCronFixture(t)
	config.BotConfig.Agents = &config.AgentV3Configs{}
	require.ErrorContains(t, Init(t.Context()), "exactly one enabled agent")
	config.BotConfig.AgentV3.Cron.RunnerAgent = ""
	config.BotConfig.AgentV3.Cron.PollIntervalMinutes = 61
	require.ErrorContains(t, Init(t.Context()), "poll_interval_minutes")
}

func TestCronBackgroundGuardThroughActualSubAgent(t *testing.T) {
	newCronFixture(t)
	var requests atomic.Int32
	var denied atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Stream   bool `json:"stream"`
			Messages []struct {
				Role    string `json:"role"`
				Content any    `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid JSON", 400)
			return
		}
		requests.Add(1)
		for _, message := range body.Messages {
			if message.Role == "tool" && strings.Contains(fmt.Sprint(message.Content), "background_forbidden") {
				denied.Store(true)
			}
		}
		message := map[string]any{"role": "assistant", "content": "Nested task finished."}
		finish := "stop"
		if !denied.Load() {
			message["content"] = ""
			message["tool_calls"] = []any{map[string]any{"index": 0, "id": "nested-delegate", "type": "function", "function": map[string]any{"name": "delegate", "arguments": `{"cron":"* * * * *","prompt":"recursive work"}`}}}
			finish = "tool_calls"
		}
		if body.Stream {
			w.Header().Set("Content-Type", "text/event-stream")
			data, _ := json.Marshal(map[string]any{"id": "fixture", "object": "chat.completion.chunk", "choices": []any{map[string]any{"index": 0, "delta": message, "finish_reason": nil}}})
			_, _ = fmt.Fprintf(w, "data: %s\n\n", data)
			_, _ = fmt.Fprintf(w, "data: {\"id\":\"fixture\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":%q}]}\n\ndata: [DONE]\n\n", finish)
		} else {
			_ = json.NewEncoder(w).Encode(map[string]any{"id": "fixture", "object": "chat.completion", "choices": []any{map[string]any{"index": 0, "message": message, "finish_reason": finish}}})
		}
	}))
	t.Cleanup(server.Close)
	ctx, cancel := context.WithTimeout(WithTurnContext(t.Context(), &TurnContext{Background: true}), 10*time.Second)
	defer cancel()
	sub, err := buildSubAgentTool(ctx, &config.SubAgentConfig{Name: "nested", Model: &config.Model{Name: "fixture", Model: "fixture", BaseUrl: server.URL, ApiKey: "fixture"}, Tools: []string{"delegate"}, MaxSteps: 4}, nil)
	require.NoError(t, err)
	_, err = sub.(tool.InvokableTool).InvokableRun(ctx, `{"request":"Create a recurring task."}`)
	require.NoError(t, err)
	require.True(t, denied.Load(), "subagent must inherit the background turn and receive tool denial")
	require.Equal(t, int32(2), requests.Load())
}

func TestCronReportOnlyRetryNeverRegenerates(t *testing.T) {
	for _, expression := range []string{"* * * * *", "@at 90s", "@every 90s"} {
		t.Run(expression, func(t *testing.T) {
			f := newCronFixture(t)
			runs := 0
			f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
				runs++
				return agentCronRunResult{Text: "saved result"}
			}
			f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
				return nil, errCronTelegramUnderTest
			}
			task := f.runTask(t, f.createSchedule(t, expression))
			next := task.NextRunAt
			originalVersion := task.LatestResult.TaskVersion
			for range 3 {
				task = f.deliver(t, task)
				if !task.Report.NextAttemptAt.IsZero() {
					f.now = task.Report.NextAttemptAt
				}
			}
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			require.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
			args := map[string]any{"action": "retry", "task_id": task.ID, "expected_version": task.Version, "run_id": task.LatestResult.RunID}
			out := f.invoke(t, true, args)
			require.Equal(t, "report_only", out["mode"])
			require.Equal(t, "conflict", f.invoke(t, true, args)["code"])
			f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
				return &cronjob.DeliveryReceipt{MessageIDs: []int{1}}, nil
			}
			task = f.deliver(t, task)
			require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
			require.Equal(t, originalVersion, task.LatestResult.TaskVersion)
			require.Equal(t, originalVersion+1, task.Report.TaskVersion)
			require.Equal(t, 1, runs)
			require.Equal(t, next, task.NextRunAt, "report-only retry must not move the schedule")
		})
	}
}

type cronFlakyFinishStore struct {
	cronjob.Store
	finishes int
}

func (s *cronFlakyFinishStore) Finish(ctx context.Context, req cronjob.FinishRequest) (cronjob.Task, error) {
	s.finishes++
	if s.finishes < 3 {
		return cronjob.Task{}, cronjob.ErrUnavailable
	}
	return s.Store.Finish(ctx, req)
}

func TestCronFinishPersistenceRetryAndLifecycleCancellation(t *testing.T) {
	for _, expression := range []string{"* * * * *", "@at 90s", "@every 90s"} {
		t.Run(expression, func(t *testing.T) {
			f := newCronFixture(t)
			store := &cronFlakyFinishStore{Store: f.s.store}
			f.s.store = store
			runs := 0
			f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
				runs++
				return agentCronRunResult{Text: "result"}
			}
			task := f.runTask(t, f.createSchedule(t, expression))
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			require.Equal(t, 1, runs)
			require.Equal(t, 3, store.finishes)
			// A second task demonstrates real poll-loop cancellation and worker joining.
			err := f.s.store.Delete(t.Context(), cronjob.DeleteRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: 7}, TaskID: task.ID, ExpectedVersion: task.Version, Now: f.now})
			require.NoError(t, err)
			task = f.createSchedule(t, expression)
			f.now = task.NextRunAt
			agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "runner", Model: &scriptedToolModel{}, MaxSteps: 4})
			require.NoError(t, err)
			value, _ := compiledAgents.Load("runner")
			value.(*CompiledAgent).Agent = agent
			started := make(chan struct{})
			f.s.run = func(ctx context.Context, _ cronjob.Lease) agentCronRunResult {
				close(started)
				<-ctx.Done()
				return agentCronRunResult{Err: ctx.Err()}
			}
			require.NoError(t, StartCron(t.Context(), f.s.bot))
			select {
			case <-started:
			case <-time.After(5 * time.Second):
				t.Fatal("scheduler did not start due task")
			}
			done := make(chan struct{})
			go func() { f.s.stop(); close(done) }()
			select {
			case <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("scheduler did not cancel and join worker")
			}
			task, err = f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			require.NoError(t, err)
			require.Equal(t, cronjob.OutcomeFailed, task.LatestResult.Outcome)
			switch expression {
			case "@at 90s":
				require.True(t, task.NextRunAt.IsZero())
			case "@every 90s":
				require.True(t, task.NextRunAt.Equal(task.LatestResult.FinishedAt.Add(90*time.Second)))
			}
		})
	}
}

type cronFinishSignalStore struct {
	cronjob.Store
	finished chan cronjob.ExecutionResult
}

func (s *cronFinishSignalStore) Finish(ctx context.Context, req cronjob.FinishRequest) (cronjob.Task, error) {
	task, err := s.Store.Finish(ctx, req)
	if err == nil {
		s.finished <- req.Result
	}
	return task, err
}

func claimCronTask(t *testing.T, f *cronFixture, task cronjob.Task) cronjob.Lease {
	t.Helper()
	f.now = task.NextRunAt
	candidates, err := f.s.store.Due(t.Context(), cronjob.DueRequest{Coordinator: f.s.coordinator, Now: f.now, Limit: 20})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	lease, err := f.s.claimCandidate(t.Context(), candidates[0])
	require.NoError(t, err)
	return lease
}

func TestCronPersistsModelOutcomeBeforeBoundedTraceFinalization(t *testing.T) {
	f := newCronFixture(t)
	store := &cronFinishSignalStore{Store: f.s.store, finished: make(chan cronjob.ExecutionResult, 2)}
	f.s.store = store
	task := f.create(t)
	lease := claimCronTask(t, f, task)
	finalizeStarted := make(chan struct{})
	finalizeDone := make(chan struct{})
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: "completed before deadline", Finalize: func(ctx context.Context) {
			close(finalizeStarted)
			<-ctx.Done()
			close(finalizeDone)
		}}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 80*time.Millisecond)
	defer cancel()
	done := make(chan struct{})
	go func() { f.s.execute(ctx, lease); close(done) }()
	select {
	case result := <-store.finished:
		require.Equal(t, cronjob.OutcomeSucceeded, result.Outcome)
		require.Equal(t, cronjob.RetryUnavailable, result.RetryStatus)
	case <-time.After(time.Second):
		t.Fatal("successful model outcome was not persisted before trace finalization")
	}
	select {
	case <-finalizeStarted:
	case <-time.After(time.Second):
		t.Fatal("trace finalization did not start")
	}
	select {
	case <-finalizeDone:
	case <-time.After(time.Second):
		t.Fatal("trace finalization ignored its bounded context")
	}
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("execution did not return after bounded trace finalization")
	}
	persisted, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, cronjob.OutcomeSucceeded, persisted.LatestResult.Outcome)
	require.Equal(t, cronjob.RetryUnavailable, persisted.LatestResult.RetryStatus)
}

func TestCronExecutionCompletingAfterDeadlineFails(t *testing.T) {
	f := newCronFixture(t)
	task := f.create(t)
	lease := claimCronTask(t, f, task)
	f.s.run = func(ctx context.Context, _ cronjob.Lease) agentCronRunResult {
		<-ctx.Done()
		return agentCronRunResult{Text: "late success"}
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Millisecond)
	defer cancel()
	f.s.execute(ctx, lease)
	persisted, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, cronjob.OutcomeFailed, persisted.LatestResult.Outcome)
	require.Equal(t, cronjob.RetryAvailable, persisted.LatestResult.RetryStatus)
}

type cronBlockedFirstStore struct {
	cronjob.Store
	candidates []cronjob.Candidate
	blockedID  string
	claims     []string
	finished   chan struct{}
}

func (s *cronBlockedFirstStore) Due(context.Context, cronjob.DueRequest) ([]cronjob.Candidate, error) {
	return s.candidates, nil
}
func (s *cronBlockedFirstStore) Claim(ctx context.Context, req cronjob.ClaimRequest) (cronjob.Lease, error) {
	s.claims = append(s.claims, req.Candidate.TaskID)
	if req.Candidate.TaskID == s.blockedID {
		return cronjob.Lease{}, cronjob.ErrRateLimited
	}
	return s.Store.Claim(ctx, req)
}
func (s *cronBlockedFirstStore) Finish(ctx context.Context, req cronjob.FinishRequest) (cronjob.Task, error) {
	task, err := s.Store.Finish(ctx, req)
	if err == nil {
		close(s.finished)
	}
	return task, err
}

func TestCronPollContinuesAfterRejectedClaimAtLocalCapacityOne(t *testing.T) {
	f := newCronFixture(t)
	task := f.create(t)
	f.now = task.NextRunAt
	due, err := f.s.store.Due(t.Context(), cronjob.DueRequest{Coordinator: f.s.coordinator, Now: f.now, Limit: 20})
	require.NoError(t, err)
	require.Len(t, due, 1)
	blocked := due[0]
	blocked.TaskID = "blocked-by-chat-cooldown"
	store := &cronBlockedFirstStore{Store: f.s.store, candidates: []cronjob.Candidate{blocked, due[0]}, blockedID: blocked.TaskID, finished: make(chan struct{})}
	f.s.store = store
	f.s.cfg.MaxConcurrency = 1
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: "runnable chat completed"}
	}
	slots := make(chan struct{}, 1)
	f.s.pollDue(t.Context(), t.Context(), slots)
	select {
	case <-store.finished:
	case <-time.After(time.Second):
		t.Fatal("later runnable candidate was not executed")
	}
	require.Equal(t, []string{blocked.TaskID, due[0].TaskID}, store.claims)
	f.s.wg.Wait()
}

func TestCronImageToolWorkerJoinsAtRunDeadline(t *testing.T) {
	f := newCronFixture(t)
	requestCanceled := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusOK)
		if flusher, ok := w.(http.Flusher); ok {
			flusher.Flush()
		}
		<-r.Context().Done()
		close(requestCanceled)
	}))
	defer server.Close()
	model := &scriptedToolModel{turns: [][]*schema.Message{{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "image", Function: schema.FunctionCall{Name: "get_image", Arguments: fmt.Sprintf(`{"url":%q}`, server.URL)}}}}}}}
	tools, err := BuildBuiltinTools([]string{"get_image"}, nil)
	require.NoError(t, err)
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "runner", Model: model, Tools: tools, MaxSteps: 4})
	require.NoError(t, err)
	value, _ := compiledAgents.Load("runner")
	value.(*CompiledAgent).Agent = agent
	task := f.create(t)
	lease := claimCronTask(t, f, task)
	lease.RunTimeoutAt = time.Now().Add(100 * time.Millisecond)
	slots := make(chan struct{}, 1)
	slots <- struct{}{}
	f.s.launchReserved(slots, func() { f.s.execute(t.Context(), lease) })
	stopped := make(chan struct{})
	go func() { f.s.stop(); close(stopped) }()
	select {
	case <-requestCanceled:
	case <-time.After(time.Second):
		t.Fatal("stalled image response was not canceled")
	}
	select {
	case <-stopped:
	case <-time.After(2 * time.Second):
		t.Fatal("cron worker did not join after run deadline")
	}
	persisted, err := f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, cronjob.OutcomeFailed, persisted.LatestResult.Outcome)
}
