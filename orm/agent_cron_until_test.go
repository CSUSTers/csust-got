package orm

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"testing"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/require"
)

func createAgentCronUntil(t *testing.T, store cronjob.Store, expression string, now time.Time, chatID int64) cronjob.Task {
	t.Helper()
	schedule, err := cronjob.Resolve(expression, "UTC", now)
	require.NoError(t, err)
	next, err := schedule.Next(now)
	require.NoError(t, err)
	request := agentCronTestCreate(agentCronTestScope(chatID), 11, now, expression)
	request.Cron, request.NextRunAt, request.Until = schedule.Expression(), next, next
	request.MaxTasksPerChat = 1
	created, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	return created.Task
}

func finishAgentCronUntil(t *testing.T, store cronjob.Store, lease cronjob.Lease, now time.Time, outcome cronjob.Outcome, state cronjob.DeliveryState) cronjob.Task {
	t.Helper()
	schedule, err := cronjob.Parse(lease.Task.Cron, lease.Task.Timezone)
	require.NoError(t, err)
	next, _, err := schedule.AfterRunUntil(now, lease.Task.Until)
	require.NoError(t, err)
	task, err := store.Finish(t.Context(), cronjob.FinishRequest{
		Lease: lease, Now: now, NextRunAt: next,
		Result: cronjob.ExecutionResult{Outcome: outcome, Text: "final output", Format: "plain"},
		Report: cronjob.Report{State: state},
	})
	require.NoError(t, err)
	return task
}

func failAgentCronUntilReport(t *testing.T, store cronjob.Store, task cronjob.Task) cronjob.Task {
	t.Helper()
	for range 3 {
		now := task.Report.NextAttemptAt
		task = failReport(t, store, claimReportForTask(t, store, task, now), now.Add(time.Second), &cronjob.DeliveryReceipt{MessageIDs: []int{101}})
	}
	require.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
	return task
}

func requireAgentCronMissing(t *testing.T, store cronjob.Store, task cronjob.Task) {
	t.Helper()
	_, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.ErrorIs(t, err, cronjob.ErrNotFound)
}

func TestAgentCronUntilCreateDedupAndLegacyRecord(t *testing.T) {
	mr := setupAgentV3Redis(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	stores := []cronjob.Store{NewAgentCronStore(), &agentCronStore{client: client}}
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	request := agentCronTestCreate(agentCronTestScope(-3000), 11, now, "same")
	request.Until = request.NextRunAt.Add(time.Nanosecond)
	request.MaxTasksPerChat = 2
	var wg sync.WaitGroup
	results := make(chan cronjob.CreateResult, 2)
	errs := make(chan error, 2)
	start := make(chan struct{})
	for i, store := range stores {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			input := request
			if i == 1 {
				input.Until = request.Until.In(time.FixedZone("offset", 8*3600))
			}
			result, err := store.Create(t.Context(), input)
			results <- result
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	for range 2 {
		require.NoError(t, <-errs)
	}
	a, b := <-results, <-results
	require.Equal(t, a.Task.ID, b.Task.ID)
	require.NotEqual(t, a.Deduplicated, b.Deduplicated)
	require.True(t, a.Task.Until.Equal(request.Until))
	bad := request
	bad.Until = bad.NextRunAt.Add(-time.Nanosecond)
	_, err := stores[0].Create(t.Context(), bad)
	require.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	bad.Until = now
	_, err = stores[0].Create(t.Context(), bad)
	require.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	request.Until = time.Time{}
	legacy, err := stores[0].Create(t.Context(), request)
	require.NoError(t, err)
	require.NotEqual(t, a.Task.ID, legacy.Task.ID)
	concrete := stores[0].(*agentCronStore)
	stored, err := loadAgentCronTask(t.Context(), rc, concrete.taskKey(request.Actor.Scope, legacy.Task.ID))
	require.NoError(t, err)
	// Old records have no Until field and use this unchanged dedup payload.
	sum := sha256.Sum256([]byte(fmt.Sprintf("%d\x00%s\x00%s\x00%s", legacy.Task.CreatorID, legacy.Task.Cron, legacy.Task.Timezone, legacy.Task.Prompt)))
	require.Equal(t, hex.EncodeToString(sum[:]), stored.DedupKey)
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	var record map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(data, &record))
	var fields map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(record["task"], &fields))
	delete(fields, "Until")
	record["task"], err = json.Marshal(fields)
	require.NoError(t, err)
	data, err = json.Marshal(record)
	require.NoError(t, err)
	require.NoError(t, rc.Set(t.Context(), concrete.taskKey(request.Actor.Scope, legacy.Task.ID), data, 0).Err())
	duplicate, err := stores[1].Create(t.Context(), request)
	require.NoError(t, err)
	require.True(t, duplicate.Deduplicated)
	require.Equal(t, legacy.Task.ID, duplicate.Task.ID)
	require.True(t, duplicate.Task.Until.IsZero())
	request.Until = a.Task.Until.Add(time.Second)
	_, err = stores[0].Create(t.Context(), request)
	require.ErrorIs(t, err, cronjob.ErrQuotaExceeded)
}

