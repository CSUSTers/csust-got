package orm

import (
	"testing"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func finishAgentCronWithPendingReport(t *testing.T, store cronjob.Store, task cronjob.Task, dueAt time.Time, outcome cronjob.Outcome) cronjob.Task {
	t.Helper()
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, dueAt), "run-report", dueAt, 2, 0)
	return finishAgentCronTask(t, store, lease, dueAt.Add(time.Second), outcome, cronjob.DeliveryPending)
}

func nextReportCandidate(t *testing.T, store cronjob.Store, task cronjob.Task, now time.Time) cronjob.ReportCandidate {
	t.Helper()
	candidates, err := store.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: now, Limit: 20})
	require.NoError(t, err)
	for _, candidate := range candidates {
		if candidate.TaskID == task.ID {
			return candidate
		}
	}
	require.FailNow(t, "report was not ready", task.ID)
	return cronjob.ReportCandidate{}
}

func claimReportForTask(t *testing.T, store cronjob.Store, task cronjob.Task, now time.Time) cronjob.ReportLease {
	t.Helper()
	lease, err := store.ClaimReport(t.Context(), cronjob.ClaimReportRequest{
		Candidate: nextReportCandidate(t, store, task, now), Now: now, Timeout: time.Minute, GracePeriod: time.Second,
	})
	require.NoError(t, err)
	return lease
}

func failReport(t *testing.T, store cronjob.Store, lease cronjob.ReportLease, now time.Time, receipt *cronjob.DeliveryReceipt) cronjob.Task {
	t.Helper()
	task, err := store.FinishReport(t.Context(), cronjob.ReportFinishRequest{
		Lease: lease, Now: now, ErrorMessage: "telegram unavailable", NextAttemptAt: now.Add(time.Minute), Receipt: receipt,
	})
	require.NoError(t, err)
	return task
}

func TestAgentCronPendingOutboxBlocksUpdateAndExecution(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1600), 71, createdAt, "pending")
	task = finishAgentCronWithPendingReport(t, store, task, task.NextRunAt, cronjob.OutcomeSucceeded)
	require.NotNil(t, task.Report)
	assert.Equal(t, cronjob.DeliveryPending, task.Report.State)

	prompt := "## Context\nnew\n## Steps\nrun\n## Goal\ndone"
	_, err := store.Update(t.Context(), cronjob.UpdateRequest{
		Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID, ExpectedVersion: task.Version,
		Prompt: &prompt, Now: task.UpdatedAt.Add(time.Second), NextRunAt: task.NextRunAt.Add(time.Hour),
	})
	assert.ErrorIs(t, err, cronjob.ErrConflict)

	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{
		Candidate: cronjob.Candidate{Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version, Kind: cronjob.RunScheduled, ScheduledAt: task.NextRunAt},
		RunID:     "blocked", Now: task.NextRunAt, RunTimeout: time.Minute, MaxConcurrency: 2,
	})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronReportLeaseRecoveryAndStaleFinish(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 14, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1700), 81, createdAt, "lease")
	task = finishAgentCronWithPendingReport(t, store, task, task.NextRunAt, cronjob.OutcomeSucceeded)
	claimAt := task.UpdatedAt
	lease := claimReportForTask(t, store, task, claimAt)
	afterExpiry := lease.ExpiresAt.Add(time.Second)

	_, err := store.FinishReport(t.Context(), cronjob.ReportFinishRequest{Lease: lease, Now: afterExpiry, Delivered: true})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	candidate := nextReportCandidate(t, store, task, afterExpiry)
	assert.Equal(t, 1, candidate.Attempt)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.NotNil(t, got.Report)
	assert.Equal(t, cronjob.DeliveryPending, got.Report.State)
	assert.Equal(t, 1, got.Report.Attempt)
}

