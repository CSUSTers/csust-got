package agentv3

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"csust-got/config"
	"csust-got/cronjob"
	"csust-got/orm"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func cronRichModel(t *testing.T, richEnabled bool, load bool, content string) *scriptedToolModel {
	t.Helper()
	value, _ := compiledAgents.Load("runner")
	compiled := value.(*CompiledAgent)
	compiled.Config.Agent.Rich = richEnabled
	snapshot := buildAgentV3BuiltinSkillSnapshot(compiled.Config, config.BotConfig.AgentV3)
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{snapshot}
	catalog, _, err := mergeAgentV3SkillSnapshots(snapshot)
	require.NoError(t, err)
	turns := [][]*schema.Message{}
	if load {
		turns = append(turns, []*schema.Message{{Role: schema.Assistant, ToolCalls: []schema.ToolCall{{ID: "rich-skill", Type: "function", Function: schema.FunctionCall{Name: "load_skill", Arguments: `{"name":"rich-message"}`}}}}})
	}
	turns = append(turns, []*schema.Message{{Role: schema.Assistant, Content: content, ReasoningContent: "PRIVATE_REASONING"}})
	model := &scriptedToolModel{turns: turns}
	agent, err := NewCustomAgent(t.Context(), &CustomAgentConfig{Name: "runner", Model: model, Tools: buildAgentV3Tools(compiled.Config, config.BotConfig.AgentV3, catalog, nil), MaxSteps: 4})
	require.NoError(t, err)
	compiled.Agent = agent
	return model
}

type cronRichRequest struct {
	method  string
	payload map[string]any
}

func cronRichTelegram(t *testing.T, f *cronFixture, fail func(int, cronRichRequest) bool) *[]cronRichRequest {
	t.Helper()
	requests := &[]cronRichRequest{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			http.Error(w, "invalid payload", http.StatusBadRequest)
			return
		}
		request := cronRichRequest{method: r.URL.Path, payload: payload}
		*requests = append(*requests, request)
		if fail != nil && fail(len(*requests), request) {
			_, _ = w.Write([]byte(`{"ok":false,"error_code":500,"description":"temporary failure"}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"ok":true,"result":{"message_id":%d,"chat":{"id":-100}}}`, 100+len(*requests))
	}))
	t.Cleanup(server.Close)
	f.s.bot.URL = server.URL
	return requests
}

func TestCronRichSuccessfulLoadPreservesGetTextAndResumesReportOnly(t *testing.T) {
	f := newCronFixture(t)
	markdown := "# 状态 🙂\n\n**完成**"
	raw := "前置说明\n" + mustTelegramRichEnvelope(markdown) + "\n尾随说明"
	model := cronRichModel(t, true, true, raw)
	failRich := true
	requests := cronRichTelegram(t, f, func(_ int, req cronRichRequest) bool {
		return failRich && strings.HasSuffix(req.method, "/sendRichMessage")
	})
	task := f.runTask(t, f.create(t))
	require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
	require.Equal(t, cronRichFormatV1, task.LatestResult.Format)
	require.Equal(t, raw, task.LatestResult.Text)
	require.NotContains(t, task.LatestResult.Text, "PRIVATE_REASONING")
	get := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
	publicResult := get["latest_result"].(map[string]any)
	require.Equal(t, raw, publicResult["text"])
	require.NotContains(t, publicResult, "format")
	require.NotContains(t, publicResult, "rich_message")
	input := model.capturedInputs()
	require.Len(t, input, 2)
	require.Contains(t, fmt.Sprint(input[1]), "loaded_skill")
	runID, resultVersion, next := task.LatestResult.RunID, task.LatestResult.TaskVersion, task.NextRunAt
	task = f.deliver(t, task)
	require.Equal(t, cronjob.DeliveryPending, task.Report.State)
	require.Equal(t, []int{101}, task.Report.Receipt.MessageIDs)
	require.True(t, task.Report.Receipt.DeliveredAt.IsZero())
	require.Len(t, *requests, 2)
	require.True(t, strings.HasSuffix((*requests)[0].method, "/sendMessage"))
	require.True(t, strings.HasSuffix((*requests)[1].method, "/sendRichMessage"))
	require.Equal(t, cronReportMetadata(task), (*requests)[0].payload["text"])
	require.NotContains(t, (*requests)[0].payload["text"], markdown)
	require.NotContains(t, (*requests)[1].payload, "text")
	require.Equal(t, markdown, (*requests)[1].payload["rich_message"].(map[string]any)["markdown"])
	require.NotContains(t, fmt.Sprint((*requests)[1].payload), "前置说明")
	require.NotContains(t, fmt.Sprint((*requests)[1].payload), "尾随说明")
	for _, req := range *requests {
		require.Equal(t, "-100", fmt.Sprint(req.payload["chat_id"]))
	}
	require.Equal(t, "77", (*requests)[0].payload["message_thread_id"])
	require.Equal(t, float64(77), (*requests)[1].payload["message_thread_id"])
	require.Equal(t, float64(50), (*requests)[1].payload["reply_parameters"].(map[string]any)["message_id"])
	require.Equal(t, true, (*requests)[1].payload["reply_parameters"].(map[string]any)["allow_sending_without_reply"])
	// Exhaust automatic attempts; explicit report_only retry must retain the cursor.
	for range 2 {
		f.now = task.Report.NextAttemptAt
		task = f.deliver(t, task)
	}
	require.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
	require.Equal(t, []int{101}, task.Report.Receipt.MessageIDs)
	require.Len(t, *requests, 4)
	args := map[string]any{"action": "retry", "task_id": task.ID, "expected_version": task.Version, "run_id": runID}
	require.Equal(t, "report_only", f.invoke(t, true, args)["mode"])
	(*config.BotConfig.Agents)[1].Agent.Rich = false
	(*config.BotConfig.Agents)[1].Format.Reason = "quote"
	failRich = false
	task = f.deliver(t, task)
	require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
	require.Equal(t, []int{101, 105}, task.Report.Receipt.MessageIDs)
	require.False(t, task.Report.Receipt.DeliveredAt.IsZero())
	require.Equal(t, runID, task.LatestResult.RunID)
	require.Equal(t, raw, task.LatestResult.Text)
	require.Equal(t, resultVersion, task.LatestResult.TaskVersion)
	require.Equal(t, next, task.NextRunAt)
	require.Equal(t, resultVersion+1, task.Report.TaskVersion)
	require.Len(t, model.capturedInputs(), 2, "report-only retry must not invoke the model")
	for _, req := range (*requests)[1:] {
		require.True(t, strings.HasSuffix(req.method, "/sendRichMessage"))
		require.Equal(t, markdown, req.payload["rich_message"].(map[string]any)["markdown"])
	}
}

