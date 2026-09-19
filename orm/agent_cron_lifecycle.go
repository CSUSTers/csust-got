package orm

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"strings"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
)

func (s *agentCronStore) Due(ctx context.Context, request cronjob.DueRequest) ([]cronjob.Candidate, error) {
	if err := validateCoordinator(request.Coordinator); err != nil {
		return nil, err
	}
	if request.Now.IsZero() {
		return nil, cronjob.NewError(cronjob.CodeInvalidArgument, "current time is required")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	candidates := make([]cronjob.Candidate, 0, limit)
	seenChats := make(map[int64]struct{}, limit)
	for _, kind := range []cronjob.RunKind{cronjob.RunScheduled, cronjob.RunRetry} {
		if len(candidates) == limit {
			break
		}
		found, err := s.dueCandidatesFromIndex(ctx, request, kind, limit-len(candidates), seenChats)
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, found...)
	}
	return candidates, nil
}

type agentCronDueEntry struct {
	scope   cronjob.Scope
	taskID  string
	score   int64
	taskCmd *redis.StringCmd
}

func (s *agentCronStore) dueCandidatesFromIndex(ctx context.Context, request cronjob.DueRequest, kind cronjob.RunKind, limit int, seenChats map[int64]struct{}) ([]cronjob.Candidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	indexKey := s.dueKey(request.Coordinator)
	if kind == cronjob.RunRetry {
		indexKey = s.retryKey(request.Coordinator)
	}
	batchSize := int64(128)
	if scaled := int64(limit * 2); scaled > batchSize {
		batchSize = scaled
	}
	if batchSize > 512 {
		batchSize = 512
	}
	maxScore := strconv.FormatInt(request.Now.UnixMilli(), 10)
	result := make([]cronjob.Candidate, 0, limit)
	var offset int64
	for len(result) < limit {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		items, err := s.client.ZRangeByScoreWithScores(ctx, indexKey, &redis.ZRangeBy{
			Min: agentCronNegativeInfinity, Max: maxScore, Offset: offset, Count: batchSize,
		}).Result()
		if err != nil {
			return nil, agentCronPublicError(err)
		}
		if len(items) == 0 {
			break
		}
		offset += int64(len(items))
		entries, lockCommands, cooldownCommands, err := s.loadDueEntries(ctx, request.Coordinator, items)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			if len(result) == limit {
				break
			}
			if _, seen := seenChats[entry.scope.ChatID]; seen {
				continue
			}
			stored, err := decodeAgentCronTaskCommand(entry.taskCmd)
			if err != nil {
				return nil, agentCronPublicError(err)
			}
			if stored == nil || stored.Task.Scope != entry.scope || stored.Task.ID != entry.taskID || stored.Task.ActiveRun != nil || agentCronReportBlocks(stored.Task) {
				continue
			}
			runnable, err := agentCronChatRunnable(lockCommands[entry.scope.ChatID], cooldownCommands[entry.scope.ChatID], request.Now)
			if err != nil {
				return nil, agentCronPublicError(err)
			}
			if !runnable {
				continue
			}
			candidate, ok := agentCronCandidateFromStored(*stored, kind, entry.score, request.Now)
			if !ok {
				continue
			}
			seenChats[entry.scope.ChatID] = struct{}{}
			result = append(result, candidate)
		}
		if int64(len(items)) < batchSize {
			break
		}
	}
	return result, nil
}

func (s *agentCronStore) loadDueEntries(ctx context.Context, coordinator cronjob.Coordinator, items []redis.Z) ([]agentCronDueEntry, map[int64]*redis.StringCmd, map[int64]*redis.StringCmd, error) {
	entries := make([]agentCronDueEntry, 0, len(items))
	pipe := s.client.Pipeline()
	lockCommands := make(map[int64]*redis.StringCmd)
	cooldownCommands := make(map[int64]*redis.StringCmd)
	for _, item := range items {
		member, ok := item.Member.(string)
		if !ok {
			continue
		}
		scope, taskID, err := decodeAgentCronTaskMember(member, coordinator)
		if err != nil {
			continue
		}
		entry := agentCronDueEntry{scope: scope, taskID: taskID, score: int64(item.Score)}
		entry.taskCmd = pipe.Get(ctx, s.taskKey(scope, taskID))
		entries = append(entries, entry)
		if _, ok := lockCommands[scope.ChatID]; !ok {
			field := strconv.FormatInt(scope.ChatID, 10)
			lockCommands[scope.ChatID] = pipe.HGet(ctx, s.chatLocksKey(coordinator), field)
			cooldownCommands[scope.ChatID] = pipe.HGet(ctx, s.cooldownsKey(coordinator), field)
		}
	}
	if len(entries) == 0 {
		return entries, lockCommands, cooldownCommands, nil
	}
	_, err := pipe.Exec(ctx)
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, nil, nil, agentCronPublicError(err)
	}
	return entries, lockCommands, cooldownCommands, nil
}

