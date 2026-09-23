package orm

import (
	"context"
	"strings"
	"testing"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentCronScheduledRequest(t *testing.T, scope cronjob.Scope, now time.Time, expression, suffix string) cronjob.CreateRequest {
	t.Helper()
	schedule, err := cronjob.Resolve(expression, "UTC", now)
	require.NoError(t, err)
	next, err := schedule.Next(now)
	require.NoError(t, err)
	request := agentCronTestCreate(scope, 42, now, suffix)
	request.Cron = schedule.Expression()
	request.NextRunAt = next
	return request
}

func agentCronIndexAbsent(t *testing.T, store *agentCronStore, task cronjob.Task, index string) {
	t.Helper()
	assert.ErrorIs(t, rc.ZScore(t.Context(), index, agentCronTaskMember(task.Scope, task.ID)).Err(), redis.Nil)
}

func TestAgentCronCanonicalScheduleBoundaryAndDedup(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 987654321, time.UTC)
	scope := agentCronTestScope(-3101)
	request := agentCronScheduledRequest(t, scope, now, "@at 90s", "same")
	assert.Equal(t, now.Add(90*time.Second), request.NextRunAt)
	invalid := request
	invalid.Cron = "@at 90s"
	_, err := store.Create(t.Context(), invalid)
	assert.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	invalid = request
	invalid.NextRunAt = request.NextRunAt.Add(time.Nanosecond)
	_, err = store.Create(t.Context(), invalid)
	assert.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	assert.Empty(t, storeMustList(t, store, scope).Tasks)

	first, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	assert.Equal(t, "@at "+request.NextRunAt.UTC().Format(time.RFC3339Nano), first.Task.Cron)
	copy := request
	copy.Cron = " @at   " + request.NextRunAt.In(time.FixedZone("other", 3600)).Format(time.RFC3339Nano)
	duplicate, err := store.Create(t.Context(), copy)
	require.NoError(t, err)
	assert.True(t, duplicate.Deduplicated)
	assert.Equal(t, first.Task.ID, duplicate.Task.ID)
	other := agentCronScheduledRequest(t, scope, now.Add(time.Nanosecond), "@at 90s", "same")
	created, err := store.Create(t.Context(), other)
	require.NoError(t, err)
	assert.NotEqual(t, first.Task.ID, created.Task.ID)
	alias := agentCronScheduledRequest(t, scope, now, "@daily 10:05", "alias")
	plain := alias
	plain.Cron = "5 10 * * *"
	aliasCreated, err := store.Create(t.Context(), alias)
	require.NoError(t, err)
	plainCreated, err := store.Create(t.Context(), plain)
	require.NoError(t, err)
	assert.True(t, plainCreated.Deduplicated)
	assert.Equal(t, aliasCreated.Task.ID, plainCreated.Task.ID)
	_, err = store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@every 90s", "quota"))
	require.NoError(t, err)
	assert.Len(t, storeMustList(t, store, scope).Tasks, 4)
}

func storeMustList(t *testing.T, store cronjob.Store, scope cronjob.Scope) cronjob.Page {
	t.Helper()
	page, err := store.List(t.Context(), cronjob.ListRequest{Scope: scope})
	require.NoError(t, err)
	return page
}

