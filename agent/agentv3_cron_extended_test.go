package agentv3

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"csust-got/config"
	"csust-got/cronjob"
	"csust-got/orm"

	"github.com/stretchr/testify/require"
)

func TestCronActualToolInfoScheduleContract(t *testing.T) {
	f := newCronFixture(t)
	items := buildAgentV3Tools(f.tc.Config, config.BotConfig.AgentV3, agentV3SkillCatalog{}, nil)
	found := map[string]bool{}
	for _, item := range items {
		info, err := item.Info(t.Context())
		require.NoError(t, err)
		if info.Name != "delegate" && info.Name != "cron_tasks" {
			continue
		}
		found[info.Name] = true
		params, err := info.ParamsOneOf.ToJSONSchema()
		require.NoError(t, err)
		encoded, err := json.Marshal(params)
		require.NoError(t, err)
		var decoded struct {
			Required   []string `json:"required"`
			Properties map[string]struct {
				Type        string   `json:"type"`
				Description string   `json:"description"`
				Enum        []string `json:"enum"`
			} `json:"properties"`
		}
		require.NoError(t, json.Unmarshal(encoded, &decoded))
		require.Equal(t, "string", decoded.Properties["cron"].Type)
		require.Equal(t, "string", decoded.Properties["prompt"].Type)
		for _, syntax := range []string{"m h dom mon dow", "@at <duration|RFC3339|YYYY-MM-DD HH:MM|HH:MM>", "@at tomorro HH:MM", "@daily HH:MM", "@month/@monthly <1-31> HH:MM", "@week/@weekly <0-7> HH:MM", "@every <duration>", "@at 10m", "@at tomorro 09:00", "@weekly 1 09:00", "@every 90s", "positive whole-millisecond", "no d/w", "0/7 is Sunday"} {
			require.Contains(t, decoded.Properties["cron"].Description, syntax)
		}
		for _, rule := range []string{"configured timezone", "absolute UTC", "strictly in the future", "next local calendar day", "gap", "fold", "earliest strictly future", "once only", "both fold instants", "monthly days are skipped", "fixed-delay", "completion/recovery", "manual execution retry", "no sub-minute start guarantee", "Background agents cannot"} {
			require.Contains(t, info.Desc, rule)
		}
		require.NotContains(t, info.Desc, "recurring five-field numeric cron task")
		if info.Name == "delegate" {
			for _, syntax := range []string{"@at", "@daily", "@month/@monthly", "@week/@weekly", "@every"} {
				require.Contains(t, info.Desc, syntax, "the stable-prefix help must also expose the input syntax")
			}
			require.ElementsMatch(t, []string{"cron", "prompt"}, decoded.Required)
			require.Len(t, decoded.Properties, 2)
			for _, rule := range []string{"one-time or recurring", "this chat and topic", "dedicated runner", "No target override", "## Context", "## Steps", "## Goal", "not now"} {
				require.Contains(t, info.Desc, rule)
			}
		} else {
			require.ElementsMatch(t, []string{"action"}, decoded.Required)
			require.Len(t, decoded.Properties, 8)
			require.ElementsMatch(t, []string{"list", "get", "delete", "update", "retry"}, decoded.Properties["action"].Enum)
			require.Equal(t, "integer", decoded.Properties["expected_version"].Type)
			for _, rule := range []string{"Only the creator", "expected_version", "Update only cron/prompt", "Prompt-only", "explicit new cron", "next_run_at=null", "count toward quota", "user explicitly requests", "side effects", "report_only", "without rerunning the model or moving the schedule"} {
				require.Contains(t, info.Desc, rule)
			}
		}
	}
	require.Len(t, found, 2)
}

func TestCronToolAllScheduleSyntaxAndCanonicalDedup(t *testing.T) {
	f := newCronFixture(t)
	f.now = time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ input, canonical string }{
		{"@at 10m", "@at 2026-09-23T02:10:00Z"},
		{"@at 2026-09-23T10:10:00+08:00", "@at 2026-09-23T02:10:00Z"},
		{"@at 2026-09-23 10:10", "@at 2026-09-23T02:10:00Z"},
		{"@at 10:10", "@at 2026-09-23T02:10:00Z"},
		{"@at tomorro 09:00", "@at 2026-09-24T01:00:00Z"},
		{"@daily 09:05", "5 9 * * *"},
		{"@month 31 09:05", "5 9 31 * *"},
		{"@monthly 31 09:05", "5 9 31 * *"},
		{"@week 1 09:00", "0 9 * * 1"},
		{"@weekly 1 09:00", "0 9 * * 1"},
		{"@every 90s", "@every 1m30s"},
		{"0 9 * * 1", "0 9 * * 1"},
	} {
		t.Run(tc.input, func(t *testing.T) {
			out := f.invoke(t, false, map[string]any{"cron": tc.input, "prompt": testCronPrompt})
			require.NotContains(t, out, "code")
			require.Equal(t, tc.canonical, out["cron"])
			require.Equal(t, "Asia/Shanghai", out["timezone"])
			duplicate := f.invoke(t, false, map[string]any{"cron": tc.canonical, "prompt": testCronPrompt})
			require.Equal(t, true, duplicate["deduplicated"])
			require.Equal(t, out["task_id"], duplicate["task_id"])
		})
	}
	for _, input := range []string{"@at tomorrow 09:00", "@daily", "@weekly Monday 09:00", "@every 1.5ms", "@at 0s", "@at 2026-09-23T02:00:00Z"} {
		require.Equal(t, "invalid_argument", f.invoke(t, false, map[string]any{"cron": input, "prompt": testCronPrompt})["code"])
	}
}