func decodeAgentCronTaskCommand(command *redis.StringCmd) (*storedAgentCronTask, error) {
	data, err := command.Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var stored storedAgentCronTask
	if err := json.Unmarshal(data, &stored); err != nil {
		return nil, err
	}
	return &stored, nil
}

func agentCronChatRunnable(lockCommand, cooldownCommand *redis.StringCmd, now time.Time) (bool, error) {
	if lockCommand != nil {
		lock, err := lockCommand.Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			return false, err
		}
		if err == nil {
			parts := strings.SplitN(lock, "|", 2)
			if len(parts) != 2 || parts[0] == "" {
				return false, errInvalidAgentCronChatLock
			}
			expiresAt, parseErr := strconv.ParseInt(parts[1], 10, 64)
			if parseErr != nil {
				return false, parseErr
			}
			if expiresAt > now.UnixMilli() {
				return false, nil
			}
		}
	}
	if cooldownCommand != nil {
		cooldown, err := cooldownCommand.Int64()
		if err != nil && !errors.Is(err, redis.Nil) {
			return false, err
		}
		if err == nil && cooldown > now.UnixMilli() {
			return false, nil
		}
	}
	return true, nil
}

func agentCronCandidateFromStored(stored storedAgentCronTask, kind cronjob.RunKind, indexScore int64, now time.Time) (cronjob.Candidate, bool) {
	task := stored.Task
	switch kind {
	case cronjob.RunScheduled:
		if task.NextRunAt.After(now) || task.NextRunAt.UnixMilli() != indexScore {
			return cronjob.Candidate{}, false
		}
		return cronjob.Candidate{Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version, Kind: kind, ScheduledAt: task.NextRunAt}, true
	case cronjob.RunRetry:
		if task.LatestResult == nil || task.LatestResult.RetryStatus != cronjob.RetryQueued || stored.RetryReadyAt.After(now) || stored.RetryReadyAt.UnixMilli() != indexScore || !task.NextRunAt.After(now) {
			return cronjob.Candidate{}, false
		}
		return cronjob.Candidate{
			Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version, Kind: kind,
			ScheduledAt: task.LatestResult.ScheduledAt, RetryOfRunID: task.LatestResult.RunID,
			RetryCount: task.LatestResult.RetryCount + 1,
		}, true
	default:
		return cronjob.Candidate{}, false
	}
}