func TestAgentCronUntilUpdatePreservesOccurrenceAndMigratesDedup(t *testing.T) {
	mr := setupAgentV3Redis(t)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	store := NewAgentCronStore()
	other := &agentCronStore{client: client}
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-3001), 11, now, "update")
	actor := cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}
	until := task.NextRunAt
	task, err := store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: task.ID, ExpectedVersion: task.Version, Until: &until, Now: now.Add(time.Second)})
	require.NoError(t, err)
	require.True(t, until.Equal(task.Until))
	originalNext := task.NextRunAt
	prompt := "new prompt"
	// Even an overdue saved occurrence is preserved, and a past retained cutoff is not cleared.
	task, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: task.ID, ExpectedVersion: task.Version, Prompt: &prompt, Now: until.Add(time.Hour)})
	require.NoError(t, err)
	require.Equal(t, originalNext, task.NextRunAt)
	require.True(t, task.Until.Equal(until))
	cron := "@every 1h"
	_, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: task.ID, ExpectedVersion: task.Version, Cron: &cron, NextRunAt: until.Add(2 * time.Hour), Now: until.Add(time.Hour)})
	require.ErrorIs(t, err, cronjob.ErrInvalidArgument)
	noCutoff := time.Time{}
	task, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: task.ID, ExpectedVersion: task.Version, Cron: &cron, Until: &noCutoff, NextRunAt: until.Add(2 * time.Hour), Now: until.Add(time.Hour)})
	require.NoError(t, err)
	require.True(t, task.Until.IsZero())
	require.Equal(t, until.Add(2*time.Hour), task.NextRunAt)
	newUntil := task.NextRunAt.Add(time.Hour)
	var wg sync.WaitGroup
	errs := make(chan error, 2)
	for _, contender := range []cronjob.Store{store, other} {
		wg.Go(func() {
			_, updateErr := contender.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: task.ID, ExpectedVersion: task.Version, Until: &newUntil, Now: until.Add(time.Hour)})
			errs <- updateErr
		})
	}
	wg.Wait()
	a, b := <-errs, <-errs
	if a == nil {
		require.ErrorIs(t, b, cronjob.ErrConflict)
	} else {
		require.ErrorIs(t, a, cronjob.ErrConflict)
		require.NoError(t, b)
	}
	// The original dedup entry was released by the update.
	request := agentCronTestCreate(task.Scope, task.CreatorID, now, "update")
	created, err := other.Create(t.Context(), request)
	require.NoError(t, err)
	require.False(t, created.Deduplicated)
	conflictUntil := request.NextRunAt.Add(time.Minute)
	bounded := request
	bounded.Until = conflictUntil
	duplicate, err := other.Create(t.Context(), bounded)
	require.NoError(t, err)
	_, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: created.Task.ID, ExpectedVersion: created.Task.Version, Until: &conflictUntil, Now: now.Add(time.Second)})
	require.ErrorIs(t, err, cronjob.ErrConflict)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: created.Task.ID})
	require.NoError(t, err)
	require.True(t, got.Until.IsZero())
	require.NotEqual(t, got.ID, duplicate.Task.ID)
	actor.UserID++
	_, err = store.Update(t.Context(), cronjob.UpdateRequest{Actor: actor, TaskID: got.ID, ExpectedVersion: got.Version, Until: &conflictUntil, Now: now.Add(time.Second)})
	require.ErrorIs(t, err, cronjob.ErrForbidden)
}