func TestCronRichClassificationRequiresThisTurnSuccessfulLoad(t *testing.T) {
	for _, tc := range []struct {
		name, text string
		rich, load bool
		wantRich   bool
	}{
		{"disabled", mustTelegramRichEnvelope("**a**"), false, true, false},
		{"unloaded", mustTelegramRichEnvelope("**a**"), true, false, false},
		{"loaded plain", "ordinary", true, true, false},
		{"empty payload", mustTelegramRichEnvelope("  "), true, true, false},
		{"loaded rich", mustTelegramRichEnvelope("**a**"), true, true, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newCronFixture(t)
			cronRichModel(t, tc.rich, tc.load, tc.text)
			task := f.runTask(t, f.create(t))
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			if tc.wantRich {
				require.Equal(t, cronRichFormatV1, task.LatestResult.Format)
			} else {
				require.Equal(t, "plain", task.LatestResult.Format)
			}
			require.Equal(t, tc.text, task.LatestResult.Text)
			get := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
			publicResult := get["latest_result"].(map[string]any)
			require.Equal(t, tc.text, publicResult["text"])
			require.NotContains(t, publicResult, "format")
		})
	}
}

func TestCronRichUnavailableSkillAndPriorTurnDoNotAuthorize(t *testing.T) {
	f := newCronFixture(t)
	content := mustTelegramRichEnvelope("**one**")
	model := cronRichModel(t, true, true, content)
	value, _ := compiledAgents.Load("runner")
	compiled := value.(*CompiledAgent)
	compiled.AgentV3SkillSources = []agentV3SkillSnapshot{emptyAgentV3SkillSnapshot(agentV3SkillSourceBuiltin)}
	task := f.runTask(t, f.create(t))
	require.Equal(t, "plain", task.LatestResult.Format, "failed load_skill must not authorize rich")
	require.Equal(t, content, task.LatestResult.Text)
	require.Len(t, model.capturedInputs(), 2)
	// A separate successful load is also scoped to its own run, not the next one.
	f2 := newCronFixture(t)
	model = cronRichModel(t, true, true, content)
	model.turns = append(model.turns, []*schema.Message{{Role: schema.Assistant, Content: mustTelegramRichEnvelope("**second**")}})
	f2.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
		return &cronjob.DeliveryReceipt{MessageIDs: []int{1, 2}, DeliveredAt: f2.now}, nil
	}
	first := f2.runTask(t, f2.create(t))
	require.Equal(t, cronRichFormatV1, first.LatestResult.Format)
	first = f2.deliver(t, first)
	second := f2.runTask(t, first)
	require.Equal(t, "plain", second.LatestResult.Format)
	require.Equal(t, mustTelegramRichEnvelope("**second**"), second.LatestResult.Text)
	require.Len(t, model.capturedInputs(), 3)
}