func TestAgentCronReportOnlyRetryVersionAndBudgetAreMonotonic(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1800), 91, createdAt, "report-retry")
	task = finishAgentCronWithPendingReport(t, store, task, task.NextRunAt, cronjob.OutcomeSucceeded)
	runID := task.LatestResult.RunID
	now := task.UpdatedAt

	for attempt := 1; attempt <= 3; attempt++ {
		lease := claimReportForTask(t, store, task, now)
		var receipt *cronjob.DeliveryReceipt
		if attempt == 1 {
			receipt = &cronjob.DeliveryReceipt{MessageIDs: []int{100}}
		}
		task = failReport(t, store, lease, now.Add(time.Second), receipt)
		if attempt < 3 {
			now = task.Report.NextAttemptAt
		}
	}
	assert.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
	assert.Equal(t, []int{100}, task.Report.Receipt.MessageIDs)
	assert.Equal(t, 3, task.Report.Attempt)
	originalResultVersion := task.LatestResult.TaskVersion

	retryRequest := cronjob.RetryRequest{
		Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID,
		ExpectedVersion: task.Version, RunID: runID, Now: now.Add(time.Minute), MaxRetries: 2,
	}
	retried, err := store.Retry(t.Context(), retryRequest)
	require.NoError(t, err)
	assert.Equal(t, cronjob.RetryReportOnly, retried.Mode)
	assert.Equal(t, task.Version+1, retried.Task.Version)
	assert.Equal(t, retried.Task.Version, retried.Task.Report.TaskVersion)
	assert.Equal(t, originalResultVersion, retried.Task.LatestResult.TaskVersion)

	_, err = store.Retry(t.Context(), retryRequest)
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	task = retried.Task
	now = task.Report.NextAttemptAt
	task = failReport(t, store, claimReportForTask(t, store, task, now), now.Add(time.Second), nil)
	assert.Equal(t, 4, task.Report.Attempt)
	assert.Equal(t, cronjob.DeliveryExhausted, task.Report.State)

	retried, err = store.Retry(t.Context(), cronjob.RetryRequest{
		Actor: retryRequest.Actor, TaskID: task.ID, ExpectedVersion: task.Version, RunID: runID,
		Now: now.Add(2 * time.Minute), MaxRetries: 2,
	})
	require.NoError(t, err)
	task = retried.Task
	now = task.Report.NextAttemptAt
	task = failReport(t, store, claimReportForTask(t, store, task, now), now.Add(time.Second), nil)
	assert.Equal(t, 5, task.Report.Attempt)
	assert.Equal(t, cronjob.DeliveryExhausted, task.Report.State)

	_, err = store.Retry(t.Context(), cronjob.RetryRequest{
		Actor: retryRequest.Actor, TaskID: task.ID, ExpectedVersion: task.Version, RunID: runID,
		Now: now.Add(3 * time.Minute), MaxRetries: 2,
	})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronRichResultAndPartialReceiptSurviveReportOnlyRetry(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 9, 19, 15, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1810), 91, now, "rich-report")
	due := task.NextRunAt
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, due), "rich-run", due, 2, 0)
	markdown := "<telegram_rich_message># 状态 🙂\n\n**完成**</telegram_rich_message>"
	task, err := store.Finish(t.Context(), cronjob.FinishRequest{
		Lease: lease, Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded, Format: "telegram_rich_message_v1", Text: markdown},
		Report: cronjob.Report{State: cronjob.DeliveryPending}, NextRunAt: due.Add(time.Hour), Now: due.Add(time.Second),
	})
	require.NoError(t, err)
	resultVersion, nextRun := task.LatestResult.TaskVersion, task.NextRunAt
	for attempt := 1; attempt <= 3; attempt++ {
		now = task.Report.NextAttemptAt
		var receipt *cronjob.DeliveryReceipt
		if attempt == 1 {
			receipt = &cronjob.DeliveryReceipt{MessageIDs: []int{101}}
		}
		task = failReport(t, store, claimReportForTask(t, store, task, now), now.Add(time.Second), receipt)
		persisted, getErr := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
		require.NoError(t, getErr)
		assert.Equal(t, "telegram_rich_message_v1", persisted.LatestResult.Format)
		assert.Equal(t, markdown, persisted.LatestResult.Text)
		assert.Equal(t, []int{101}, persisted.Report.Receipt.MessageIDs)
		assert.True(t, persisted.Report.Receipt.DeliveredAt.IsZero())
	}
	assert.Equal(t, cronjob.DeliveryExhausted, task.Report.State)
	retried, err := store.Retry(t.Context(), cronjob.RetryRequest{
		Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID,
		ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: now.Add(time.Minute), MaxRetries: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, cronjob.RetryReportOnly, retried.Mode)
	assert.Equal(t, resultVersion, retried.Task.LatestResult.TaskVersion)
	assert.Equal(t, resultVersion+1, retried.Task.Report.TaskVersion)
	assert.Equal(t, nextRun, retried.Task.NextRunAt)
	assert.Equal(t, []int{101}, retried.Task.Report.Receipt.MessageIDs)
	now = retried.Task.Report.NextAttemptAt
	task, err = store.FinishReport(t.Context(), cronjob.ReportFinishRequest{
		Lease: claimReportForTask(t, store, retried.Task, now), Now: now.Add(time.Second), Delivered: true,
		Receipt: &cronjob.DeliveryReceipt{MessageIDs: []int{101, 102}, DeliveredAt: now.Add(time.Second)},
	})
	require.NoError(t, err)
	assert.Equal(t, cronjob.DeliveryDelivered, task.Report.State)
	assert.Equal(t, []int{101, 102}, task.Report.Receipt.MessageIDs)
	assert.Equal(t, "telegram_rich_message_v1", task.LatestResult.Format)
	assert.Equal(t, markdown, task.LatestResult.Text)
}