func TestAgentCronUntilFinalLifecycleAndQuota(t *testing.T) {
	for _, expression := range []string{"* * * * *", "@every 90s", "@at 90s"} {
		for _, outcome := range []cronjob.Outcome{cronjob.OutcomeSucceeded, cronjob.OutcomeFailed, cronjob.OutcomeSkipped} {
			t.Run(expression+string(outcome), func(t *testing.T) {
				setupAgentV3Redis(t)
				store := NewAgentCronStore()
				now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
				task := createAgentCronUntil(t, store, expression, now, -3010)
				// Downtime beyond until still permits the saved ScheduledAt == until.
				claimAt := task.Until.Add(24 * time.Hour)
				candidate := dueCandidateForTask(t, store, task, claimAt)
				lease := claimAgentCronTask(t, store, candidate, "final", claimAt, 1, 0)
				_, err := store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: claimAt.Add(time.Second), NextRunAt: claimAt.Add(time.Hour), Result: cronjob.ExecutionResult{Outcome: outcome}})
				require.ErrorIs(t, err, cronjob.ErrInvalidArgument)
				task = finishAgentCronUntil(t, store, lease, claimAt.Add(time.Second), outcome, cronjob.DeliveryPending)
				require.True(t, task.NextRunAt.IsZero())
				require.Equal(t, candidate.ScheduledAt, task.LatestResult.ScheduledAt)
				require.Equal(t, outcome, task.LatestResult.Outcome)
				got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
				require.NoError(t, err)
				require.Equal(t, "final output", got.LatestResult.Text)
				request := agentCronTestCreate(task.Scope, task.CreatorID, now, "replacement")
				request.MaxTasksPerChat = 1
				_, err = store.Create(t.Context(), request)
				require.ErrorIs(t, err, cronjob.ErrQuotaExceeded)
				due, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: claimAt.Add(time.Hour)})
				require.NoError(t, err)
				require.Empty(t, due)
				reportLease := claimReportForTask(t, store, task, task.UpdatedAt)
				_, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
				require.NoError(t, err)
				finish := cronjob.ReportFinishRequest{Lease: reportLease, Now: task.UpdatedAt.Add(time.Second), Delivered: outcome != cronjob.OutcomeSkipped, Suppressed: outcome == cronjob.OutcomeSkipped}
				_, err = store.FinishReport(t.Context(), finish)
				require.NoError(t, err)
				requireAgentCronMissing(t, store, task)
				_, err = store.FinishReport(t.Context(), finish)
				require.ErrorIs(t, err, cronjob.ErrNotFound)
				_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: task.UpdatedAt.Add(2 * time.Second), Result: cronjob.ExecutionResult{Outcome: outcome}})
				require.ErrorIs(t, err, cronjob.ErrNotFound)
				_, err = store.Create(t.Context(), request)
				require.NoError(t, err)
			})
		}
	}
}