func TestCronRichOldAndUnknownFormatsKeepPlainLayout(t *testing.T) {
	for _, format := range []string{"", "plain", "future_format"} {
		t.Run(format, func(t *testing.T) {
			f := newCronFixture(t)
			text := mustTelegramRichEnvelope("**legacy**")
			f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
				return agentCronRunResult{Text: text, Format: format}
			}
			requests := cronRichTelegram(t, f, nil)
			task := f.runTask(t, f.create(t))
			require.Equal(t, "plain", task.LatestResult.Format)
			task.LatestResult.Format = format
			parts, err := cronReportParts(task)
			require.NoError(t, err)
			require.Len(t, parts, 1)
			require.False(t, parts[0].rich)
			require.Equal(t, cronReportText(task), parts[0].text)
			task = f.deliver(t, task)
			require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
			require.Len(t, *requests, 1)
			require.True(t, strings.HasSuffix((*requests)[0].method, "/sendMessage"))
			require.Equal(t, cronReportText(task), (*requests)[0].payload["text"])
		})
	}
}

func TestCronRichCorruptSavedV1FailsBeforeAnySendAndKeepsReceipt(t *testing.T) {
	for _, tc := range []struct {
		name, text string
	}{
		{"no envelope", "plain text"},
		{"unclosed", telegramRichEnvelopeStart + "**body**"},
		{"first empty", mustTelegramRichEnvelope("  ") + mustTelegramRichEnvelope("valid")},
	} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%t", tc.name, partial), func(t *testing.T) {
				f := newCronFixture(t)
				f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
					return agentCronRunResult{Text: mustTelegramRichEnvelope("valid"), Format: cronRichFormatV1}
				}
				task := f.runTask(t, f.create(t))
				task.LatestResult.Text = tc.text
				if partial {
					task.Report.Receipt = &cronjob.DeliveryReceipt{MessageIDs: []int{101}}
				}
				requests := cronRichTelegram(t, f, nil)
				receipt, err := f.s.sendReport(t.Context(), task)
				require.ErrorIs(t, err, errCronInvalidRichResult)
				require.Empty(t, *requests)
				require.True(t, receipt.DeliveredAt.IsZero())
				if partial {
					require.Equal(t, []int{101}, receipt.MessageIDs)
				} else {
					require.Empty(t, receipt.MessageIDs)
				}
			})
		}
	}
}

func TestCronRichBudgetAndFailureCannotUpgrade(t *testing.T) {
	f := newCronFixture(t)
	long := strings.Repeat("🙂", 12001)
	raw := mustTelegramRichEnvelope(long)
	cronRichModel(t, true, true, raw)
	task := f.runTask(t, f.create(t))
	require.Equal(t, "plain", task.LatestResult.Format)
	require.True(t, utf8.ValidString(task.LatestResult.Text))
	require.Equal(t, boundedCronText(raw), task.LatestResult.Text)
	get := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
	require.Equal(t, boundedCronText(raw), get["latest_result"].(map[string]any)["text"])
	// A failed execution cannot turn even an already rich-looking response into rich delivery.
	f2 := newCronFixture(t)
	f2.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: long, Format: cronRichFormatV1, Err: errCronSecretProviderUnderTest}
	}
	failed := f2.runTask(t, f2.create(t))
	require.Equal(t, cronjob.OutcomeFailed, failed.LatestResult.Outcome)
	require.Equal(t, "plain", failed.LatestResult.Format)
	require.NotContains(t, cronReportText(failed), "secret")
}

