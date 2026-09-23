package agentv3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"time"
	"unicode/utf8"

	"csust-got/cronjob"

	"github.com/cloudwego/eino/components/tool"
	"github.com/cloudwego/eino/schema"
)

const cronScheduleHelp = "Schedules use the captured configured timezone. @at is resolved once to an absolute UTC instant; new deadlines must be strictly in the future. HH:MM is 24-hour time. Bare @at HH:MM selects the next existing local time; tomorro (exact spelling) means the next local calendar day. Explicit local dates/tomorro in a DST gap are rejected; a fold chooses the earliest strictly future instant, once only. Recurring calendar aliases retain five-field cron DST behavior (skip gaps, both fold instants); nonexistent monthly days are skipped. @every is fixed-delay from creation/rescheduling or completion/recovery; manual execution retry also moves the next time. Polling, cooldown and report blocking mean no sub-minute start guarantee."
const cronScheduleDesc = "m h dom mon dow; numeric *, lists, ranges, steps; @at <duration|RFC3339|YYYY-MM-DD HH:MM|HH:MM>; @at tomorro HH:MM; @daily HH:MM; @month/@monthly <1-31> HH:MM; @week/@weekly <0-7> HH:MM; @every <duration>. Examples: @at 10m; @at tomorro 09:00; @weekly 1 09:00; @every 90s. Duration: positive whole-millisecond Go duration (e.g. 1h30m), no d/w units. Weekday 0/7 is Sunday."
const cronDelegateHelp = "Create a one-time or recurring scheduled task in this chat and topic using the configured dedicated runner. No target override. Supply a self-contained prompt with nonempty sections in this exact order: ## Context\n(background)\n## Steps\n(actions)\n## Goal\n(expected result). Do not rely on conversation references. First run is the next scheduled time, not now. Background agents cannot create or manage tasks. Supported cron inputs: " + cronScheduleDesc + " " + cronScheduleHelp
const cronTasksHelp = "Manage this chat's cron tasks: list/get/delete/update/retry. Only the creator may mutate. Get current task_id, version and latest run_id before mutations; pass expected_version. Update only cron/prompt. Prompt-only updates preserve the scheduled deadline, including once/every; completed one-time tasks require an explicit new cron to reschedule. One-time completion retains the task, result, report and explicit retry entry with next_run_at=null; retained tasks still count toward quota. Retry ONLY after the user explicitly requests it; warn that previous execution may already have caused side effects. A successful execution with failed delivery is report_only: resend saved output without rerunning the model or moving the schedule. Delete cannot undo effects already underway. Background agents cannot create or manage tasks. " + cronScheduleHelp

type cronTool struct {
	manage  bool
	service *agentCronService
}

func (t *cronTool) Info(context.Context) (*schema.ToolInfo, error) {
	params := map[string]*schema.ParameterInfo{
		agentV3FieldCron: {Type: schema.String, Desc: cronScheduleDesc, Required: !t.manage},
		"prompt":         {Type: schema.String, Desc: "Self-contained ## Context, ## Steps, ## Goal sections", Required: !t.manage},
	}
	name, desc := agentV3ToolDelegate, cronDelegateHelp
	if t.manage {
		name, desc = agentV3ToolCronTasks, cronTasksHelp
		params["action"] = &schema.ParameterInfo{Type: schema.String, Enum: []string{agentV3ActionList, "get", "delete", agentV3ActionUpdate, "retry"}, Required: true}
		params["task_id"] = &schema.ParameterInfo{Type: schema.String}
		params["expected_version"] = &schema.ParameterInfo{Type: schema.Integer}
		params["run_id"] = &schema.ParameterInfo{Type: schema.String}
		params["cursor"] = &schema.ParameterInfo{Type: schema.String}
		params["limit"] = &schema.ParameterInfo{Type: schema.Integer, Desc: "Default 20; maximum 50"}
	}
	return &schema.ToolInfo{Name: name, Desc: desc, ParamsOneOf: schema.NewParamsOneOfByParams(params)}, nil
}

type cronToolArgs struct {
	Action          string  `json:"action"`
	TaskID          string  `json:"task_id"`
	ExpectedVersion int64   `json:"expected_version"`
	Cron            *string `json:"cron"`
	Prompt          *string `json:"prompt"`
	RunID           string  `json:"run_id"`
	Cursor          string  `json:"cursor"`
	Limit           int     `json:"limit"`
}