func (s *agentCronStore) Claim(ctx context.Context, request cronjob.ClaimRequest) (cronjob.Lease, error) {
	var lease cronjob.Lease
	if err := validateScope(request.Candidate.Scope); err != nil {
		return lease, err
	}
	if request.Candidate.TaskID == "" || request.Candidate.TaskVersion <= 0 || request.RunID == "" || request.Now.IsZero() || request.RunTimeout <= 0 || request.GracePeriod < 0 || request.MaxConcurrency <= 0 || request.ChatCooldown < 0 {
		return lease, cronjob.NewError(cronjob.CodeInvalidArgument, "invalid claim request")
	}
	if request.Candidate.Kind != cronjob.RunScheduled && request.Candidate.Kind != cronjob.RunRetry {
		return lease, cronjob.NewError(cronjob.CodeInvalidArgument, "invalid run kind")
	}
	scope := request.Candidate.Scope
	coordinator := scope.Coordinator()
	err := s.watchCoordinator(ctx, coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
		lease = cronjob.Lease{}
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.Candidate.TaskID))
		if err != nil {
			return err
		}
		if stored == nil || stored.Task.Scope != scope {
			return cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
		}
		if stored.Task.Version != request.Candidate.TaskVersion || stored.Task.ActiveRun != nil {
			return cronjob.NewError(cronjob.CodeConflict, "candidate is stale or already claimed")
		}
		if agentCronReportBlocks(stored.Task) {
			return cronjob.NewError(cronjob.CodeConflict, "the previous report is still pending")
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		switch request.Candidate.Kind {
		case cronjob.RunScheduled:
			score, scoreErr := tx.ZScore(ctx, s.dueKey(coordinator), member).Result()
			if scoreErr != nil && !errors.Is(scoreErr, redis.Nil) {
				return scoreErr
			}
			if errors.Is(scoreErr, redis.Nil) || stored.Task.NextRunAt.After(request.Now) || !stored.Task.NextRunAt.Equal(request.Candidate.ScheduledAt) || int64(score) != stored.Task.NextRunAt.UnixMilli() {
				return cronjob.NewError(cronjob.CodeConflict, "scheduled occurrence is stale")
			}
			if stored.Task.LatestResult != nil && stored.Task.LatestResult.RetryStatus == cronjob.RetryQueued {
				stored.Task.LatestResult.RetryStatus = cronjob.RetryConsumed
				stored.RetryReadyAt = time.Time{}
				pipe.ZRem(ctx, s.retryKey(coordinator), member)
			}
			pipe.ZRem(ctx, s.dueKey(coordinator), member)
		case cronjob.RunRetry:
			score, scoreErr := tx.ZScore(ctx, s.retryKey(coordinator), member).Result()
			if scoreErr != nil && !errors.Is(scoreErr, redis.Nil) {
				return scoreErr
			}
			if errors.Is(scoreErr, redis.Nil) || stored.RetryReadyAt.After(request.Now) || !stored.Task.NextRunAt.After(request.Now) || int64(score) != stored.RetryReadyAt.UnixMilli() || stored.Task.LatestResult == nil || stored.Task.LatestResult.RetryStatus != cronjob.RetryQueued || stored.Task.LatestResult.RunID != request.Candidate.RetryOfRunID || !stored.Task.LatestResult.ScheduledAt.Equal(request.Candidate.ScheduledAt) || stored.Task.LatestResult.RetryCount+1 != request.Candidate.RetryCount {
				return cronjob.NewError(cronjob.CodeConflict, "retry candidate is stale")
			}
			stored.Task.LatestResult.RetryStatus = cronjob.RetryConsumed
			stored.RetryReadyAt = time.Time{}
			pipe.ZRem(ctx, s.retryKey(coordinator), member)
		}
		if err := s.checkAndCleanClaimCapacity(ctx, tx, pipe, coordinator, scope.ChatID, request.Now, request.MaxConcurrency); err != nil {
			return err
		}
		token, err := agentCronRandomID("lease_")
		if err != nil {
			return err
		}
		runTimeoutAt := request.Now.Add(request.RunTimeout)
		expiresAt := runTimeoutAt.Add(request.GracePeriod)
		run := &cronjob.Run{
			RunID:          request.RunID,
			Kind:           request.Candidate.Kind,
			TaskVersion:    stored.Task.Version,
			ScheduledAt:    request.Candidate.ScheduledAt,
			StartedAt:      request.Now,
			RetryOfRunID:   request.Candidate.RetryOfRunID,
			RetryCount:     request.Candidate.RetryCount,
			LeaseToken:     token,
			RunTimeoutAt:   runTimeoutAt,
			LeaseExpiresAt: expiresAt,
		}
		stored.Task.ActiveRun = run
		stored.Task.UpdatedAt = request.Now
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		leaseRecord := agentCronExecutionLease{Token: token, Scope: scope, TaskID: stored.Task.ID, TaskVersion: stored.Task.Version, RunID: request.RunID, ExpiresAt: expiresAt}
		leaseData, err := marshalAgentCron(leaseRecord)
		if err != nil {
			return err
		}
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		pipe.Set(ctx, s.leaseKey(coordinator, token), leaseData, 0)
		pipe.ZAdd(ctx, s.leasesKey(coordinator), redis.Z{Score: agentCronTimeScore(expiresAt), Member: token})
		pipe.HSet(ctx, s.slotsKey(coordinator), token, expiresAt.UnixMilli())
		pipe.HSet(ctx, s.chatLocksKey(coordinator), strconv.FormatInt(scope.ChatID, 10), token+"|"+strconv.FormatInt(expiresAt.UnixMilli(), 10))
		if request.ChatCooldown > 0 {
			pipe.HSet(ctx, s.cooldownsKey(coordinator), strconv.FormatInt(scope.ChatID, 10), request.Now.Add(request.ChatCooldown).UnixMilli())
		}
		lease = cronjob.Lease{
			Task: stored.Task, RunID: request.RunID, Token: token, Kind: request.Candidate.Kind,
			TaskVersion: stored.Task.Version, ScheduledAt: request.Candidate.ScheduledAt,
			RetryOfRunID: request.Candidate.RetryOfRunID, RetryCount: request.Candidate.RetryCount,
			RunTimeoutAt: runTimeoutAt, ExpiresAt: expiresAt,
		}
		return nil
	})
	if err != nil {
		return cronjob.Lease{}, err
	}
	return lease, err
}