func TestAgentCronExecutionRetryIsFiniteAndNormalDueWins(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 16, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1900), 101, createdAt, "execution-retry")
	dueAt := task.NextRunAt
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, dueAt), "failed-run", dueAt, 2, 0)
	task = finishAgentCronTask(t, store, lease, dueAt.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliverySuppressed)
	require.Equal(t, cronjob.RetryAvailable, task.LatestResult.RetryStatus)

	retried, err := store.Retry(t.Context(), cronjob.RetryRequest{
		Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID,
		ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: dueAt.Add(2 * time.Second), MaxRetries: 2,
	})
	require.NoError(t, err)
	assert.Equal(t, cronjob.RetryExecution, retried.Mode)
	retryCandidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: dueAt.Add(3 * time.Second), Limit: 10})
	require.NoError(t, err)
	require.Len(t, retryCandidates, 1)
	assert.Equal(t, cronjob.RunRetry, retryCandidates[0].Kind)
	assert.Equal(t, 1, retryCandidates[0].RetryCount)

	normalCandidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: retried.Task.NextRunAt, Limit: 10})
	require.NoError(t, err)
	require.Len(t, normalCandidates, 1)
	assert.Equal(t, cronjob.RunScheduled, normalCandidates[0].Kind)
	claimed := claimAgentCronTask(t, store, normalCandidates[0], "normal-wins", retried.Task.NextRunAt, 2, 0)
	require.NotNil(t, claimed.Task.LatestResult)
	assert.Equal(t, cronjob.RetryConsumed, claimed.Task.LatestResult.RetryStatus)
}

func TestAgentCronReportExpiresAfterTwentyFourHours(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 17, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-2000), 111, createdAt, "expiry")
	task = finishAgentCronWithPendingReport(t, store, task, task.NextRunAt, cronjob.OutcomeSucceeded)
	expiredAt := task.LatestResult.FinishedAt.Add(24 * time.Hour)
	reports, err := store.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: expiredAt, Limit: 10})
	require.NoError(t, err)
	assert.Empty(t, reports)
	task, err = store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	assert.Equal(t, cronjob.DeliveryFailed, task.Report.State)
	_, err = store.Retry(t.Context(), cronjob.RetryRequest{
		Actor: cronjob.Actor{Scope: task.Scope, UserID: task.CreatorID}, TaskID: task.ID,
		ExpectedVersion: task.Version, RunID: task.LatestResult.RunID, Now: expiredAt, MaxRetries: 2,
	})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronRecoverExpiredReportLeaseRangeHonorsScoreAndLimit(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	coordinator := agentCronTestScope(-2010).Coordinator()
	now := time.Date(2026, 9, 19, 18, 0, 0, 0, time.UTC)
	key := store.reportLeasesKey(coordinator)
	require.NoError(t, rc.ZAdd(t.Context(), key,
		redis.Z{Score: agentCronTimeScore(now.Add(-2 * time.Second)), Member: "expired-first"},
		redis.Z{Score: agentCronTimeScore(now.Add(-time.Second)), Member: "expired-second"},
		redis.Z{Score: agentCronTimeScore(now.Add(time.Second)), Member: "future"},
	).Err())

	require.NoError(t, store.recoverExpiredReportLeases(t.Context(), coordinator, now, 1))
	assert.ErrorIs(t, rc.ZScore(t.Context(), key, "expired-first").Err(), redis.Nil)
	require.NoError(t, rc.ZScore(t.Context(), key, "expired-second").Err())
	require.NoError(t, rc.ZScore(t.Context(), key, "future").Err())

	require.NoError(t, store.recoverExpiredReportLeases(t.Context(), coordinator, now, 1))
	assert.ErrorIs(t, rc.ZScore(t.Context(), key, "expired-second").Err(), redis.Nil)
	require.NoError(t, rc.ZScore(t.Context(), key, "future").Err())
}

func TestAgentCronReportsScansPastMalformedEntriesWithinResultLimit(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	task := createAgentCronTask(t, store, agentCronTestScope(-2020), 112, time.Date(2026, 9, 19, 19, 0, 0, 0, time.UTC), "scan")
	task = finishAgentCronWithPendingReport(t, store, task, task.NextRunAt, cronjob.OutcomeSucceeded)
	concrete := store.(*agentCronStore)
	require.NoError(t, rc.ZAdd(t.Context(), concrete.reportsKey(task.Scope.Coordinator()), redis.Z{
		Score: agentCronTimeScore(task.UpdatedAt.Add(-time.Second)), Member: "malformed",
	}).Err())

	reports, err := store.Reports(t.Context(), cronjob.ReportsRequest{Coordinator: task.Scope.Coordinator(), Now: task.UpdatedAt, Limit: 1})
	require.NoError(t, err)
	require.Len(t, reports, 1)
	assert.Equal(t, task.ID, reports[0].TaskID)
}