func TestCronToolPromptUpdatesKeepDeadlineAndCapturedTimezone(t *testing.T) {
	for _, expression := range []string{"@at 90s", "@every 90s"} {
		t.Run(expression, func(t *testing.T) {
			f := newCronFixture(t)
			task := f.createSchedule(t, expression)
			f.s.cfg.Timezone = "UTC"
			for _, now := range []time.Time{f.now.Add(time.Second), task.NextRunAt.Add(time.Hour)} {
				f.now = now
				out := f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": task.Version, "prompt": testCronPrompt + " Updated."})
				require.NotContains(t, out, "code")
				require.Equal(t, task.NextRunAt.Format(time.RFC3339Nano), out["next_run_at"])
				require.Equal(t, task.Cron, out["cron"])
				require.Equal(t, "Asia/Shanghai", out["timezone"])
				task.Version++
			}
			out := f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": task.Version, "cron": "@at tomorro 09:00"})
			require.NotContains(t, out, "code")
			schedule, err := cronjob.Resolve("@at tomorro 09:00", "Asia/Shanghai", f.now)
			require.NoError(t, err)
			require.Equal(t, schedule.Expression(), out["cron"])
		})
	}
}

func TestCronOnceAndEveryExplicitExecutionRetry(t *testing.T) {
	for _, expression := range []string{"@at 90s", "@every 90s"} {
		t.Run(expression, func(t *testing.T) {
			f := newCronFixture(t)
			f.s.cfg.ChatCooldownSeconds = 0
			calls := 0
			f.s.run = func(_ context.Context, lease cronjob.Lease) agentCronRunResult {
				calls++
				f.now = f.now.Add(12 * time.Second)
				if calls == 1 {
					return agentCronRunResult{Err: errCronSecretProviderUnderTest}
				}
				require.Equal(t, cronjob.RunRetry, lease.Kind)
				require.Equal(t, 1, lease.RetryCount)
				return agentCronRunResult{Text: "retry result"}
			}
			f.s.report = func(context.Context, cronjob.Task) (*cronjob.DeliveryReceipt, error) {
				return &cronjob.DeliveryReceipt{MessageIDs: []int{1}}, nil
			}
			task := f.runTask(t, f.createSchedule(t, expression))
			require.Equal(t, cronjob.OutcomeFailed, task.LatestResult.Outcome)
			task = f.deliver(t, task)
			f.now = f.now.Add(10 * time.Second)
			out := f.invoke(t, true, map[string]any{"action": "retry", "task_id": task.ID, "expected_version": task.Version, "run_id": task.LatestResult.RunID})
			require.Equal(t, "execution", out["mode"])
			candidates, err := f.s.store.Due(t.Context(), cronjob.DueRequest{Coordinator: f.s.coordinator, Now: f.now})
			require.NoError(t, err)
			require.Len(t, candidates, 1)
			require.Equal(t, cronjob.RunRetry, candidates[0].Kind)
			f.s.executeCandidate(t.Context(), candidates[0])
			task, err = f.s.store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			require.NoError(t, err)
			require.Equal(t, 2, calls)
			require.Equal(t, cronjob.OutcomeSucceeded, task.LatestResult.Outcome)
			require.Equal(t, 1, task.LatestResult.RetryCount)
			task = f.deliver(t, task)
			if expression == "@at 90s" {
				require.True(t, task.NextRunAt.IsZero())
				out = f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": task.Version, "prompt": testCronPrompt})
				require.Equal(t, "conflict", out["code"])
				out = f.invoke(t, true, map[string]any{"action": "get", "task_id": task.ID})
				require.Nil(t, out["next_run_at"])
				require.NotNil(t, out["latest_result"])
				out = f.invoke(t, true, map[string]any{"action": "update", "task_id": task.ID, "expected_version": task.Version, "cron": "@at 10m"})
				require.NotContains(t, out, "code")
				require.Equal(t, f.now.Add(10*time.Minute).Format(time.RFC3339Nano), out["next_run_at"])
			} else {
				require.True(t, task.NextRunAt.Equal(task.LatestResult.FinishedAt.Add(90*time.Second)))
			}
		})
	}
}

func TestCronOncePolicySkipEndsSchedule(t *testing.T) {
	f := newCronFixture(t)
	task := f.createSchedule(t, "@at 90s")
	f.s.policy = func(context.Context, int64, int64) (orm.AgentCronPolicyState, error) {
		return orm.AgentCronPolicyState{}, cronjob.ErrUnavailable
	}
	f.s.run = func(context.Context, cronjob.Lease) agentCronRunResult {
		t.Fatal("denied task must not generate")
		return agentCronRunResult{}
	}
	task = f.runTask(t, task)
	require.True(t, task.NextRunAt.IsZero())
	require.Equal(t, cronjob.OutcomeSkipped, task.LatestResult.Outcome)
	require.Equal(t, cronjob.RetryUnavailable, task.LatestResult.RetryStatus)
	task = f.deliver(t, task)
	require.Equal(t, cronjob.DeliverySuppressed, task.Report.State)
	f.now = f.now.Add(24 * time.Hour)
	candidates, err := f.s.store.Due(t.Context(), cronjob.DueRequest{Coordinator: f.s.coordinator, Now: f.now})
	require.NoError(t, err)
	require.Empty(t, candidates)
}