func TestAgentCronUntilNextValidationRecoveryAndNanoseconds(t *testing.T) {
	for _, expression := range []string{"* * * * *", "@every 90s", "@at 90s"} {
		t.Run(expression, func(t *testing.T) {
			setupAgentV3Redis(t)
			store := NewAgentCronStore()
			now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			task := createAgentCronUntil(t, store, expression, now, -3011)
			lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "interrupted", task.NextRunAt, 1, 0)
			recovered, err := store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: lease.ExpiresAt})
			require.NoError(t, err)
			require.Equal(t, 1, recovered.Recovered)
			task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			require.NoError(t, err)
			require.True(t, task.NextRunAt.IsZero())
			require.Equal(t, "interrupted", task.LatestResult.ErrorCode)
			require.Equal(t, lease.ScheduledAt, task.LatestResult.ScheduledAt)
			require.Equal(t, cronjob.DeliveryPending, task.Report.State)
			_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: lease.ExpiresAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}})
			require.ErrorIs(t, err, cronjob.ErrConflict)
		})
	}
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronUntil(t, store, "@every 90s", now, -3012)
	until := task.NextRunAt.Add(5 * time.Minute)
	task, err := store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, Until: &until, Now: now.Add(time.Second)})
	require.NoError(t, err)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "not-final", task.NextRunAt, 1, 0)
	_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: task.NextRunAt.Add(5 * time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}})
	require.ErrorIs(t, err, cronjob.ErrInvalidArgument, "zero cannot suppress a valid future occurrence")
	task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(5*time.Second), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)
	require.Equal(t, task.LatestResult.FinishedAt.Add(90*time.Second), task.NextRunAt)
	candidate := cronjob.Candidate{Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version, Kind: cronjob.RunScheduled, ScheduledAt: task.NextRunAt}
	stored, err := loadAgentCronTask(t.Context(), rc, store.taskKey(task.Scope, task.ID))
	require.NoError(t, err)
	stored.Task.Until = task.NextRunAt.Add(-time.Nanosecond)
	data, err := json.Marshal(stored)
	require.NoError(t, err)
	require.NoError(t, rc.Set(t.Context(), store.taskKey(task.Scope, task.ID), data, 0).Err())
	due, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: task.NextRunAt})
	require.NoError(t, err)
	require.Empty(t, due)
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: candidate, RunID: "forged", Now: task.NextRunAt, RunTimeout: time.Minute, MaxConcurrency: 1})
	require.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronUntilRetainedReportIsReachableAfterRestart(t *testing.T) {
	for _, mode := range []string{"exhausted", "pending", "claimed", "report-only"} {
		t.Run(mode, func(t *testing.T) {
			setupAgentV3Redis(t)
			store := NewAgentCronStore()
			now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			task := createAgentCronUntil(t, store, "@at 90s", now, -3020)
			lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "retained", task.NextRunAt, 1, 0)
			task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
			deadline := task.LatestResult.FinishedAt.Add(24 * time.Hour)
			var reportLease cronjob.ReportLease
			if mode == "exhausted" || mode == "report-only" {
				task = failAgentCronUntilReport(t, store, task)
				noCutoff := time.Time{}
				_, err := store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, Until: &noCutoff, Now: task.UpdatedAt})
				require.ErrorIs(t, err, cronjob.ErrConflict)
			}
			if mode == "report-only" {
				retried, err := store.Retry(t.Context(), cronjob.RetryRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: deadline.Add(-time.Minute), MaxRetries: 2})
				require.NoError(t, err)
				require.Equal(t, cronjob.RetryReportOnly, retried.Mode)
				require.Equal(t, task.LatestResult.FinishedAt, retried.Task.LatestResult.FinishedAt)
				require.Equal(t, []int{101}, retried.Task.Report.Receipt.MessageIDs)
				task = retried.Task
				task = failReport(t, store, claimReportForTask(t, store, task, task.Report.NextAttemptAt), task.Report.NextAttemptAt.Add(time.Second), nil)
			}
			if mode == "claimed" {
				reportLease = claimReportForTask(t, store, task, deadline.Add(-time.Second))
			}
			restarted := NewAgentCronStore()
			_, err := restarted.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline.Add(-time.Nanosecond)})
			require.NoError(t, err)
			_, err = restarted.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			require.NoError(t, err)
			_, err = restarted.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
			require.NoError(t, err)
			if mode == "claimed" {
				_, err = restarted.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
				require.NoError(t, err, "an active report lease must survive the window")
				_, err = restarted.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: reportLease.ExpiresAt})
				require.NoError(t, err)
				_, err = restarted.FinishReport(t.Context(), cronjob.ReportFinishRequest{Lease: reportLease, Now: reportLease.ExpiresAt.Add(time.Second), Delivered: true})
				require.ErrorIs(t, err, cronjob.ErrNotFound)
			}
			requireAgentCronMissing(t, restarted, task)
			request := agentCronTestCreate(task.Scope, task.CreatorID, now, "reuse-quota")
			request.MaxTasksPerChat = 1
			_, err = restarted.Create(t.Context(), request)
			require.NoError(t, err)
		})
	}
}

