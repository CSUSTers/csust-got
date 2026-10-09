package agentv3

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"csust-got/agent/session"
	"csust-got/config"

	"github.com/cloudwego/eino/schema"
	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionCatchUpDecision(t *testing.T) {
	location := time.FixedZone("catch-up", 8*3600)
	today := time.Date(2026, 10, 8, 0, 0, 0, 0, location)
	tests := []struct {
		name string
		now  time.Time
		last string
		want bool
	}{
		{"before today's 02:00", today.Add(time.Hour), "", false},
		{"exactly 02:00 still pending", today.Add(2 * time.Hour).Add(-time.Second), "", false},
		{"after 02:00 and never collected", today.Add(10 * time.Hour), "", true},
		{"after 02:00 and collected yesterday", today.Add(10 * time.Hour), "2026-10-07", true},
		{"after 02:00 and collected today", today.Add(10 * time.Hour), "2026-10-08", false},
		{"late night without collection", today.Add(23*time.Hour + 59*time.Minute), "", true},
		{"marker from another zone's date", today.Add(3 * time.Hour), "2026-10-07", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			require.Equal(t, tt.want, agentV3SessionCatchUpDue(tt.now, location, tt.last))
		})
	}
	require.Equal(t, "2026-10-08", agentV3SessionCollectionDay(today.Add(10*time.Hour), location))
	require.Equal(t, "2026-10-07", agentV3SessionCollectionDay(today.Add(3*time.Hour), time.UTC), "the day follows the process location")
	require.NotEmpty(t, agentV3SessionCollectionDay(time.Now(), nil))
}

var errAgentSessionClaimFailed = errors.New("claim failed")

type agentSessionCatchUpRepository struct {
	session.Repository
	scope       session.Scope
	collections atomic.Int32
	scopeCalls  atomic.Int32
	firstDelay  time.Duration
	claimFails  atomic.Int32
	mu          sync.Mutex
	last        string
	marks       []string
}

func (r *agentSessionCatchUpRepository) Scopes(context.Context) ([]session.Scope, error) {
	if r.scopeCalls.Add(1) == 1 && r.firstDelay > 0 {
		time.Sleep(r.firstDelay)
	}
	return []session.Scope{r.scope}, nil
}

func (r *agentSessionCatchUpRepository) Pending(context.Context, session.Scope) ([]session.Intent, error) {
	return nil, nil
}

func (r *agentSessionCatchUpRepository) Deleting(context.Context, session.Scope) ([]session.Deletion, error) {
	return nil, nil
}

func (r *agentSessionCatchUpRepository) ClaimDeleting(context.Context, session.Scope, time.Duration) ([]session.Deletion, error) {
	r.collections.Add(1)
	if r.claimFails.Add(-1) >= 0 {
		return nil, errAgentSessionClaimFailed
	}
	return nil, nil
}

type agentSessionCatchUpMarkerRepository struct{ *agentSessionCatchUpRepository }

func (r agentSessionCatchUpMarkerRepository) LastCollection(context.Context) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.last, nil
}

func (r agentSessionCatchUpMarkerRepository) MarkCollection(_ context.Context, day string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.last = day
	r.marks = append(r.marks, day)
	return nil
}

func TestAgentV3SessionStartupCatchUpCollection(t *testing.T) {
	f := newAgentSessionFixture(t)
	tests := []struct {
		name            string
		offsetHours     int
		marker          bool
		last            string
		wantCollections int32
		wantMarks       int
	}{
		{"after 02:00 without marker support", 3, false, "", 1, 0},
		{"after 02:00 with no collection today", 3, true, "", 1, 1},
		{"after 02:00 with yesterday's marker", 3, true, "1999-12-31", 1, 1},
		{"after 02:00 already collected today", 3, true, "", 0, 0},
		{"before 02:00", 1, true, "", 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := session.NewFileStore(t.TempDir())
			require.NoError(t, err)
			synctest.Test(t, func(t *testing.T) {
				location := time.FixedZone("catch-up-local", tt.offsetHours*3600)
				base := &agentSessionCatchUpRepository{Repository: f.repo, scope: f.scope(), last: tt.last}
				if tt.name == "after 02:00 already collected today" {
					base.last = agentV3SessionCollectionDay(time.Now(), location)
				}
				var repo session.Repository = base
				if tt.marker {
					repo = agentSessionCatchUpMarkerRepository{base}
				}
				service, err := session.NewService(repo, files, session.Options{})
				require.NoError(t, err)
				s := startAgentV3SessionMaintenance(t.Context(), service, location)
				defer s.close()
				synctest.Wait()
				require.Equal(t, tt.wantCollections, base.collections.Load())
				base.mu.Lock()
				marks := append([]string(nil), base.marks...)
				base.mu.Unlock()
				require.Len(t, marks, tt.wantMarks)
				if tt.wantMarks > 0 {
					require.Equal(t, agentV3SessionCollectionDay(time.Now(), location), marks[0])
				}
				time.Sleep(time.Minute)
				synctest.Wait()
				require.Equal(t, tt.wantCollections, base.collections.Load(), "the minute recovery loop never collects")
				next, err := session.NextCollection(time.Now(), location)
				require.NoError(t, err)
				time.Sleep(time.Until(next) - time.Second)
				synctest.Wait()
				require.Equal(t, tt.wantCollections, base.collections.Load(), "no second collection before the next 02:00")
				time.Sleep(2 * time.Second)
				synctest.Wait()
				require.Equal(t, tt.wantCollections+1, base.collections.Load(), "the next collection runs at the following 02:00")
			})
		})
	}
}

