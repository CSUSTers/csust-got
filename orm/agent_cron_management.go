package orm

import (
	"context"
	"errors"
	"strings"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
)

func (s *agentCronStore) Create(ctx context.Context, request cronjob.CreateRequest) (cronjob.CreateResult, error) {
	var result cronjob.CreateResult
	if err := validateScope(request.Actor.Scope); err != nil {
		return result, err
	}
	if request.Actor.UserID == 0 || request.SourceAgent == "" || request.Now.IsZero() || request.NextRunAt.IsZero() || !request.NextRunAt.After(request.Now) {
		return result, cronjob.NewError(cronjob.CodeInvalidArgument, "creator, source agent, now, and future next run are required")
	}
	cronValue := normalizeCronValue(request.Cron)
	timezone := strings.TrimSpace(request.Timezone)
	prompt := normalizePromptValue(request.Prompt)
	if err := validateTaskText(cronValue, timezone, prompt); err != nil {
		return result, err
	}
	schedule, err := cronjob.Parse(cronValue, timezone)
	if err != nil {
		return result, err
	}
	cronValue = schedule.Expression()
	timezone = schedule.Timezone()
	if request.MaxTasksPerChat <= 0 {
		return result, cronjob.NewError(cronjob.CodeInvalidArgument, "task quota must be positive")
	}

	scope := request.Actor.Scope
	dedup := agentCronDedupKey(request.Actor.UserID, cronValue, timezone, prompt)
	err = s.watchCoordinator(ctx, scope.Coordinator(), func(tx *redis.Tx, pipe redis.Pipeliner) error {
		result = cronjob.CreateResult{}
		existingID, err := tx.HGet(ctx, s.dedupKey(scope), dedup).Result()
		if err == nil {
			existing, loadErr := loadAgentCronTask(ctx, tx, s.taskKey(scope, existingID))
			if loadErr != nil {
				return loadErr
			}
			if existing != nil && existing.Task.CreatorID == request.Actor.UserID && existing.DedupKey == dedup {
				result = cronjob.CreateResult{Task: existing.Task, Deduplicated: true}
				return nil
			}
			pipe.HDel(ctx, s.dedupKey(scope), dedup)
		} else if !errors.Is(err, redis.Nil) {
			return err
		}

		count, err := tx.ZCard(ctx, s.tasksKey(scope)).Result()
		if err != nil {
			return err
		}
		if count >= int64(request.MaxTasksPerChat) {
			return cronjob.NewError(cronjob.CodeQuotaExceeded, "chat cron task quota exceeded")
		}
		id, err := agentCronRandomID("cron_")
		if err != nil {
			return err
		}
		task := cronjob.Task{
			ID:              id,
			Scope:           scope,
			CreatorID:       request.Actor.UserID,
			SourceAgent:     request.SourceAgent,
			ChatType:        request.ChatType,
			ThreadID:        request.ThreadID,
			SourceMessageID: request.SourceMessageID,
			Cron:            cronValue,
			Timezone:        timezone,
			Prompt:          prompt,
			Version:         1,
			NextRunAt:       request.NextRunAt,
			CreatedAt:       request.Now,
			UpdatedAt:       request.Now,
		}
		stored := storedAgentCronTask{Task: task, DedupKey: dedup}
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		member := agentCronTaskMember(scope, id)
		pipe.Set(ctx, s.taskKey(scope, id), data, 0)
		pipe.ZAdd(ctx, s.tasksKey(scope), redis.Z{Score: agentCronTimeScore(request.Now), Member: id})
		pipe.HSet(ctx, s.dedupKey(scope), dedup, id)
		pipe.ZAdd(ctx, s.dueKey(scope.Coordinator()), redis.Z{Score: agentCronTimeScore(task.NextRunAt), Member: member})
		result = cronjob.CreateResult{Task: task}
		return nil
	})
	if err != nil {
		return cronjob.CreateResult{}, err
	}
	return result, err
}

func (s *agentCronStore) List(ctx context.Context, request cronjob.ListRequest) (cronjob.Page, error) {
	var page cronjob.Page
	if err := validateScope(request.Scope); err != nil {
		return page, err
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 20
	}
	if limit > 50 {
		limit = 50
	}
	ids, err := s.client.ZRange(ctx, s.tasksKey(request.Scope), 0, -1).Result()
	if err != nil {
		return page, agentCronPublicError(err)
	}
	start := 0
	if request.Cursor != "" {
		start = len(ids)
		for i, id := range ids {
			if id == request.Cursor {
				start = i + 1
				break
			}
		}
	}
	for _, id := range ids[start:] {
		stored, loadErr := loadAgentCronTask(ctx, s.client, s.taskKey(request.Scope, id))
		if loadErr != nil {
			return page, agentCronPublicError(loadErr)
		}
		if stored == nil || stored.Task.Scope != request.Scope {
			continue
		}
		page.Tasks = append(page.Tasks, stored.Task)
		if len(page.Tasks) == limit {
			if start+len(page.Tasks) < len(ids) {
				page.NextCursor = id
			}
			break
		}
	}
	return page, nil
}