func TestAgentCronUntilExecutionRetryExpiryAndActiveLease(t *testing.T) {
	for _, mode := range []string{"queued", "active-finish", "active-recover", "expired-claim", "expired-retry"} {
		t.Run(mode, func(t *testing.T) {
			setupAgentV3Redis(t)
			store := NewAgentCronStore()
			now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			task := createAgentCronUntil(t, store, "* * * * *", now, -3030)
			lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "failed", task.NextRunAt, 1, 0)
			task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliveryPending)
			task = failAgentCronUntilReport(t, store, task)
			deadline := task.LatestResult.FinishedAt.Add(24 * time.Hour)
			request := cronjob.RetryRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: deadline.Add(-time.Second), MaxRetries: 2}
			if mode == "expired-retry" {
				request.Now = deadline
				_, err := store.Retry(t.Context(), request)
				require.ErrorIs(t, err, cronjob.ErrConflict)
				requireAgentCronMissing(t, store, task)
				return
			}
			retried, err := store.Retry(t.Context(), request)
			require.NoError(t, err)
			require.Equal(t, cronjob.RetryExecution, retried.Mode)
			task = retried.Task
			candidate := dueCandidateForTask(t, store, task, request.Now)
			require.Equal(t, lease.ScheduledAt, candidate.ScheduledAt)
			require.Equal(t, 1, candidate.RetryCount)
			if mode == "expired-claim" {
				_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: candidate, RunID: "expired", Now: deadline, RunTimeout: time.Minute, MaxConcurrency: 1})
				require.ErrorIs(t, err, cronjob.ErrConflict)
				requireAgentCronMissing(t, store, task)
				return
			}
			if mode == "queued" {
				_, err = NewAgentCronStore().Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
				require.NoError(t, err)
				requireAgentCronMissing(t, store, task)
				return
			}
			retryLease := claimAgentCronTask(t, store, candidate, "retry-active", request.Now, 1, 0)
			_, err = NewAgentCronStore().Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
			require.NoError(t, err)
			_, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			require.NoError(t, err, "active retry must not be removed by old report cleanup")
			if mode == "active-finish" {
				task = finishAgentCronUntil(t, store, retryLease, deadline.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliveryPending)
			} else {
				_, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: retryLease.ExpiresAt})
				require.NoError(t, err)
				task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
				require.NoError(t, err)
			}
			require.Equal(t, 1, task.LatestResult.RetryCount)
			require.Equal(t, lease.ScheduledAt, task.LatestResult.ScheduledAt)
			require.True(t, task.LatestResult.FinishedAt.After(deadline))
			require.Equal(t, cronjob.DeliveryPending, task.Report.State)
			_, err = store.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: task.LatestResult.FinishedAt.Add(24 * time.Hour)})
			require.NoError(t, err)
			requireAgentCronMissing(t, store, task)
		})
	}
}

func TestAgentCronUntilCleanupRechecksConcurrentActiveRetry(t *testing.T) {
	mr := setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronUntil(t, store, "* * * * *", now, -3040)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "original", task.NextRunAt, 1, 0)
	task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliveryPending)
	task = failAgentCronUntilReport(t, store, task)
	deadline := task.LatestResult.FinishedAt.Add(24 * time.Hour)
	client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = client.Close() })
	hook := &agentCronBlockAfterCommandHook{command: "get", key: store.taskKey(task.Scope, task.ID), read: make(chan struct{}), release: make(chan struct{})}
	client.AddHook(hook)
	cleaner := &agentCronStore{client: client}
	done := make(chan error, 1)
	go func() {
		_, err := cleaner.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
		done <- err
	}()
	select {
	case <-hook.read:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "cleanup did not reach its task read")
	}
	retried, err := store.Retry(t.Context(), cronjob.RetryRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, MaxRetries: 2, Now: deadline.Add(-time.Second)})
	require.NoError(t, err)
	retryLease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, retried.Task, deadline.Add(-time.Second)), "concurrent-retry", deadline.Add(-time.Second), 1, 0)
	close(hook.release)
	require.NoError(t, <-done)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, retryLease.Token, got.ActiveRun.LeaseToken)
	finishAgentCronUntil(t, store, retryLease, deadline.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
}