func TestCronRichSavedEnvelopeBoundariesAndResolverSelection(t *testing.T) {
	start, end := telegramRichEnvelopeStart, telegramRichEnvelopeEnd
	cases := []struct {
		name     string
		raw      string
		markdown string
		rich     bool
		inline   bool
	}{
		{"around body", "前\n" + mustTelegramRichEnvelope("  **正文**  ") + "\n尾", "**正文**", true, false},
		{"multiple first", mustTelegramRichEnvelope("first") + mustTelegramRichEnvelope("second"), "first", true, false},
		{"first empty does not skip", mustTelegramRichEnvelope("  ") + mustTelegramRichEnvelope("second"), "", false, false},
		{"unclosed", start + "body", "", false, false},
		{"start truncated", strings.Repeat("x", 11999) + mustTelegramRichEnvelope("body"), "", false, false},
		{"body truncated", start + strings.Repeat("🙂", 12000) + end, "", false, false},
		{"end loses last rune", start + strings.Repeat("🙂", 12000-len(start)-len(end)+1) + end, "", false, false},
		{"end exactly at boundary", start + strings.Repeat("🙂", 12000-len(start)-len(end)) + end + "tail", strings.Repeat("🙂", 12000-len(start)-len(end)), true, false},
		{"only trailing text truncated", start + "**yes**" + end + strings.Repeat("x", 12000), "**yes**", true, false},
		{"native reasoning separate", mustTelegramRichEnvelope("answer"), "answer", true, false},
		{"inline reasoning", "<think>analysis</think>" + mustTelegramRichEnvelope("body"), "body", true, true},
		{"reasoning has different first envelope", "<think>" + mustTelegramRichEnvelope("reason") + "</think>" + mustTelegramRichEnvelope("body"), "", false, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newCronFixture(t)
			if tc.inline {
				disabled := false
				(*config.BotConfig.Agents)[1].Format.UseNativeReasoning = &disabled
			}
			model := cronRichModel(t, true, true, tc.raw)
			if tc.name == "native reasoning separate" {
				model.turns[len(model.turns)-1][0].ReasoningContent = mustTelegramRichEnvelope("reason")
			}
			requests := cronRichTelegram(t, f, nil)
			task := f.runTask(t, f.create(t))
			wantText := boundedCronText(tc.raw)
			require.Equal(t, wantText, task.LatestResult.Text)
			require.True(t, utf8.ValidString(task.LatestResult.Text))
			get := f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
			publicResult := get["latest_result"].(map[string]any)
			require.Equal(t, wantText, publicResult["text"])
			require.NotContains(t, publicResult, "format")
			if tc.rich {
				require.Equal(t, cronRichFormatV1, task.LatestResult.Format)
			} else {
				require.Equal(t, "plain", task.LatestResult.Format)
			}
			task = f.deliver(t, task)
			require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
			if tc.rich {
				require.Len(t, *requests, 2)
				require.True(t, strings.HasSuffix((*requests)[1].method, "/sendRichMessage"))
				require.Equal(t, tc.markdown, (*requests)[1].payload["rich_message"].(map[string]any)["markdown"])
			} else {
				require.Len(t, *requests, len(cronReportChunks(cronReportText(task))))
				for _, req := range *requests {
					require.True(t, strings.HasSuffix(req.method, "/sendMessage"))
				}
			}
		})
	}
}

func TestCronRichExecuteDowngradesDamagedMarkedResult(t *testing.T) {
	f := newCronFixture(t)
	raw := telegramRichEnvelopeStart + strings.Repeat("🙂", 12000) + telegramRichEnvelopeEnd
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: raw, Format: cronRichFormatV1}
	}
	task := f.runTask(t, f.create(t))
	require.Equal(t, "plain", task.LatestResult.Format)
	require.Equal(t, boundedCronText(raw), task.LatestResult.Text)
}

type cronRichOverlayStore struct {
	cronjob.Store
	task cronjob.Task
	err  error
}

func (s *cronRichOverlayStore) Get(_ context.Context, _ cronjob.GetRequest) (cronjob.Task, error) {
	if s.err != nil {
		return cronjob.Task{}, s.err
	}
	return s.task, nil
}

func TestCronRichMetadataChunksShareReceiptCursor(t *testing.T) {
	for _, failPart := range []int{2, 4} {
		t.Run(fmt.Sprintf("fail part %d", failPart), func(t *testing.T) {
			f := newCronFixture(t)
			f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
				return agentCronRunResult{Text: mustTelegramRichEnvelope("**payload**"), Format: cronRichFormatV1}
			}
			task := f.runTask(t, f.create(t))
			task.ID = strings.Repeat("X", 4200)
			overlay := &cronRichOverlayStore{Store: f.s.store, task: task}
			f.s.store = overlay
			parts, err := cronReportParts(task)
			require.NoError(t, err)
			require.Len(t, parts, 4)
			failed := false
			requests := cronRichTelegram(t, f, func(index int, _ cronRichRequest) bool {
				if index == failPart && !failed {
					failed = true
					return true
				}
				return false
			})
			receipt, err := f.s.sendReport(t.Context(), task)
			require.Error(t, err)
			require.Len(t, receipt.MessageIDs, failPart-1)
			require.True(t, receipt.DeliveredAt.IsZero())
			task.Report.Receipt = receipt
			receipt, err = f.s.sendReport(t.Context(), task)
			require.NoError(t, err)
			require.Len(t, receipt.MessageIDs, len(parts))
			require.False(t, receipt.DeliveredAt.IsZero())
			require.Len(t, *requests, len(parts)+1)
			for i, req := range *requests {
				part := i - 1
				if i <= failPart-1 {
					part = i
				}
				if parts[part].rich {
					require.True(t, strings.HasSuffix(req.method, "/sendRichMessage"))
					require.Equal(t, parts[part].text, req.payload["rich_message"].(map[string]any)["markdown"])
				} else {
					require.True(t, strings.HasSuffix(req.method, "/sendMessage"))
					require.Equal(t, parts[part].text, req.payload["text"])
				}
			}
		})
	}
}