func TestAgentCronOnceFinishOutcomesAndNanoDue(t *testing.T) {
	for _, outcome := range []cronjob.Outcome{cronjob.OutcomeSucceeded, cronjob.OutcomeFailed, cronjob.OutcomeSkipped} {
		t.Run(string(outcome), func(t *testing.T) {
			setupAgentV3Redis(t)
			store := NewAgentCronStore().(*agentCronStore)
			now := time.Date(2026, 9, 23, 10, 0, 0, 987654321, time.UTC)
			scope := agentCronTestScope(-3200)
			created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", string(outcome)))
			require.NoError(t, err)
			task := created.Task
			justBefore := task.NextRunAt.Add(-time.Nanosecond)
			candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: justBefore})
			require.NoError(t, err)
			assert.Empty(t, candidates)
			candidate := dueCandidateForTask(t, store, task, task.NextRunAt)
			_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: candidate, RunID: "early", Now: justBefore, RunTimeout: time.Minute, MaxConcurrency: 2})
			assert.ErrorIs(t, err, cronjob.ErrConflict)
			lease := claimAgentCronTask(t, store, candidate, "once-run", task.NextRunAt, 2, 0)
			finishAt := task.NextRunAt.Add(time.Second)
			_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishAt, NextRunAt: finishAt.Add(time.Hour), Result: cronjob.ExecutionResult{Outcome: outcome}})
			assert.ErrorIs(t, err, cronjob.ErrInvalidArgument)
			task, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishAt, Result: cronjob.ExecutionResult{Outcome: outcome}, Report: cronjob.Report{State: cronjob.DeliveryPending}})
			require.NoError(t, err)
			assert.True(t, task.NextRunAt.IsZero())
			require.NotNil(t, task.LatestResult)
			assert.Equal(t, outcome, task.LatestResult.Outcome)
			require.NotNil(t, task.Report)
			assert.Equal(t, cronjob.DeliveryPending, task.Report.State)
			agentCronIndexAbsent(t, store, task, store.dueKey(scope.Coordinator()))
			agentCronIndexAbsent(t, store, task, store.retryKey(scope.Coordinator()))
			require.NoError(t, rc.ZScore(t.Context(), store.reportsKey(scope.Coordinator()), agentCronTaskMember(scope, task.ID)).Err())
			assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
			assert.Empty(t, rc.HGetAll(t.Context(), store.chatLocksKey(scope.Coordinator())).Val())
			assert.ErrorIs(t, rc.Get(t.Context(), store.leaseKey(scope.Coordinator(), lease.Token)).Err(), redis.Nil)
			assert.Empty(t, rc.ZRange(t.Context(), store.leasesKey(scope.Coordinator()), 0, -1).Val())
			candidates, err = store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: finishAt.Add(2 * time.Hour)})
			require.NoError(t, err)
			assert.Empty(t, candidates)
			_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: candidate, RunID: "duplicate", Now: finishAt, RunTimeout: time.Minute, MaxConcurrency: 2})
			assert.ErrorIs(t, err, cronjob.ErrConflict)
		})
	}
}

func TestAgentCronOncePromptUpdateAndExplicitReschedule(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-3300)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", "edit"))
	require.NoError(t, err)
	task := created.Task
	prompt := "different prompt"
	for _, editAt := range []time.Time{now.Add(time.Second), task.NextRunAt.Add(time.Hour)} {
		task, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: scope, UserID: 42}, TaskID: task.ID, ExpectedVersion: task.Version, Now: editAt, Prompt: &prompt, NextRunAt: editAt.Add(24 * time.Hour)})
		require.NoError(t, err)
		assert.Equal(t, created.Task.NextRunAt, task.NextRunAt)
		assert.Equal(t, created.Task.Cron, task.Cron)
	}
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.UpdatedAt), "edit-run", task.UpdatedAt, 2, 0)
	finishAt := task.UpdatedAt.Add(time.Second)
	task, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishAt, Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
	require.NoError(t, err)
	_, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: scope, UserID: 42}, TaskID: task.ID, ExpectedVersion: task.Version, Now: finishAt.Add(time.Second), Prompt: &prompt})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: task.ID})
	require.NoError(t, err)
	assert.Equal(t, task.LatestResult, got.LatestResult)
	for _, expression := range []string{"@at 10m", "@every 90s", "@at 20m"} {
		editAt := finishAt.Add(time.Minute)
		priorCron := task.Cron
		priorNext := task.NextRunAt
		priorVersion := task.Version
		schedule, resolveErr := cronjob.Resolve(expression, "UTC", editAt)
		require.NoError(t, resolveErr)
		canonical := schedule.Expression()
		next, nextErr := schedule.Next(editAt)
		require.NoError(t, nextErr)
		_, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: scope, UserID: 42}, TaskID: task.ID, ExpectedVersion: task.Version, Now: editAt, Cron: &canonical, NextRunAt: next.Add(time.Nanosecond)})
		assert.ErrorIs(t, err, cronjob.ErrInvalidArgument)
		task, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: scope, UserID: 42}, TaskID: task.ID, ExpectedVersion: task.Version, Now: editAt, Cron: &canonical, NextRunAt: next})
		require.NoError(t, err)
		assert.Equal(t, canonical, task.Cron)
		assert.Equal(t, next, task.NextRunAt)
		assert.Nil(t, task.LatestResult)
		assert.Nil(t, task.Report)
		assert.NoError(t, rc.ZScore(t.Context(), store.dueKey(scope.Coordinator()), agentCronTaskMember(scope, task.ID)).Err())
		_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: cronjob.Candidate{Scope: scope, TaskID: task.ID, TaskVersion: priorVersion, Kind: cronjob.RunScheduled, ScheduledAt: priorNext}, RunID: "old-schedule", Now: editAt.Add(time.Hour), RunTimeout: time.Minute, MaxConcurrency: 2})
		assert.ErrorIs(t, err, cronjob.ErrConflict)
		oldRequest := agentCronTestCreate(scope, 42, now, "unused")
		oldRequest.Cron = priorCron
		oldRequest.NextRunAt = priorNext
		oldRequest.Now = now
		oldRequest.Prompt = task.Prompt
		if strings.HasPrefix(priorCron, "@at ") && priorNext.After(now) {
			oldTask, createErr := store.Create(t.Context(), oldRequest)
			require.NoError(t, createErr)
			assert.NotEqual(t, task.ID, oldTask.Task.ID)
		}
		finishAt = editAt
	}
}