func TestAgentCronUntilConcurrentFinishAndLeaseRecovery(t *testing.T) {
	for _, reportRace := range []bool{false, true} {
		t.Run(fmt.Sprintf("report=%t", reportRace), func(t *testing.T) {
			mr := setupAgentV3Redis(t)
			store := NewAgentCronStore().(*agentCronStore)
			client := redis.NewClient(&redis.Options{Addr: mr.Addr()})
			t.Cleanup(func() { _ = client.Close() })
			other := &agentCronStore{client: client}
			now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
			task := createAgentCronUntil(t, store, "@every 90s", now, -3041)
			lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "race", task.NextRunAt, 1, 0)
			var reportLease cronjob.ReportLease
			if reportRace {
				task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
				reportLease = claimReportForTask(t, store, task, task.UpdatedAt)
			}
			start := make(chan struct{})
			finished := make(chan error, 1)
			recovered := make(chan error, 1)
			go func() {
				<-start
				var err error
				if reportRace {
					_, err = store.FinishReport(t.Context(), cronjob.ReportFinishRequest{Lease: reportLease, Now: reportLease.ExpiresAt.Add(-time.Nanosecond), Delivered: true})
				} else {
					_, err = store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: lease.ExpiresAt.Add(-time.Nanosecond), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}, Report: cronjob.Report{State: cronjob.DeliveryPending}})
				}
				finished <- err
			}()
			go func() {
				<-start
				var err error
				if reportRace {
					_, err = other.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: reportLease.ExpiresAt})
				} else {
					_, err = other.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: lease.ExpiresAt})
				}
				recovered <- err
			}()
			close(start)
			finishErr := <-finished
			require.NoError(t, <-recovered)
			if reportRace && finishErr == nil {
				requireAgentCronMissing(t, store, task)
				return
			}
			require.True(t, finishErr == nil || errors.Is(finishErr, cronjob.ErrConflict))
			got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
			last := got.LatestResult
			require.NoError(t, err)
			require.Nil(t, got.ActiveRun)
			require.True(t, got.NextRunAt.IsZero())
			require.Equal(t, cronjob.DeliveryPending, got.Report.State)
			if !reportRace && finishErr != nil {
				require.Equal(t, "interrupted", last.ErrorCode)
			}
			require.Empty(t, rc.HGetAll(t.Context(), store.slotsKey(task.Scope.Coordinator())).Val())
		})
	}
}

func TestAgentCronUntilDeletePreservesInFlightResourcesAndOtherTaskLock(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronUntil(t, store, "@at 90s", now, -3042)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "delete", task.NextRunAt, 1, 0)
	require.NoError(t, store.Delete(t.Context(), cronjob.DeleteRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version}))
	// Manual deletion does not revoke the execution slot or chat lock while external effects may be in flight.
	require.True(t, rc.HExists(t.Context(), store.slotsKey(task.Scope.Coordinator()), lease.Token).Val())
	require.Contains(t, rc.HGet(t.Context(), store.chatLocksKey(task.Scope.Coordinator()), strconv.FormatInt(task.Scope.ChatID, 10)).Val(), lease.Token)
	replacement := createAgentCronUntil(t, store, "@at 90s", now, task.Scope.ChatID)
	newLease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, replacement, lease.ExpiresAt), "replacement", lease.ExpiresAt, 1, 0)
	_, err := store.Finish(t.Context(), cronjob.FinishRequest{Lease: lease, Now: lease.ExpiresAt.Add(time.Second), Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded}})
	require.ErrorIs(t, err, cronjob.ErrNotFound)
	require.True(t, rc.HExists(t.Context(), store.slotsKey(task.Scope.Coordinator()), newLease.Token).Val())
	require.Contains(t, rc.HGet(t.Context(), store.chatLocksKey(task.Scope.Coordinator()), strconv.FormatInt(task.Scope.ChatID, 10)).Val(), newLease.Token)
	finishAgentCronUntil(t, store, newLease, newLease.Task.ActiveRun.StartedAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)
	requireAgentCronMissing(t, store, replacement)
}