func (s *agentCronStore) checkAndCleanClaimCapacity(ctx context.Context, tx *redis.Tx, pipe redis.Pipeliner, coordinator cronjob.Coordinator, chatID int64, now time.Time, maxConcurrency int) error {
	slots, err := tx.HGetAll(ctx, s.slotsKey(coordinator)).Result()
	if err != nil {
		return err
	}
	active := 0
	for token, rawExpiry := range slots {
		expiry, parseErr := strconv.ParseInt(rawExpiry, 10, 64)
		if parseErr != nil {
			return parseErr
		}
		if expiry <= now.UnixMilli() {
			pipe.HDel(ctx, s.slotsKey(coordinator), token)
			continue
		}
		active++
	}
	if active >= maxConcurrency {
		return cronjob.NewError(cronjob.CodeRateLimited, "global cron concurrency is full")
	}
	chatField := strconv.FormatInt(chatID, 10)
	lock, err := tx.HGet(ctx, s.chatLocksKey(coordinator), chatField).Result()
	if err == nil {
		parts := strings.SplitN(lock, "|", 2)
		if len(parts) != 2 || parts[0] == "" {
			return errInvalidAgentCronChatLock
		}
		expiry, parseErr := strconv.ParseInt(parts[1], 10, 64)
		if parseErr != nil {
			return parseErr
		}
		if expiry > now.UnixMilli() {
			return cronjob.NewError(cronjob.CodeRateLimited, "another cron task is active in this chat")
		}
		pipe.HDel(ctx, s.chatLocksKey(coordinator), chatField)
	} else if !errors.Is(err, redis.Nil) {
		return err
	}
	cooldown, err := tx.HGet(ctx, s.cooldownsKey(coordinator), chatField).Int64()
	if err == nil && cooldown > now.UnixMilli() {
		return cronjob.NewError(cronjob.CodeRateLimited, "chat cron cooldown is active")
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return err
	}
	if err == nil {
		pipe.HDel(ctx, s.cooldownsKey(coordinator), chatField)
	}
	return nil
}