func (s *agentCronStore) Get(ctx context.Context, request cronjob.GetRequest) (cronjob.Task, error) {
	if err := validateScope(request.Scope); err != nil {
		return cronjob.Task{}, err
	}
	if request.TaskID == "" {
		return cronjob.Task{}, cronjob.NewError(cronjob.CodeInvalidArgument, "task id is required")
	}
	stored, err := loadAgentCronTask(ctx, s.client, s.taskKey(request.Scope, request.TaskID))
	if err != nil {
		return cronjob.Task{}, agentCronPublicError(err)
	}
	if stored == nil || stored.Task.Scope != request.Scope {
		return cronjob.Task{}, cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
	}
	return stored.Task, nil
}

func (s *agentCronStore) Update(ctx context.Context, request cronjob.UpdateRequest) (cronjob.Task, error) {
	var result cronjob.Task
	if err := validateScope(request.Actor.Scope); err != nil {
		return result, err
	}
	if request.Actor.UserID == 0 || request.TaskID == "" || request.ExpectedVersion <= 0 || request.Now.IsZero() || request.NextRunAt.IsZero() || !request.NextRunAt.After(request.Now) || (request.Cron == nil && request.Prompt == nil) {
		return result, cronjob.NewError(cronjob.CodeInvalidArgument, "valid actor, task, version, update, now, and future next run are required")
	}
	scope := request.Actor.Scope
	err := s.watchCoordinator(ctx, scope.Coordinator(), func(tx *redis.Tx, pipe redis.Pipeliner) error {
		result = cronjob.Task{}
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.TaskID))
		if err != nil {
			return err
		}
		if stored == nil || stored.Task.Scope != scope {
			return cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
		}
		if stored.Task.CreatorID != request.Actor.UserID {
			return cronjob.NewError(cronjob.CodeForbidden, "only the task creator may update it")
		}
		if stored.Task.Version != request.ExpectedVersion {
			return cronjob.NewError(cronjob.CodeConflict, "task version changed")
		}
		if stored.Task.ActiveRun != nil || stored.Task.LatestResult != nil && stored.Task.LatestResult.RetryStatus == cronjob.RetryQueued || agentCronReportBlocks(stored.Task) {
			return cronjob.NewError(cronjob.CodeConflict, "task has active execution, retry, or report work")
		}

		cronValue := stored.Task.Cron
		prompt := stored.Task.Prompt
		if request.Cron != nil {
			cronValue = normalizeCronValue(*request.Cron)
		}
		if request.Prompt != nil {
			prompt = normalizePromptValue(*request.Prompt)
		}
		if err := validateTaskText(cronValue, stored.Task.Timezone, prompt); err != nil {
			return err
		}
		schedule, err := cronjob.Parse(cronValue, stored.Task.Timezone)
		if err != nil {
			return err
		}
		cronValue = schedule.Expression()
		newDedup := agentCronDedupKey(stored.Task.CreatorID, cronValue, stored.Task.Timezone, prompt)
		if newDedup != stored.DedupKey {
			existingID, getErr := tx.HGet(ctx, s.dedupKey(scope), newDedup).Result()
			if getErr == nil && existingID != stored.Task.ID {
				return cronjob.NewError(cronjob.CodeConflict, "an identical cron task already exists")
			}
			if getErr != nil && !errors.Is(getErr, redis.Nil) {
				return getErr
			}
			pipe.HDel(ctx, s.dedupKey(scope), stored.DedupKey)
			pipe.HSet(ctx, s.dedupKey(scope), newDedup, stored.Task.ID)
			stored.DedupKey = newDedup
		}
		stored.Task.Cron = cronValue
		stored.Task.Prompt = prompt
		stored.Task.Version++
		stored.Task.NextRunAt = request.NextRunAt
		stored.Task.LatestResult = nil
		stored.Task.Report = nil
		stored.Task.UpdatedAt = request.Now
		stored.RetryReadyAt = time.Time{}
		stored.ManualReportRetries = 0
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		pipe.ZAdd(ctx, s.dueKey(scope.Coordinator()), redis.Z{Score: agentCronTimeScore(stored.Task.NextRunAt), Member: member})
		pipe.ZRem(ctx, s.retryKey(scope.Coordinator()), member)
		pipe.ZRem(ctx, s.reportsKey(scope.Coordinator()), member)
		result = stored.Task
		return nil
	})
	if err != nil {
		return cronjob.Task{}, err
	}
	return result, err
}