func TestAgentV3SessionStartupCatchUpAfterSlowRecoveryCollectsOnce(t *testing.T) {
	f := newAgentSessionFixture(t)
	files, err := session.NewFileStore(t.TempDir())
	require.NoError(t, err)
	synctest.Test(t, func(t *testing.T) {
		location := time.FixedZone("slow-recovery", 2*3600-2*60)
		base := &agentSessionCatchUpRepository{Repository: f.repo, scope: f.scope(), firstDelay: 7 * time.Minute}
		service, err := session.NewService(agentSessionCatchUpMarkerRepository{base}, files, session.Options{})
		require.NoError(t, err)
		require.Equal(t, "01:58", time.Now().In(location).Format("15:04"))
		s := startAgentV3SessionMaintenance(t.Context(), service, location)
		defer s.close()
		time.Sleep(7*time.Minute + time.Second)
		synctest.Wait()
		require.Equal(t, int32(1), base.collections.Load(), "slow recovery crossing 02:00 triggers exactly one catch-up collection")
		time.Sleep(time.Minute)
		synctest.Wait()
		require.Equal(t, int32(1), base.collections.Load(), "no second collection right after the catch-up")
		next, err := session.NextCollection(time.Now(), location)
		require.NoError(t, err)
		time.Sleep(time.Until(next) - time.Second)
		synctest.Wait()
		require.Equal(t, int32(1), base.collections.Load())
		time.Sleep(2 * time.Second)
		synctest.Wait()
		require.Equal(t, int32(2), base.collections.Load(), "the next collection runs at tomorrow's 02:00")
	})
}

func TestAgentV3SessionStartupCatchUpCollectsExpiredDAG(t *testing.T) {
	f := newAgentSessionFixture(t)
	now := time.Date(2026, 10, 7, 10, 0, 0, 0, time.UTC)
	f.mini.SetTime(now)
	cfg := &config.AgentConfig{Name: "catch-up", ContextMode: "reply_chain"}
	f.compile(t, cfg, &scriptedToolModel{turns: [][]*schema.Message{{schema.AssistantMessage("expired answer", nil)}}})
	id := f.chat(t, cfg, sessionMessage(100, 7, 0, "expired input"), nil)
	node := f.node(t, id)
	f.mini.SetTime(now.Add(2 * time.Hour))
	afternoon := time.FixedZone("catch-up-afternoon", (14-time.Now().UTC().Hour())*3600)
	last, err := f.service.LastCollection(t.Context())
	require.NoError(t, err)
	require.Empty(t, last)
	s := startAgentV3SessionMaintenance(t.Context(), f.service, afternoon)
	t.Cleanup(s.close)
	require.Eventually(t, func() bool {
		published, err := f.repo.GetPublication(t.Context(), f.scope(), node.RunID)
		return err == nil && published == nil
	}, 3*time.Second, 10*time.Millisecond, "a process started after today's 02:00 collects the missed expiry once")
	require.Eventually(t, func() bool {
		last, err := f.service.LastCollection(t.Context())
		return err == nil && last == agentV3SessionCollectionDay(time.Now(), afternoon)
	}, 3*time.Second, 10*time.Millisecond)
}

func TestAgentV3SessionCollectionRetriesAfterFailure(t *testing.T) {
	f := newAgentSessionFixture(t)
	tests := []struct {
		name   string
		offset time.Duration
	}{
		{"catch-up failure retries in an hour", 3 * time.Hour},
		{"scheduled failure retries at 03:00", 90 * time.Minute},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			files, err := session.NewFileStore(t.TempDir())
			require.NoError(t, err)
			synctest.Test(t, func(t *testing.T) {
				location := time.FixedZone("retry", int(tt.offset/time.Second))
				base := &agentSessionCatchUpRepository{Repository: f.repo, scope: f.scope()}
				base.claimFails.Store(1)
				service, err := session.NewService(agentSessionCatchUpMarkerRepository{base}, files, session.Options{})
				require.NoError(t, err)
				s := startAgentV3SessionMaintenance(t.Context(), service, location)
				defer s.close()
				synctest.Wait()
				if tt.offset > 2*time.Hour {
					require.Equal(t, int32(1), base.collections.Load(), "the catch-up attempt failed")
				} else {
					require.Zero(t, base.collections.Load())
					time.Sleep(30 * time.Minute)
					synctest.Wait()
					require.Equal(t, int32(1), base.collections.Load(), "the 02:00 attempt failed")
				}
				base.mu.Lock()
				require.Empty(t, base.marks, "a failed collection does not mark the day")
				base.mu.Unlock()
				time.Sleep(time.Hour - time.Second)
				synctest.Wait()
				require.Equal(t, int32(1), base.collections.Load(), "no retry before the hour is up")
				time.Sleep(2 * time.Second)
				synctest.Wait()
				require.Equal(t, int32(2), base.collections.Load(), "retry runs an hour after the failure")
				base.mu.Lock()
				require.Equal(t, []string{agentV3SessionCollectionDay(time.Now(), location)}, base.marks)
				base.mu.Unlock()
				next, err := session.NextCollection(time.Now(), location)
				require.NoError(t, err)
				time.Sleep(time.Until(next) - time.Second)
				synctest.Wait()
				require.Equal(t, int32(2), base.collections.Load(), "success resumes the 02:00 schedule")
				time.Sleep(2 * time.Second)
				synctest.Wait()
				require.Equal(t, int32(3), base.collections.Load())
			})
		})
	}
}