func TestAgentCronUnclaimedOnceSurvivesRestartBeforeAndAfterDeadline(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 9, 23, 2, 0, 0, 456789123, time.UTC)
	scope := agentCronTestScope(-3350)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", "restart"))
	require.NoError(t, err)
	for _, restartAt := range []time.Time{now.Add(time.Minute), now.Add(48 * time.Hour)} {
		store = NewAgentCronStore()
		recovered, err := store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: scope.Coordinator(), Now: restartAt})
		require.NoError(t, err)
		require.Zero(t, recovered.Recovered, "an unclaimed deadline is not an interrupted execution")
		task, err := store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: created.Task.ID})
		require.NoError(t, err)
		require.Equal(t, created.Task.Cron, task.Cron)
		require.Equal(t, created.Task.Timezone, task.Timezone)
		require.Equal(t, created.Task.NextRunAt, task.NextRunAt)
		candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: restartAt})
		require.NoError(t, err)
		if restartAt.Before(task.NextRunAt) {
			require.Empty(t, candidates)
			continue
		}
		require.Len(t, candidates, 1)
		require.Equal(t, task.NextRunAt, candidates[0].ScheduledAt)
		lease := claimAgentCronTask(t, store, candidates[0], "after-restart", restartAt, 2, 0)
		finished, err := store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: restartAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
		require.NoError(t, err)
		require.True(t, finished.NextRunAt.IsZero())
		store = NewAgentCronStore()
		candidates, err = store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: restartAt.Add(24 * time.Hour)})
		require.NoError(t, err)
		require.Empty(t, candidates)
	}
}