func (s *agentCronStore) Finish(ctx context.Context, request cronjob.FinishRequest) (cronjob.Task, error) {
	var resultTask cronjob.Task
	if err := validateScope(request.Lease.Task.Scope); err != nil {
		return resultTask, err
	}
	if request.Lease.Token == "" || request.Lease.RunID == "" || request.Now.IsZero() || request.NextRunAt.IsZero() || !request.NextRunAt.After(request.Now) {
		return resultTask, cronjob.NewError(cronjob.CodeInvalidArgument, "valid lease, finish time, and future next run are required")
	}
	if request.Result.Outcome != cronjob.OutcomeSucceeded && request.Result.Outcome != cronjob.OutcomeFailed && request.Result.Outcome != cronjob.OutcomeSkipped {
		return resultTask, cronjob.NewError(cronjob.CodeInvalidArgument, "valid execution outcome is required")
	}
	scope := request.Lease.Task.Scope
	coordinator := scope.Coordinator()
	var postErr error
	err := s.watchCoordinator(ctx, coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
		resultTask = cronjob.Task{}
		postErr = nil
		leaseRecord, err := s.loadExecutionLease(ctx, tx, coordinator, request.Lease.Token)
		if err != nil {
			return err
		}
		stored, err := loadAgentCronTask(ctx, tx, s.taskKey(scope, request.Lease.Task.ID))
		if err != nil {
			return err
		}
		if leaseRecord == nil {
			if stored == nil {
				postErr = cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
			} else {
				postErr = cronjob.NewError(cronjob.CodeConflict, "execution lease is stale")
			}
			return nil
		}
		if stored == nil {
			s.releaseExecutionResources(ctx, tx, pipe, *leaseRecord)
			postErr = cronjob.NewError(cronjob.CodeNotFound, "cron task not found")
			return nil
		}
		if leaseRecord.Scope != scope || leaseRecord.TaskID != request.Lease.Task.ID || !request.Now.Before(leaseRecord.ExpiresAt) || stored.Task.ActiveRun == nil || stored.Task.ActiveRun.LeaseToken != request.Lease.Token || stored.Task.ActiveRun.RunID != request.Lease.RunID || stored.Task.Version != request.Lease.TaskVersion || leaseRecord.TaskVersion != request.Lease.TaskVersion || leaseRecord.RunID != request.Lease.RunID {
			postErr = cronjob.NewError(cronjob.CodeConflict, "execution lease is stale")
			return nil
		}

		run := stored.Task.ActiveRun
		finished := agentCronBoundResult(request.Result)
		finished.RunID = run.RunID
		finished.Kind = run.Kind
		finished.TaskVersion = run.TaskVersion
		finished.ScheduledAt = run.ScheduledAt
		finished.StartedAt = run.StartedAt
		finished.FinishedAt = request.Now
		finished.RetryOfRunID = run.RetryOfRunID
		finished.RetryCount = run.RetryCount
		if finished.Outcome == cronjob.OutcomeSucceeded {
			finished.RetryStatus = cronjob.RetryUnavailable
		} else if finished.RetryStatus == "" {
			finished.RetryStatus = cronjob.RetryAvailable
		}
		report := request.Report
		report.RunID = run.RunID
		report.TaskVersion = stored.Task.Version
		report.Attempt = 0
		report.LeaseToken = ""
		report.LeaseExpiresAt = time.Time{}
		report.LastError = agentCronTruncate(report.LastError, agentCronMaxErrorBytes)
		report.Receipt = agentCronMergeReceipt(nil, report.Receipt)
		if report.State != cronjob.DeliverySuppressed {
			report.State = cronjob.DeliveryPending
			report.NextAttemptAt = request.Now
		}
		stored.Task.ActiveRun = nil
		stored.Task.LatestResult = &finished
		stored.Task.Report = &report
		stored.Task.NextRunAt = request.NextRunAt
		stored.Task.UpdatedAt = request.Now
		stored.ManualReportRetries = 0
		stored.RetryReadyAt = time.Time{}
		data, err := marshalAgentCron(stored)
		if err != nil {
			return err
		}
		member := agentCronTaskMember(scope, stored.Task.ID)
		pipe.Set(ctx, s.taskKey(scope, stored.Task.ID), data, 0)
		pipe.ZAdd(ctx, s.dueKey(coordinator), redis.Z{Score: agentCronTimeScore(request.NextRunAt), Member: member})
		pipe.ZRem(ctx, s.retryKey(coordinator), member)
		if agentCronReportTerminal(report.State) {
			pipe.ZRem(ctx, s.reportsKey(coordinator), member)
		} else {
			pipe.ZAdd(ctx, s.reportsKey(coordinator), redis.Z{Score: agentCronTimeScore(report.NextAttemptAt), Member: member})
		}
		s.releaseExecutionResources(ctx, tx, pipe, *leaseRecord)
		resultTask = stored.Task
		return nil
	})
	if err != nil {
		return resultTask, err
	}
	if postErr != nil {
		return resultTask, postErr
	}
	return resultTask, nil
}

