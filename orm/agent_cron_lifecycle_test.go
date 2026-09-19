package orm

import (
	"context"
	"errors"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type agentCronBlockAfterCommandHook struct {
	command string
	key     string
	read    chan struct{}
	release chan struct{}
	once    sync.Once
}

func (h *agentCronBlockAfterCommandHook) DialHook(next redis.DialHook) redis.DialHook {
	return func(ctx context.Context, network, addr string) (net.Conn, error) {
		return next(ctx, network, addr)
	}
}

func (h *agentCronBlockAfterCommandHook) ProcessHook(next redis.ProcessHook) redis.ProcessHook {
	return func(ctx context.Context, command redis.Cmder) error {
		err := next(ctx, command)
		if command.Name() == h.command && len(command.Args()) > 1 && command.Args()[1] == h.key {
			h.once.Do(func() { close(h.read) })
			select {
			case <-h.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return err
	}
}

func (h *agentCronBlockAfterCommandHook) ProcessPipelineHook(next redis.ProcessPipelineHook) redis.ProcessPipelineHook {
	return next
}

func dueCandidateForTask(t *testing.T, store cronjob.Store, task cronjob.Task, now time.Time) cronjob.Candidate {
	t.Helper()
	candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: now, Limit: 50})
	require.NoError(t, err)
	for _, candidate := range candidates {
		if candidate.TaskID == task.ID {
			return candidate
		}
	}
	require.FailNow(t, "task was not due", task.ID)
	return cronjob.Candidate{}
}

func claimAgentCronTask(t *testing.T, store cronjob.Store, candidate cronjob.Candidate, runID string, now time.Time, maxConcurrency int, cooldown time.Duration) cronjob.Lease {
	t.Helper()
	lease, err := store.Claim(t.Context(), cronjob.ClaimRequest{
		Candidate: candidate, RunID: runID, Now: now, RunTimeout: time.Minute,
		GracePeriod: 10 * time.Second, MaxConcurrency: maxConcurrency, ChatCooldown: cooldown,
	})
	require.NoError(t, err)
	return lease
}

func finishAgentCronTask(t *testing.T, store cronjob.Store, lease cronjob.Lease, now time.Time, outcome cronjob.Outcome, reportState cronjob.DeliveryState) cronjob.Task {
	t.Helper()
	task, err := store.Finish(t.Context(), cronjob.FinishRequest{
		Lease: lease, Result: cronjob.ExecutionResult{Outcome: outcome, Text: "result"},
		Report: cronjob.Report{State: reportState}, NextRunAt: now.Add(time.Hour), Now: now,
	})
	require.NoError(t, err)
	return task
}

func TestAgentCronConcurrentClaimAndExactCandidateFencing(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1300), 41, createdAt, "claim")
	dueAt := task.NextRunAt
	candidate := dueCandidateForTask(t, store, task, dueAt)

	stale := candidate
	stale.ScheduledAt = stale.ScheduledAt.Add(time.Minute)
	_, err := store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: stale, RunID: "stale", Now: dueAt, RunTimeout: time.Minute, GracePeriod: time.Second, MaxConcurrency: 2})
	assert.ErrorIs(t, err, cronjob.ErrConflict)

	start := make(chan struct{})
	type claimResult struct {
		lease cronjob.Lease
		err   error
	}
	results := make(chan claimResult, 2)
	var wg sync.WaitGroup
	for i := range 2 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			lease, claimErr := NewAgentCronStore().Claim(t.Context(), cronjob.ClaimRequest{
				Candidate: candidate, RunID: "run-" + string(rune('a'+i)), Now: dueAt,
				RunTimeout: time.Minute, GracePeriod: time.Second, MaxConcurrency: 2,
			})
			results <- claimResult{lease: lease, err: claimErr}
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	successes := 0
	conflicts := 0
	var won cronjob.Lease
	for result := range results {
		switch {
		case result.err == nil:
			successes++
			won = result.lease
		case errors.Is(result.err, cronjob.ErrConflict):
			conflicts++
		default:
			assert.ErrorIs(t, result.err, cronjob.ErrConflict)
		}
	}
	assert.Equal(t, 1, successes)
	assert.Equal(t, 1, conflicts)
	require.NotEmpty(t, won.Token)

	finishAgentCronTask(t, store, won, dueAt.Add(10*time.Second), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{Candidate: candidate, RunID: "late", Now: dueAt.Add(20 * time.Second), RunTimeout: time.Minute, MaxConcurrency: 2})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
}

