package orm

import (
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"csust-got/cronjob"

	"github.com/redis/go-redis/v9"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func agentCronTestScope(chatID int64) cronjob.Scope {
	return cronjob.Scope{Bot: "testbot", Platform: "tg", ChatID: chatID}
}

func agentCronTestCreate(scope cronjob.Scope, creator int64, now time.Time, suffix string) cronjob.CreateRequest {
	return cronjob.CreateRequest{
		Actor: cronjob.Actor{Scope: scope, UserID: creator}, SourceAgent: "source", ChatType: "group",
		Cron: " */1  * * * * ", Timezone: "UTC", Prompt: "\r\n## Context\r\n" + suffix + "\r\n## Steps\r\nrun\r\n## Goal\r\ndone\r\n",
		NextRunAt: now.Add(time.Minute), Now: now, MaxTasksPerChat: 20,
	}
}

func createAgentCronTask(t *testing.T, store cronjob.Store, scope cronjob.Scope, creator int64, now time.Time, suffix string) cronjob.Task {
	t.Helper()
	created, err := store.Create(t.Context(), agentCronTestCreate(scope, creator, now, suffix))
	require.NoError(t, err)
	return created.Task
}

func TestAgentCronCreateDedupQuotaAndScope(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-1001)

	request := agentCronTestCreate(scope, 11, now, "same")
	request.MaxTasksPerChat = 2
	first, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	assert.False(t, first.Deduplicated)
	assert.Equal(t, "*/1 * * * *", first.Task.Cron)
	assert.NotContains(t, first.Task.Prompt, "\r")

	duplicate, err := store.Create(t.Context(), request)
	require.NoError(t, err)
	assert.True(t, duplicate.Deduplicated)
	assert.Equal(t, first.Task.ID, duplicate.Task.ID)

	otherCreator := request
	otherCreator.Actor.UserID = 12
	second, err := store.Create(t.Context(), otherCreator)
	require.NoError(t, err)
	assert.NotEqual(t, first.Task.ID, second.Task.ID)

	overQuota := agentCronTestCreate(scope, 11, now, "different")
	overQuota.MaxTasksPerChat = 2
	_, err = store.Create(t.Context(), overQuota)
	assert.ErrorIs(t, err, cronjob.ErrQuotaExceeded)

	page, err := store.List(t.Context(), cronjob.ListRequest{Scope: scope, Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Tasks, 1)
	assert.NotEmpty(t, page.NextCursor)
	page2, err := store.List(t.Context(), cronjob.ListRequest{Scope: scope, Cursor: page.NextCursor, Limit: 10})
	require.NoError(t, err)
	require.Len(t, page2.Tasks, 1)

	_, err = store.Get(t.Context(), cronjob.GetRequest{Scope: agentCronTestScope(-1002), TaskID: first.Task.ID})
	assert.ErrorIs(t, err, cronjob.ErrNotFound)
}

func TestAgentCronConcurrentCreateIsAtomicAcrossStores(t *testing.T) {
	mr := setupAgentV3Redis(t)
	secondClient := redis.NewClient(&redis.Options{Addr: mr.Addr()})
	t.Cleanup(func() { _ = secondClient.Close() })
	stores := []cronjob.Store{NewAgentCronStore(), &agentCronStore{client: secondClient}}
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	request := agentCronTestCreate(agentCronTestScope(-1100), 21, now, "concurrent")
	const workers = 16
	start := make(chan struct{})
	results := make(chan cronjob.CreateResult, workers)
	errs := make(chan error, workers)
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			created, err := stores[i%len(stores)].Create(t.Context(), request)
			results <- created
			errs <- err
		}()
	}
	close(start)
	wg.Wait()
	close(results)
	close(errs)
	for err := range errs {
		require.NoError(t, err)
	}
	ids := make(map[string]struct{})
	nonDeduplicated := 0
	for result := range results {
		ids[result.Task.ID] = struct{}{}
		if !result.Deduplicated {
			nonDeduplicated++
		}
	}
	assert.Len(t, ids, 1)
	assert.Equal(t, 1, nonDeduplicated)
}

func TestAgentCronCreatorCASUpdateAndDelete(t *testing.T) {
	setupAgentV3Redis(t)
	store := NewAgentCronStore()
	now := time.Date(2026, 9, 19, 10, 0, 0, 0, time.UTC)
	scope := agentCronTestScope(-1200)
	task := createAgentCronTask(t, store, scope, 31, now, "original")
	prompt := "## Context\nupdated\n## Steps\nrun\n## Goal\ndone"

	_, err := store.Update(t.Context(), cronjob.UpdateRequest{
		Actor: cronjob.Actor{Scope: scope, UserID: 32}, TaskID: task.ID, ExpectedVersion: task.Version,
		Prompt: &prompt, NextRunAt: now.Add(2 * time.Minute), Now: now.Add(time.Second),
	})
	assert.ErrorIs(t, err, cronjob.ErrForbidden)

	updated, err := store.Update(t.Context(), cronjob.UpdateRequest{
		Actor: cronjob.Actor{Scope: scope, UserID: 31}, TaskID: task.ID, ExpectedVersion: task.Version,
		Prompt: &prompt, NextRunAt: now.Add(2 * time.Minute), Now: now.Add(time.Second),
	})
	require.NoError(t, err)
	assert.Equal(t, int64(2), updated.Version)
	assert.Equal(t, prompt, updated.Prompt)

	err = store.Delete(t.Context(), cronjob.DeleteRequest{Actor: cronjob.Actor{Scope: scope, UserID: 31}, TaskID: task.ID, ExpectedVersion: task.Version})
	assert.ErrorIs(t, err, cronjob.ErrConflict)
	err = store.Delete(t.Context(), cronjob.DeleteRequest{Actor: cronjob.Actor{Scope: scope, UserID: 31}, TaskID: task.ID, ExpectedVersion: updated.Version})
	require.NoError(t, err)
	_, err = store.Get(t.Context(), cronjob.GetRequest{Scope: scope, TaskID: task.ID})
	assert.True(t, errors.Is(err, cronjob.ErrNotFound), fmt.Sprintf("unexpected error: %v", err))
}