func TestAgentCronUntilDeletionStorageFailureIsRecoverable(t *testing.T) {
	mr := setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronUntil(t, store, "@at 90s", now, -3043)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "outbox", task.NextRunAt, 1, 0)
	task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
	reportLease := claimReportForTask(t, store, task, task.UpdatedAt)
	request := cronjob.ReportFinishRequest{Lease: reportLease, Now: task.UpdatedAt.Add(time.Second), Delivered: true, Receipt: &cronjob.DeliveryReceipt{MessageIDs: []int{101, 102}}}
	mr.SetError("temporary storage failure")
	_, err := store.FinishReport(t.Context(), request)
	require.ErrorIs(t, err, cronjob.ErrUnavailable)
	mr.SetError("")
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, cronjob.DeliveryClaimed, got.Report.State)
	require.Equal(t, "final output", got.LatestResult.Text)
	_, err = store.FinishReport(t.Context(), request)
	require.NoError(t, err)
	requireAgentCronMissing(t, store, task)
	// Terminal cleanup also remains reachable after a failed Reports poll.
	task = createAgentCronUntil(t, store, "@at 90s", now, -3043)
	lease = claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "cleanup", task.NextRunAt, 1, 0)
	task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
	task = failAgentCronUntilReport(t, store, task)
	deadline := task.LatestResult.FinishedAt.Add(24 * time.Hour)
	mr.SetError("temporary storage failure")
	_, err = NewAgentCronStore().Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
	require.ErrorIs(t, err, cronjob.ErrUnavailable)
	mr.SetError("")
	_, err = NewAgentCronStore().Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: deadline})
	require.NoError(t, err)
	requireAgentCronMissing(t, store, task)
}

func TestAgentCronUntilSuppressedFinishAndBoundedRecovery(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	task := createAgentCronUntil(t, store, "@at 90s", now, -3050)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "suppressed", task.NextRunAt, 1, 0)
	finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSkipped, cronjob.DeliverySuppressed)
	requireAgentCronMissing(t, store, task)
	task = createAgentCronUntil(t, store, "@every 90s", now, -3050)
	// A recovered interval is based on recovery time, and the inclusive cutoff can be its exact next instant.
	recoverAt := task.NextRunAt.Add(2 * time.Minute)
	until := recoverAt.Add(90 * time.Second)
	task, err := store.Update(t.Context(), cronjob.UpdateRequest{Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version, Until: &until, Now: now.Add(time.Second)})
	require.NoError(t, err)
	oldCandidate := cronjob.Candidate{Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version - 1, Kind: cronjob.RunScheduled, ScheduledAt: task.NextRunAt}
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: oldCandidate, RunID: "old-version", Now: task.NextRunAt, RunTimeout: time.Minute, MaxConcurrency: 1})
	require.ErrorIs(t, err, cronjob.ErrConflict)
	lease = claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), "recovered", task.NextRunAt, 1, 0)
	_, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: recoverAt})
	require.NoError(t, err)
	task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.Equal(t, until, task.NextRunAt)
	require.Equal(t, lease.ScheduledAt, task.LatestResult.ScheduledAt)
}

func TestAgentCronUntilCleanupDoesNotConsumeReportCandidateLimit(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	expired := make([]cronjob.Task, 0, 3)
	for i := range 3 {
		task := createAgentCronUntil(t, store, "@at 90s", now, int64(-3060-i))
		lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, task.NextRunAt), fmt.Sprint("expired", i), task.NextRunAt, 1, 0)
		task = finishAgentCronUntil(t, store, lease, task.NextRunAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
		expired = append(expired, failAgentCronUntilReport(t, store, task))
	}
	deadline := expired[0].LatestResult.FinishedAt.Add(24 * time.Hour)
	ready := createAgentCronUntil(t, store, "@at 90s", deadline.Add(-2*time.Minute), -3070)
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, ready, ready.NextRunAt), "ready", ready.NextRunAt, 1, 0)
	ready = finishAgentCronUntil(t, store, lease, deadline.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliveryPending)
	candidates, err := NewAgentCronStore().Reports(t.Context(), cronjob.ReportsRequest{Coordinator: ready.Scope.Coordinator(), Now: ready.UpdatedAt, Limit: 1})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	require.Equal(t, ready.ID, candidates[0].TaskID)
	for _, task := range expired {
		requireAgentCronMissing(t, store, task)
	}
}