func TestAgentCronClaimRevalidatesCandidateBeforeCapacity(t *testing.T) {
	mr := setupAgentV3Redis(t)
	winnerStore := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 10, 30, 0, 0, time.UTC)
	task := createAgentCronTask(t, winnerStore, agentCronTestScope(-1350), 46, createdAt, "claim-snapshot")
	dueAt := task.NextRunAt
	candidate := dueCandidateForTask(t, winnerStore, task, dueAt)

	loserClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = loserClient.Close() })
	hook := &agentCronBlockAfterCommandHook{
		command: "zscore",
		key:     (&agentCronStore{}).dueKey(task.Scope.Coordinator()),
		read:    make(chan struct{}),
		release: make(chan struct{}),
	}
	loserClient.AddHook(hook)
	loserStore := &agentCronStore{client: loserClient}
	type claimResult struct {
		lease cronjob.Lease
		err   error
	}
	result := make(chan claimResult, 1)
	go func() {
		lease, err := loserStore.Claim(t.Context(), cronjob.ClaimRequest{
			Candidate: candidate, RunID: "loser", Now: dueAt, RunTimeout: time.Minute,
			GracePeriod: time.Second, MaxConcurrency: 2,
		})
		result <- claimResult{lease: lease, err: err}
	}()

	select {
	case <-hook.read:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "loser did not read the task")
	}
	winner := claimAgentCronTask(t, winnerStore, candidate, "winner", dueAt, 2, 0)
	close(hook.release)
	loser := <-result
	assert.ErrorIs(t, loser.err, cronjob.ErrConflict)
	assert.Empty(t, loser.lease.Token)
	finishAgentCronTask(t, winnerStore, winner, dueAt.Add(time.Second), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)
}

func TestAgentCronDueKeepsChatFairnessAndSkipsBlockedChatBeforeLimit(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 10, 45, 0, 0, time.UTC)
	blockedScope := agentCronTestScope(-1360)
	blocked := make([]cronjob.Task, 0, 140)
	for i := range 140 {
		request := agentCronTestCreate(blockedScope, 47, createdAt, "blocked-"+strconv.Itoa(i))
		request.MaxTasksPerChat = 200
		created, err := store.Create(t.Context(), request)
		require.NoError(t, err)
		blocked = append(blocked, created.Task)
	}
	eligibleRequest := agentCronTestCreate(agentCronTestScope(-1361), 48, createdAt, "eligible")
	eligibleRequest.NextRunAt = blocked[0].NextRunAt.Add(time.Millisecond)
	eligibleResult, err := store.Create(t.Context(), eligibleRequest)
	require.NoError(t, err)
	eligible := eligibleResult.Task
	dueAt := eligible.NextRunAt
	candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: blockedScope.Coordinator(), Now: dueAt, Limit: 2})
	require.NoError(t, err)
	require.Len(t, candidates, 2)
	assert.Equal(t, blockedScope.ChatID, candidates[0].Scope.ChatID)
	assert.Equal(t, eligible.ID, candidates[1].TaskID)

	blockedCandidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: blockedScope.Coordinator(), Now: blocked[0].NextRunAt, Limit: 10})
	require.NoError(t, err)
	require.NotEmpty(t, blockedCandidates)
	lease := claimAgentCronTask(t, store, blockedCandidates[0], "sets-chat-cooldown", blocked[0].NextRunAt, 10, time.Hour)

	candidates, err = store.Due(t.Context(), cronjob.DueRequest{Coordinator: blockedScope.Coordinator(), Now: dueAt, Limit: 2})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, eligible.ID, candidates[0].TaskID)

	finishAgentCronTask(t, store, lease, dueAt.Add(100*time.Microsecond), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)
	candidates, err = store.Due(t.Context(), cronjob.DueRequest{Coordinator: blockedScope.Coordinator(), Now: dueAt.Add(200 * time.Microsecond), Limit: 2})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, eligible.ID, candidates[0].TaskID)
}