func TestCronRichReportWithoutTopicOrSourceOmitsOptionalFields(t *testing.T) {
	f := newCronFixture(t)
	f.tc.Message.ThreadID = 0
	f.tc.Message.ID = 0
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: mustTelegramRichEnvelope("**body**"), Format: cronRichFormatV1}
	}
	requests := cronRichTelegram(t, f, nil)
	task := f.runTask(t, f.create(t))
	require.Zero(t, task.ThreadID)
	require.Zero(t, task.SourceMessageID)
	task = f.deliver(t, task)
	require.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
	require.Len(t, *requests, 2)
	require.NotContains(t, (*requests)[1].payload, "message_thread_id")
	require.NotContains(t, (*requests)[1].payload, "reply_parameters")
}

func TestCronRichRechecksPolicyAndLeaseBeforeBody(t *testing.T) {
	for _, name := range []string{"policy", "unavailable", "lease", "deleted"} {
		t.Run(name, func(t *testing.T) {
			f := newCronFixture(t)
			f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
				return agentCronRunResult{Text: mustTelegramRichEnvelope("**body**"), Format: cronRichFormatV1}
			}
			task := f.runTask(t, f.create(t))
			overlay := &cronRichOverlayStore{Store: f.s.store, task: task}
			report := *task.Report
			overlay.task.Report = &report
			f.s.store = overlay
			requests := cronRichTelegram(t, f, func(index int, _ cronRichRequest) bool {
				if index == 1 {
					switch name {
					case "lease":
						overlay.task.Report.LeaseToken = "replaced"
					case "deleted":
						overlay.err = cronjob.ErrNotFound
					case "unavailable":
						f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) {
							return orm.AgentCronPolicyState{}, errCronRedisSecretUnderTest
						}
					default:
						f.redis.SAdd(config.BotConfig.RedisConfig.KeyPrefix+"black_list", "7")
					}
				}
				return false
			})
			receipt, err := f.s.sendReport(t.Context(), task)
			switch name {
			case "lease":
				require.ErrorIs(t, err, cronjob.ErrConflict)
			case "deleted":
				require.ErrorIs(t, err, cronjob.ErrNotFound)
			case "unavailable":
				require.ErrorIs(t, err, cronjob.ErrUnavailable)
			default:
				require.ErrorIs(t, err, cronjob.ErrForbidden)
			}
			require.Equal(t, []int{101}, receipt.MessageIDs)
			require.Len(t, *requests, 1)
		})
	}
}

func TestCronRichCanceledHTTPStopsDelivery(t *testing.T) {
	f := newCronFixture(t)
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		return agentCronRunResult{Text: mustTelegramRichEnvelope("**body**"), Format: cronRichFormatV1}
	}
	task := f.runTask(t, f.create(t))
	started := make(chan struct{})
	stopped := make(chan struct{})
	release := make(chan struct{})
	var releaseOnce sync.Once
	releaseServer := func() { releaseOnce.Do(func() { close(release) }) }
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/sendMessage") {
			_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":101}}`))
			return
		}
		close(started)
		select {
		case <-r.Context().Done():
		case <-release:
		}
		close(stopped)
	}))
	t.Cleanup(func() { releaseServer(); server.Close() })
	f.s.bot.URL = server.URL
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	var receipt *cronjob.DeliveryReceipt
	var reportErr error
	go func() { receipt, reportErr = f.s.sendReport(ctx, task); close(done) }()
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("rich HTTP request did not start")
	}
	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("rich HTTP request did not honor cancellation")
	}
	require.Error(t, reportErr)
	require.Equal(t, []int{101}, receipt.MessageIDs)
	releaseServer()
	select {
	case <-stopped:
	case <-time.After(3 * time.Second):
		t.Fatal("fixture rich request did not cancel")
	}
}