func TestAgentCronOnceRecoverAndFencedRetry(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-3400)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", "recover"))
	require.NoError(t, err)
	task := created.Task
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "lost", task.NextRunAt, 2, 0)
	recoverAt := lease.ExpiresAt.Add(time.Second)
	result, err := store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: scope.Coordinator(), Now: recoverAt})
	require.NoError(t, err)
	assert.Equal(t, 1, result.Recovered)
	result, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: scope.Coordinator(), Now: recoverAt.Add(time.Second)})
	require.NoError(t, err)
	assert.Zero(t, result.Recovered)
	task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: task.ID})
	require.NoError(t, err)
	assert.True(t, task.NextRunAt.IsZero())
	assert.Nil(t, task.ActiveRun)
	assert.True(t, task.LatestResult.SideEffectsPossible)
	assert.Equal(t, "interrupted", task.LatestResult.ErrorCode)
	assert.Equal(t, cronjob.RetryAvailable, task.LatestResult.RetryStatus)
	assert.Equal(t, cronjob.DeliveryPending, task.Report.State)
	agentCronIndexAbsent(t, store, task, store.dueKey(scope.Coordinator()))
	assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
	assert.Empty(t, rc.HGetAll(t.Context(), store.chatLocksKey(scope.Coordinator())).Val())
	_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: recoverAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	retry := cronjob.RetryRequest{Actor: cronjob.Actor{Scope: scope, UserID: 42}, TaskID: task.ID, RunID: task.LatestResult.RunID, ExpectedVersion: task.Version, Now: recoverAt.Add(2 * time.Second), MaxRetries: 1}
	_, err = store.Retry(t.Context(), retry)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	claimReport := claimReportForTask(t, store, task, retry.Now)
	task, err = store.FinishReport(t.Context(), cronjob.ReportFinishRequest{Lease: claimReport, Now: retry.Now.Add(time.Second), Delivered: true})
	require.NoError(t, err)
	retry.Now = retry.Now.Add(2 * time.Second)
	otherScope := retry
	otherScope.Actor.Scope = agentCronTestScope(-3401)
	_, err = store.Retry(t.Context(), otherScope)
	assert.ErrorIs(t, err, cronjob.ErrNotFound)
	otherUser := retry
	otherUser.Actor.UserID++
	_, err = store.Retry(t.Context(), otherUser)
	assert.ErrorIs(t, err, cronjob.ErrForbidden)
	otherRun := retry
	otherRun.RunID = "stale"
	_, err = store.Retry(t.Context(), otherRun)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	retry.MaxRetries = 0
	_, err = store.Retry(t.Context(), retry)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	retry.MaxRetries = 1
	retried, err := store.Retry(t.Context(), retry)
	require.NoError(t, err)
	assert.Equal(t, cronjob.RetryExecution, retried.Mode)
	_, err = store.Retry(t.Context(), retry)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	candidate := dueCandidateForTask(t, store, retried.Task, retry.Now)
	assert.Equal(t, cronjob.RunRetry, candidate.Kind)
	assert.Equal(t, lease.RunID, candidate.RetryOfRunID)
	assert.Equal(t, 1, candidate.RetryCount)
	stale := candidate
	stale.TaskVersion = task.Version
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: stale, RunID: "bad", Now: retry.Now, RunTimeout: time.Minute, MaxConcurrency: 2})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	retryLease := claimAgentCronTask(t, store, candidate, "retry-run", retry.Now, 2, 0)
	finished, err := store.Finish(t.Context(), cronjob.FinishRequest{Lease: retryLease, Now: retry.Now.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
	require.NoError(t, err)
	assert.True(t, finished.NextRunAt.IsZero())
	assert.Equal(t, 1, finished.LatestResult.RetryCount)
	agentCronIndexAbsent(t, store, finished, store.dueKey(scope.Coordinator()))
	agentCronIndexAbsent(t, store, finished, store.retryKey(scope.Coordinator()))
	assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
}

func TestAgentCronEveryFixedDelayFinishRecoverAndPromptOnly(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 987654321, time.UTC)
	scope := agentCronTestScope(-3500)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@every 90s", "interval"))
	require.NoError(t, err)
	task := created.Task
	assert.Equal(t, now.Add(90*time.Second), task.NextRunAt)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "interval-run", task.NextRunAt, 2, 0)
	finishAt := task.NextRunAt.Add(12 * time.Second)
	_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishAt, NextRunAt: task.NextRunAt.Add(90 * time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed}})
	assert.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	task, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishAt, NextRunAt: finishAt.Add(90 * time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
	require.NoError(t, err)
	assert.Equal(t, finishAt.Add(90*time.Second), task.NextRunAt)
	prompt := "updated interval"
	task, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, Now: finishAt.Add(3 * time.Hour), Prompt: &prompt})
	require.NoError(t, err)
	assert.Equal(t, finishAt.Add(90*time.Second), task.NextRunAt)
	lease = claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.UpdatedAt), "interval-lost", task.UpdatedAt, 2, 0)
	recoverAt := lease.ExpiresAt.Add(time.Second)
	_, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: scope.Coordinator(), Now: recoverAt})
	require.NoError(t, err)
	task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: task.ID})
	require.NoError(t, err)
	assert.Equal(t, recoverAt.Add(90*time.Second), task.NextRunAt)
}

func TestAgentCronZeroNextDoesNotPermitInvalidOrPeriodicRetry(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-3600)
	task := createAgentCronTask(t, store, scope, 42, now, "corrupt")
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "periodic-fail", task.NextRunAt, 2, 0)
	task = finishAgentCronTask(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliverySuppressed)
	retry, err := store.Retry(t.Context(), cronjob.RetryRequest{Actor: cronjob.Actor{Scope: scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: task.UpdatedAt.Add(time.Second), MaxRetries: 2})
	require.NoError(t, err)
	stored, err := loadAgentCronTask(t.Context(), rc, store.taskKey(scope, task.ID))
	require.NoError(t, err)
	stored.Task.NextRunAt = time.Time{}
	data, err := marshalAgentCron(stored)
	require.NoError(t, err)
	require.NoError(t, rc.Set(t.Context(), store.taskKey(scope, task.ID), data, 0).Err())
	candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: retry.Task.UpdatedAt})
	require.NoError(t, err)
	assert.Empty(t, candidates)
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: cronjob.Candidate{Scope: scope, TaskID: task.ID, TaskVersion: retry.Task.Version, Kind: cronjob.RunRetry, ScheduledAt: task.LatestResult.ScheduledAt, RetryOfRunID: task.LatestResult.RunID, RetryCount: 1}, RunID: "bad", Now: retry.Task.UpdatedAt, RunTimeout: time.Minute, MaxConcurrency: 2})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	stored.Task.Cron = "not a schedule"
	data, err = marshalAgentCron(stored)
	require.NoError(t, err)
	require.NoError(t, rc.Set(t.Context(), store.taskKey(scope, task.ID), data, 0).Err())
	candidates, err = store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: retry.Task.UpdatedAt})
	require.NoError(t, err)
	assert.Empty(t, candidates)
}