func cronJSON(value any) (string, error) {
	b, err := json.Marshal(value)
	return string(b), err
}

func cronToolError(err error) (string, error) {
	code := cronjob.CodeUnavailable
	var domain *cronjob.Error
	if errors.As(err, &domain) {
		code = domain.Code
	}
	// Store and transport errors may contain Redis URLs or prompt content.
	return cronJSON(map[string]any{"code": code, agentV3FieldMessage: string(code) + ": task operation was not completed"})
}

func (t *cronTool) InvokableRun(ctx context.Context, input string, _ ...tool.Option) (string, error) {
	tc := GetTurnContext(ctx)
	if tc != nil && tc.Background {
		return cronJSON(map[string]string{"code": "background_forbidden", agentV3FieldMessage: "Background turns cannot delegate or manage cron tasks, including through subagents."})
	}
	s := t.service
	if s == nil {
		s = cronService.Load()
	}
	if s == nil {
		return cronToolError(cronjob.ErrUnavailable)
	}
	actor, err := s.actor(tc)
	if err != nil {
		return cronToolError(err)
	}
	if err := s.allowed(ctx, cronjob.Task{Scope: actor.Scope, CreatorID: actor.UserID, SourceAgent: tc.Config.Name}); err != nil {
		return cronToolError(err)
	}
	if !utf8.ValidString(input) {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	var args cronToolArgs
	decoder := json.NewDecoder(bytes.NewBufferString(input))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&args); err != nil {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	if err := decoder.Decode(new(any)); err != io.EOF {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	now := s.now()
	if !t.manage {
		if args.Cron == nil || args.Prompt == nil || args.Action != "" || args.TaskID != "" || args.ExpectedVersion != 0 || args.RunID != "" || args.Cursor != "" || args.Limit != 0 {
			return cronToolError(cronjob.ErrInvalidArgument)
		}
		if err := cronjob.ValidatePrompt(*args.Prompt, s.cfg.MaxPromptBytes); err != nil {
			return cronToolError(err)
		}
		schedule, err := cronjob.Resolve(*args.Cron, s.cfg.Timezone, now)
		if err != nil {
			return cronToolError(err)
		}
		next, err := schedule.Next(now)
		if err != nil {
			return cronToolError(err)
		}
		result, err := s.store.Create(ctx, cronjob.CreateRequest{Actor: actor, SourceAgent: tc.Config.Name, ChatType: string(tc.Message.Chat.Type), ThreadID: int64(tc.Message.ThreadID), SourceMessageID: tc.Message.ID, Cron: schedule.Expression(), Timezone: schedule.Timezone(), Prompt: strings.TrimSpace(*args.Prompt), NextRunAt: next, Now: now, MaxTasksPerChat: s.cfg.MaxTasksPerChat})
		if err != nil {
			return cronToolError(err)
		}
		return cronJSON(map[string]any{"task_id": result.Task.ID, agentV3FieldVersion: result.Task.Version, agentV3FieldCron: result.Task.Cron, "timezone": result.Task.Timezone, "next_run_at": cronNextView(result.Task.NextRunAt), "deduplicated": result.Deduplicated})
	}
	if args.Action != agentV3ActionUpdate && (args.Cron != nil || args.Prompt != nil) {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	if args.Action == agentV3ActionList {
		if args.Limit == 0 {
			args.Limit = 20
		}
		if args.Limit < 1 || args.Limit > 50 {
			return cronToolError(cronjob.ErrInvalidArgument)
		}
		page, err := s.store.List(ctx, cronjob.ListRequest{Scope: actor.Scope, Cursor: args.Cursor, Limit: args.Limit})
		if err != nil {
			return cronToolError(err)
		}
		tasks := make([]any, 0, len(page.Tasks))
		for _, task := range page.Tasks {
			tasks = append(tasks, cronTaskView(task, false))
		}
		return cronJSON(map[string]any{"tasks": tasks, "next_cursor": page.NextCursor})
	}
	if args.TaskID == "" {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	if args.Action == "get" {
		task, err := s.store.Get(ctx, cronjob.GetRequest{Scope: actor.Scope, TaskID: args.TaskID})
		if err != nil {
			return cronToolError(err)
		}
		return cronJSON(cronTaskView(task, true))
	}
	if args.ExpectedVersion <= 0 {
		return cronToolError(cronjob.ErrInvalidArgument)
	}
	switch args.Action {
	case "delete":
		err := s.store.Delete(ctx, cronjob.DeleteRequest{Actor: actor, TaskID: args.TaskID, ExpectedVersion: args.ExpectedVersion, Now: now})
		if err != nil {
			return cronToolError(err)
		}
		return cronJSON(map[string]any{"deleted": true, agentV3FieldMessage: "Already-started external effects or messages cannot be undone."})
	case agentV3ActionUpdate:
		if args.Cron == nil && args.Prompt == nil {
			return cronToolError(cronjob.ErrInvalidArgument)
		}
		if args.Prompt != nil {
			if err := cronjob.ValidatePrompt(*args.Prompt, s.cfg.MaxPromptBytes); err != nil {
				return cronToolError(err)
			}
			trimmed := strings.TrimSpace(*args.Prompt)
			args.Prompt = &trimmed
		}
		task, err := s.store.Get(ctx, cronjob.GetRequest{Scope: actor.Scope, TaskID: args.TaskID})
		if err != nil {
			return cronToolError(err)
		}
		next := task.NextRunAt
		if args.Cron != nil {
			schedule, err := cronjob.Resolve(*args.Cron, task.Timezone, now)
			if err != nil {
				return cronToolError(err)
			}
			next, err = schedule.Next(now)
			if err != nil {
				return cronToolError(err)
			}
			normalized := schedule.Expression()
			args.Cron = &normalized
		}
		updated, err := s.store.Update(ctx, cronjob.UpdateRequest{Actor: actor, TaskID: args.TaskID, ExpectedVersion: args.ExpectedVersion, Cron: args.Cron, Prompt: args.Prompt, NextRunAt: next, Now: now})
		if err != nil {
			return cronToolError(err)
		}
		return cronJSON(cronTaskView(updated, true))
	case "retry":
		if args.RunID == "" {
			return cronToolError(cronjob.ErrInvalidArgument)
		}
		result, err := s.store.Retry(ctx, cronjob.RetryRequest{Actor: actor, TaskID: args.TaskID, ExpectedVersion: args.ExpectedVersion, RunID: args.RunID, Now: now, MaxRetries: s.cfg.MaxManualRetries})
		if err != nil {
			return cronToolError(err)
		}
		return cronJSON(map[string]any{"task": cronTaskView(result.Task, true), "mode": result.Mode})
	default:
		return cronToolError(cronjob.ErrInvalidArgument)
	}
}

func cronTaskView(task cronjob.Task, full bool) map[string]any {
	view := map[string]any{"task_id": task.ID, agentV3FieldVersion: task.Version, "creator_id": task.CreatorID, "source_agent": task.SourceAgent, agentV3FieldCron: task.Cron, "timezone": task.Timezone, "next_run_at": cronNextView(task.NextRunAt), "running": task.ActiveRun != nil}
	if full {
		view["prompt"] = task.Prompt
	}
	if task.ActiveRun != nil {
		view["active_run_id"] = task.ActiveRun.RunID
	}
	if r := task.LatestResult; r != nil {
		result := map[string]any{"run_id": r.RunID, "outcome": r.Outcome, "retry_status": r.RetryStatus, "retry_count": r.RetryCount, "finished_at": r.FinishedAt, "error_code": r.ErrorCode, "side_effects_possible": r.SideEffectsPossible}
		if full {
			result["text"] = r.Text
			result["error_message"] = r.ErrorMessage
		}
		view["latest_result"] = result
	}
	if r := task.Report; r != nil {
		view["report"] = map[string]any{"run_id": r.RunID, "state": r.State, "attempt": r.Attempt, "next_attempt_at": r.NextAttemptAt}
	}
	return view
}

func cronNextView(next time.Time) any {
	if next.IsZero() {
		return nil
	}
	return next
}

func cronNext(task cronjob.Task, now time.Time) (time.Time, error) {
	schedule, err := cronjob.Parse(task.Cron, task.Timezone)
	if err != nil {
		return time.Time{}, err
	}
	next, _, err := schedule.AfterRun(now)
	return next, err
}