func (s *agentCronStore) Delete(ctx context.Context, request cronjob.DeleteRequest) error {
	if err := validateScope(request.Actor.Scope); err != nil {
		return err
	}
	if request.Actor.UserID == 0 || request.TaskID == "" || request.ExpectedVersion <= 0 {
		return cronjob.NewError(cronjob.CodeInvalidArgument, "valid actor, task, and version are required")
	}
	scope := request.Actor.Scope
	return s.watchCoordinator(ctx, scope.Coordinator(), func(tx *redis.Tx, pipe redis.Pipeliner) error {
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.TaskID))
		if err != nil {
			return err
		}
		if stored == nil || stored.Task.Scope != scope {
			return cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
		}
		if stored.Task.CreatorID != request.Actor.UserID {
			return cronjob.NewError(cronjob.CodeForbidden, "only the task creator may delete it")
		}
		if stored.Task.Version != request.ExpectedVersion {
			return cronjob.NewError(cronjob.CodeConflict, "task version changed")
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		pipe.Del(ctx, s.taskKey(scope, stored.Task.ID))
		pipe.ZRem(ctx, s.tasksKey(scope), stored.Task.ID)
		pipe.HDel(ctx, s.dedupKey(scope), stored.DedupKey)
		pipe.ZRem(ctx, s.dueKey(scope.Coordinator()), member)
		pipe.ZRem(ctx, s.retryKey(scope.Coordinator()), member)
		pipe.ZRem(ctx, s.reportsKey(scope.Coordinator()), member)
		return nil
	})
}

func (s *agentCronStore) Retry(ctx context.Context, request cronjob.RetryRequest) (cronjob.RetryResult, error) {
	var result cronjob.RetryResult
	if err := validateScope(request.Actor.Scope); err != nil {
		return result, err
	}
	if request.Actor.UserID == 0 || request.TaskID == "" || request.RunID == "" || request.ExpectedVersion <= 0 || request.Now.IsZero() || request.MaxRetries < 0 {
		return result, cronjob.NewError(cronjob.CodeInvalidArgument, "valid actor, task, version, run, retry limit, and time are required")
	}
	scope := request.Actor.Scope
	err := s.watchCoordinator(ctx, scope.Coordinator(), func(tx *redis.Tx, pipe redis.Pipeliner) error {
		result = cronjob.RetryResult{}
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.TaskID))
		if err != nil {
			return err
		}
		if stored == nil || stored.Task.Scope != scope {
			return cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
		}
		if stored.Task.CreatorID != request.Actor.UserID {
			return cronjob.NewError(cronjob.CodeForbidden, "only the task creator may retry it")
		}
		if stored.Task.Version != request.ExpectedVersion {
			return cronjob.NewError(cronjob.CodeConflict, "task version changed")
		}
		if stored.Task.ActiveRun != nil || stored.Task.LatestResult == nil || stored.Task.LatestResult.RunID != request.RunID {
			return cronjob.NewError(cronjob.CodeConflict, "run is stale or still active")
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		switch stored.Task.LatestResult.Outcome {
		case cronjob.OutcomeSucceeded:
			if agentCronReportExpired(stored.Task, request.Now) {
				return cronjob.NewError(cronjob.CodeConflict, "the report retry window has expired")
			}
			if stored.Task.Report == nil || stored.Task.Report.RunID != request.RunID || stored.Task.Report.State != cronjob.DeliveryFailed && stored.Task.Report.State != cronjob.DeliveryExhausted {
				return cronjob.NewError(cronjob.CodeConflict, "the successful run has no failed report to retry")
			}
			if stored.ManualReportRetries >= request.MaxRetries || stored.Task.Report.Attempt >= agentCronAutoReportAttempts+request.MaxRetries {
				return cronjob.NewError(cronjob.CodeConflict, "report retry budget exhausted")
			}
			stored.Task.Version++
			stored.ManualReportRetries++
			stored.Task.Report.TaskVersion = stored.Task.Version
			stored.Task.Report.State = cronjob.DeliveryPending
			stored.Task.Report.NextAttemptAt = request.Now
			stored.Task.Report.LeaseToken = ""
			stored.Task.Report.LeaseExpiresAt = time.Time{}
			stored.Task.UpdatedAt = request.Now
			pipe.ZAdd(ctx, s.reportsKey(scope.Coordinator()), redis.Z{Score: agentCronTimeScore(request.Now), Member: member})
			result.Mode = cronjob.RetryReportOnly
		default:
			if agentCronReportBlocks(stored.Task) {
				return cronjob.NewError(cronjob.CodeConflict, "the previous report is still pending")
			}
			if stored.Task.LatestResult.RetryStatus != cronjob.RetryAvailable || stored.Task.LatestResult.RetryCount >= request.MaxRetries {
				return cronjob.NewError(cronjob.CodeConflict, "execution retry is unavailable or exhausted")
			}
			stored.Task.Version++
			stored.Task.LatestResult.RetryStatus = cronjob.RetryQueued
			stored.RetryReadyAt = request.Now
			stored.Task.UpdatedAt = request.Now
			pipe.ZAdd(ctx, s.retryKey(scope.Coordinator()), redis.Z{Score: agentCronTimeScore(request.Now), Member: member})
			result.Mode = cronjob.RetryExecution
		}
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		result.Task = stored.Task
		return nil
	})
	if err != nil {
		return cronjob.RetryResult{}, err
	}
	return result, err
}