func TestAgentCronOnceReportOnlyAndRetainedQuota(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-3700)
	request := agentCronScheduledRequest(t, scope, now, "@at 90s", "report")
	request.MaxTasksPerChat = 1
	created, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, created.Task, created.Task.NextRunAt), "report-once", created.Task.NextRunAt, 2, 0)
	finishedAt := created.Task.NextRunAt.Add(time.Second)
	task, err := store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishedAt, Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded, Text: "once"}, Report: cronjob.Report{State: cronjob.DeliveryPending}})
	require.NoError(t, err)
	assert.True(t, task.NextRunAt.IsZero())
	quota := agentCronScheduledRequest(t, scope, now, "@at 3m", "other")
	quota.MaxTasksPerChat = 1
	_, err = store.Create(t.Context(), quota)
	assert.ErrorIs(t, err, cronjob.ErrQuotaExceeded)
	duplicate, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, duplicate.Deduplicated)
	assert.True(t, duplicate.Task.NextRunAt.IsZero())

	claimAt := finishedAt
	for attempt := range 3 {
		reportLease := claimReportForTask(t, store, task, claimAt)
		var receipt *cronjob.DeliveryReceipt
		if attempt == 0 {
			receipt = &cronjob.DeliveryReceipt{MessageIDs: []int{100}}
		}
		task = failReport(t, store, reportLease, claimAt.Add(time.Second), receipt)
		assert.True(t, task.NextRunAt.IsZero())
		if attempt < 2 {
			claimAt = task.Report.NextAttemptAt
		}
	}
	assert.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
	assert.Equal(t, []int{100}, task.Report.Receipt.MessageIDs)
	beforeResult := *task.LatestResult
	retry := cronjob.RetryRequest{Actor: cronjob.Actor{Scope: scope, UserID: task.CreatorID}, TaskID: task.ID, RunID: task.LatestResult.RunID, ExpectedVersion: task.Version, Now: claimAt.Add(time.Minute), MaxRetries: 1}
	retried, err := store.Retry(t.Context(), retry)
	require.NoError(t, err)
	assert.Equal(t, cronjob.RetryReportOnly, retried.Mode)
	assert.True(t, retried.Task.NextRunAt.IsZero())
	assert.Equal(t, beforeResult, *retried.Task.LatestResult)
	assert.Equal(t, []int{100}, retried.Task.Report.Receipt.MessageIDs)
	agentCronIndexAbsent(t, store, task, store.dueKey(scope.Coordinator()))
	_, err = store.Retry(t.Context(), retry)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	reportLease := claimReportForTask(t, store, retried.Task, retry.Now)
	task, err = store.FinishReport(t.Context(), cronjob.ReportFinishRequest{Lease: reportLease, Now: retry.Now.Add(time.Second), Delivered: true, Receipt: &cronjob.DeliveryReceipt{MessageIDs: []int{101}}})
	require.NoError(t, err)
	assert.True(t, task.NextRunAt.IsZero())
	assert.Equal(t, []int{100, 101}, task.Report.Receipt.MessageIDs)
	assert.Equal(t, beforeResult, *task.LatestResult)
	_, err = store.Retry(t.Context(), cronjob.RetryRequest{Actor: retry.Actor, TaskID: task.ID, RunID: retry.RunID, ExpectedVersion: task.Version, Now: task.LatestResult.FinishedAt.Add(agentCronReportLifetime), MaxRetries: 1})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronEveryRetryFinishReschedulesFromCompletion(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 875123456, time.UTC)
	scope := agentCronTestScope(-3800)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@every 90s", "retry"))
	require.NoError(t, err)
	task := created.Task
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "interval-fail", task.NextRunAt, 2, 0)
	finishedAt := task.NextRunAt.Add(time.Second)
	task, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: finishedAt, NextRunAt: finishedAt.Add(90 * time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
	require.NoError(t, err)
	retryAt := finishedAt.Add(10 * time.Second)
	retried, err := store.Retry(t.Context(), cronjob.RetryRequest{Actor: cronjob.Actor{Scope: scope, UserID: task.CreatorID}, TaskID: task.ID, RunID: task.LatestResult.RunID, ExpectedVersion: task.Version, Now: retryAt, MaxRetries: 1})
	require.NoError(t, err)
	assert.Equal(t, cronjob.RunRetry, dueCandidateForTask(t, store, retried.Task, retryAt).Kind)
	retryLease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, retried.Task, retryAt), "interval-retry", retryAt, 2, 0)
	retryFinishedAt := retryAt.Add(2 * time.Second)
	task, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: retryLease, Now: retryFinishedAt, NextRunAt: retryFinishedAt.Add(90 * time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}, Report: cronjob.Report{State: cronjob.DeliverySuppressed}})
	require.NoError(t, err)
	assert.Equal(t, retryFinishedAt.Add(90*time.Second), task.NextRunAt)
	assert.Equal(t, 1, task.LatestResult.RetryCount)
	agentCronIndexAbsent(t, store, task, store.retryKey(scope.Coordinator()))
	assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
}