func TestAgentCronDueReturnsOneCandidatePerChatAcrossScheduledAndRetry(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 10, 48, 0, 0, time.UTC)
	scope := agentCronTestScope(-1365)
	retryTask := createAgentCronTask(t, store, scope, 48, createdAt, "retry")
	dueAt := retryTask.NextRunAt
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, retryTask, dueAt), "failed-for-retry", dueAt, 10, 0)
	retryTask = finishAgentCronTask(t, store, lease, dueAt.Add(time.Second), cronjob.OutcomeFailed, cronjob.DeliverySuppressed)
	retried, err := store.Retry(t.Context(), cronjob.RetryRequest{
		Actor:           cronjob.Actor{Scope: scope, UserID: retryTask.CreatorID},
		TaskID:          retryTask.ID,
		ExpectedVersion: retryTask.Version,
		RunID:           retryTask.LatestResult.RunID,
		Now:             dueAt.Add(2 * time.Second),
		MaxRetries:      2,
	})
	require.NoError(t, err)
	require.Equal(t, cronjob.RetryQueued, retried.Task.LatestResult.RetryStatus)

	scheduledRequest := agentCronTestCreate(scope, 48, dueAt.Add(time.Second), "scheduled")
	scheduledRequest.NextRunAt = dueAt.Add(2 * time.Second)
	scheduled, err := store.Create(t.Context(), scheduledRequest)
	require.NoError(t, err)
	candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: scope.Coordinator(), Now: dueAt.Add(2 * time.Second), Limit: 10})
	require.NoError(t, err)
	require.Len(t, candidates, 1)
	assert.Equal(t, scheduled.Task.ID, candidates[0].TaskID)
	assert.Equal(t, cronjob.RunScheduled, candidates[0].Kind)
}

func TestAgentCronMalformedCoordinationStateFailsClosed(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 10, 50, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1370), 49, createdAt, "malformed-lock")
	concrete := store.(*agentCronStore)
	require.NoError(t, rc.HSet(t.Context(), concrete.chatLocksKey(task.Scope.Coordinator()), strconv.FormatInt(task.Scope.ChatID, 10), "malformed").Err())

	candidates, err := store.Due(t.Context(), cronjob.DueRequest{Coordinator: task.Scope.Coordinator(), Now: task.NextRunAt, Limit: 10})
	assert.ErrorIs(t, err, cronjob.ErrUnavailable)
	assert.Empty(t, candidates)
	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{
		Candidate: cronjob.Candidate{
			Scope: task.Scope, TaskID: task.ID, TaskVersion: task.Version,
			Kind: cronjob.RunScheduled, ScheduledAt: task.NextRunAt,
		},
		RunID: "must-not-claim", Now: task.NextRunAt, RunTimeout: time.Minute, MaxConcurrency: 2,
	})
	assert.ErrorIs(t, err, cronjob.ErrUnavailable)
	got, getErr := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, getErr)
	assert.Nil(t, got.ActiveRun)
}

func TestAgentCronGlobalCapacityChatCooldownAndDeletedLeaseCleanup(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC)
	taskA := createAgentCronTask(t, store, agentCronTestScope(-1401), 51, createdAt, "a")
	taskB := createAgentCronTask(t, store, agentCronTestScope(-1402), 52, createdAt, "b")
	dueAt := taskA.NextRunAt
	leaseA := claimAgentCronTask(t, store, dueCandidateForTask(t, store, taskA, dueAt), "run-a", dueAt, 1, time.Hour)
	taskSameChat := createAgentCronTask(t, store, taskA.Scope, 51, createdAt, "same-chat")

	_, err := store.Claim(t.Context(), cronjob.ClaimRequest{
		Candidate: dueCandidateForTask(t, store, taskB, dueAt), RunID: "run-b", Now: dueAt,
		RunTimeout: time.Minute, MaxConcurrency: 1,
	})
	assert.ErrorIs(t, err, cronjob.ErrRateLimited)

	err = store.Delete(t.Context(), cronjob.DeleteRequest{
		Actor: cronjob.Actor{Scope: taskA.Scope, UserID: taskA.CreatorID}, TaskID: taskA.ID, ExpectedVersion: taskA.Version,
	})
	require.NoError(t, err)
	_, err = store.Finish(t.Context(), cronjob.FinishRequest{
		Lease: leaseA, Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded},
		Report: cronjob.Report{State: cronjob.DeliverySuppressed}, Now: dueAt.Add(time.Second), NextRunAt: dueAt.Add(time.Hour),
	})
	assert.ErrorIs(t, err, cronjob.ErrNotFound)

	leaseB := claimAgentCronTask(t, store, dueCandidateForTask(t, store, taskB, dueAt.Add(2*time.Second)), "run-b", dueAt.Add(2*time.Second), 1, 0)
	finishAgentCronTask(t, store, leaseB, dueAt.Add(3*time.Second), cronjob.OutcomeSucceeded, cronjob.DeliverySuppressed)

	_, err = store.Claim(t.Context(), cronjob.ClaimRequest{
		Candidate: cronjob.Candidate{
			Scope: taskSameChat.Scope, TaskID: taskSameChat.ID, TaskVersion: taskSameChat.Version,
			Kind: cronjob.RunScheduled, ScheduledAt: taskSameChat.NextRunAt,
		}, RunID: "same-chat", Now: dueAt.Add(4 * time.Second),
		RunTimeout: time.Minute, MaxConcurrency: 2,
	})
	assert.ErrorIs(t, err, cronjob.ErrRateLimited)
}