func (s *agentCronStore) Recover(ctx context.Context, request cronjob.RecoverRequest) (cronjob.RecoverResult, error) {
	var result cronjob.RecoverResult
	if err := validateCoordinator(request.Coordinator); err != nil {
		return result, err
	}
	if request.Now.IsZero() {
		return result, cronjob.NewError(cronjob.CodeInvalidArgument, "current time is required")
	}
	limit := request.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	tokens, err := s.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: s.leasesKey(request.Coordinator), Start: agentCronNegativeInfinity,
		Stop: strconv.FormatInt(request.Now.UnixMilli(), 10), ByScore: true, Count: int64(limit),
	}).Result()
	if err != nil {
		return result, agentCronPublicError(err)
	}
	for _, token := range tokens {
		recovered := false
		err = s.watchCoordinator(ctx, request.Coordinator, func(tx *redis.Tx, pipe redis.Pipeliner) error {
			recovered = false
			leaseRecord, loadErr := s.loadExecutionLease(ctx, tx, request.Coordinator, token)
			if loadErr != nil {
				return loadErr
			}
			if leaseRecord == nil {
				pipe.ZRem(ctx, s.leasesKey(request.Coordinator), token)
				return nil
			}
			if leaseRecord.ExpiresAt.After(request.Now) {
				return nil
			}
			stored, loadErr := loadAgentCronTask(ctx, tx, s.taskKey(leaseRecord.Scope, leaseRecord.TaskID))
			if loadErr != nil {
				return loadErr
			}
			if stored == nil {
				s.releaseExecutionResources(ctx, tx, pipe, *leaseRecord)
				return nil
			}
			if stored.Task.ActiveRun == nil || stored.Task.ActiveRun.LeaseToken != token || stored.Task.ActiveRun.RunID != leaseRecord.RunID || stored.Task.Version != leaseRecord.TaskVersion {
				s.releaseExecutionResources(ctx, tx, pipe, *leaseRecord)
				return nil
			}
			schedule, parseErr := cronjob.Parse(stored.Task.Cron, stored.Task.Timezone)
			if parseErr != nil {
				return parseErr
			}
			nextRunAt, nextErr := schedule.Next(request.Now)
			if nextErr != nil {
				return nextErr
			}
			run := stored.Task.ActiveRun
			finished := cronjob.ExecutionResult{
				RunID: run.RunID, Kind: run.Kind, TaskVersion: run.TaskVersion, ScheduledAt: run.ScheduledAt,
				StartedAt: run.StartedAt, FinishedAt: request.Now, Outcome: cronjob.OutcomeFailed,
				RetryOfRunID: run.RetryOfRunID, RetryCount: run.RetryCount, RetryStatus: cronjob.RetryAvailable,
				ErrorCode: "interrupted", ErrorMessage: "execution lease expired before completion", SideEffectsPossible: true,
			}
			report := cronjob.Report{RunID: run.RunID, TaskVersion: stored.Task.Version, State: cronjob.DeliveryPending, NextAttemptAt: request.Now}
			stored.Task.ActiveRun = nil
			stored.Task.LatestResult = &finished
			stored.Task.Report = &report
			stored.Task.NextRunAt = nextRunAt
			stored.Task.UpdatedAt = request.Now
			stored.RetryReadyAt = time.Time{}
			stored.ManualReportRetries = 0
			data, marshalErr := marshalAgentCron(stored)
			if marshalErr != nil {
				return marshalErr
			}
			member := agentCronTaskMember(stored.Task.Scope, stored.Task.ID)
			pipe.Set(ctx, s.taskKey(stored.Task.Scope, stored.Task.ID), data, 0)
			pipe.ZAdd(ctx, s.dueKey(request.Coordinator), redis.Z{Score: agentCronTimeScore(nextRunAt), Member: member})
			pipe.ZRem(ctx, s.retryKey(request.Coordinator), member)
			pipe.ZAdd(ctx, s.reportsKey(request.Coordinator), redis.Z{Score: agentCronTimeScore(request.Now), Member: member})
			s.releaseExecutionResources(ctx, tx, pipe, *leaseRecord)
			recovered = true
			return nil
		})
		if err != nil {
			return result, err
		}
		if recovered {
			result.Recovered++
		}
	}
	return result, nil
}

func (s *agentCronStore) loadExecutionLease(ctx context.Context, cmd redis.Cmdable, coordinator cronjob.Coordinator, token string) (*agentCronExecutionLease, error) {
	data, err := cmd.Get(ctx, s.leaseKey(coordinator, token)).Bytes()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var lease agentCronExecutionLease
	if err := json.Unmarshal(data, &lease); err != nil {
		return nil, err
	}
	return &lease, nil
}

func (s *agentCronStore) releaseExecutionResources(ctx context.Context, tx *redis.Tx, pipe redis.Pipeliner, lease agentCronExecutionLease) {
	coordinator := lease.Scope.Coordinator()
	pipe.ZRem(ctx, s.leasesKey(coordinator), lease.Token)
	pipe.Del(ctx, s.leaseKey(coordinator, lease.Token))
	pipe.HDel(ctx, s.slotsKey(coordinator), lease.Token)
	field := strconv.FormatInt(lease.Scope.ChatID, 10)
	lock, err := tx.HGet(ctx, s.chatLocksKey(coordinator), field).Result()
	if err == nil && strings.HasPrefix(lock, lease.Token+"|") {
		pipe.HDel(ctx, s.chatLocksKey(coordinator), field)
	}
}