func TestAgentCronOnceDeleteAndStaleCandidateDoNotResurrect(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-3900)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", "deleted"))
	require.NoError(t, err)
	task := created.Task
	candidate := dueCandidateForTask(t, store, task, task.NextRunAt)
	wrongScope := candidate
	wrongScope.Scope = agentCronTestScope(-3901)
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: wrongScope, RunID: "wrong", Now: task.NextRunAt, RunTimeout: time.Minute, MaxConcurrency: 2})
	assert.ErrorIs(t, err, cronjob.ErrNotFound)
	lease := claimAgentCronTask(t, store, candidate, "delete-run", task.NextRunAt, 2, 0)
	require.NoError(t, store.Delete(t.Context(), cronjob.DeleteRequest{Actor: cronjob.Actor{Scope: scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version}))
	_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: task.NextRunAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}})
	assert.ErrorIs(t, err, cronjob.ErrNotFound)
	_, err = store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: task.ID})
	assert.ErrorIs(t, err, cronjob.ErrNotFound)
	agentCronIndexAbsent(t, store, task, store.dueKey(scope.Coordinator()))
	assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
	assert.Empty(t, rc.HGetAll(t.Context(), store.chatLocksKey(scope.Coordinator())).Val())
}

func TestAgentCronFinishCanceledTransactionCanRetrySameResult(t *testing.T) {
	mr := setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-4000)
	created, err := store.Create(t.Context(), agentCronScheduledRequest(t, scope, now, "@at 90s", "cancelled"))
	require.NoError(t, err)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, created.Task, created.Task.NextRunAt), "cancelled-run", created.Task.NextRunAt, 2, 0)
	request := cronjob.FinishRequest{Lease: lease, Now: created.Task.NextRunAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeFailed, Text: "saved once"}, Report: cronjob.Report{State: cronjob.DeliveryPending}}
	canceled, cancel := context.WithCancel(t.Context())
	cancel()
	_, err = store.Finish(canceled, request)
	assert.ErrorIs(t, err, context.Canceled)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: created.Task.ID})
	require.NoError(t, err)
	assert.Nil(t, got.LatestResult)
	assert.NotNil(t, got.ActiveRun)
	assert.NoError(t, rc.ZScore(t.Context(), store.leasesKey(scope.Coordinator()), lease.Token).Err())
	mr.SetError("temporary storage failure")
	_, err = store.Finish(t.Context(), request)
	assert.ErrorIs(t, err, cronjob.ErrUnavailable)
	mr.SetError("")
	got, err = store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: created.Task.ID})
	require.NoError(t, err)
	assert.Nil(t, got.LatestResult)
	assert.NotNil(t, got.ActiveRun)
	finished, err := store.Finish(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, finished.NextRunAt.IsZero())
	assert.Equal(t, "saved once", finished.LatestResult.Text)
	agentCronIndexAbsent(t, store, finished, store.dueKey(scope.Coordinator()))
	assert.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(scope.Coordinator())).Val())
}