func TestAgentCronRecoverExpiredRunDoesNotReplayOccurrence(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	createdAt := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	task := createAgentCronTask(t, store, agentCronTestScope(-1500), 61, createdAt, "recover")
	dueAt := task.NextRunAt
	lease := claimAgentCronTask(t, store, dueCandidateForTask(t, store, task, dueAt), "run-recover", dueAt, 1, 0)
	recoverAt := lease.ExpiresAt.Add(time.Second)

	_, err := store.Finish(t.Context(), cronjob.FinishRequest{
		Lease: lease, Result: cronjob.ExecutionResult{Outcome: cronjob.OutcomeSucceeded},
		Report: cronjob.Report{}, Now: recoverAt, NextRunAt: recoverAt.Add(time.Hour),
	})
	assert.ErrorIs(t, err, cronjob.ErrConflict)

	recovered, err := store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: recoverAt, Limit: 10})
	require.NoError(t, err)
	assert.Equal(t, 1, recovered.Recovered)
	got, err := store.Get(t.Context(), cronjob.GetRequest{Scope: task.Scope, TaskID: task.ID})
	require.NoError(t, err)
	require.NotNil(t, got.LatestResult)
	assert.Equal(t, cronjob.OutcomeFailed, got.LatestResult.Outcome)
	assert.Equal(t, "interrupted", got.LatestResult.ErrorCode)
	assert.True(t, got.LatestResult.SideEffectsPossible)
	assert.True(t, got.NextRunAt.After(recoverAt))
	assert.Nil(t, got.ActiveRun)

	recovered, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: task.Scope.Coordinator(), Now: recoverAt.Add(time.Second), Limit: 10})
	require.NoError(t, err)
	assert.Zero(t, recovered.Recovered)
}

func TestAgentCronRecoverExpiredLeaseRangeHonorsScoreAndLimit(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore().(*agentCronStore)
	coordinator := agentCronTestScope(-1510).Coordinator()
	now := time.Date(2026, 9, 19, 13, 0, 0, 0, time.UTC)
	key := store.leasesKey(coordinator)
	require.NoError(t, rc.ZAdd(t.Context(), key,
		redis.Z{Score: agentCronTimeScore(now.Add(-2 * time.Second)), Member: "expired-first"},
		redis.Z{Score: agentCronTimeScore(now.Add(-time.Second)), Member: "expired-second"},
		redis.Z{Score: agentCronTimeScore(now.Add(time.Second)), Member: "future"},
	).Err())

	_, err := store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: coordinator, Now: now, Limit: 1})
	require.NoError(t, err)
	assert.ErrorIs(t, rc.ZScore(t.Context(), key, "expired-first").Err(), redis.Nil)
	require.NoError(t, rc.ZScore(t.Context(), key, "expired-second").Err())
	require.NoError(t, rc.ZScore(t.Context(), key, "future").Err())

	_, err = store.Recover(t.Context(), cronjob.RecoverRequest{Coordinator: coordinator, Now: now, Limit: 1})
	require.NoError(t, err)
	assert.ErrorIs(t, rc.ZScore(t.Context(), key, "expired-second").Err(), redis.Nil)
	require.NoError(t, rc.ZScore(t.Context(), key, "future").Err())
}
