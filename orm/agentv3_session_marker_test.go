package orm

import (
	"testing"

	"csust-got/agent/session"

	"github.com/stretchr/testify/require"
)

func TestAgentV3SessionCollectionMarkerRoundTrip(t *testing.T) {
	f := newSessionFixture(t, session.Options{})
	last, err := f.repo.LastCollection(t.Context())
	require.NoError(t, err)
	require.Empty(t, last)
	require.ErrorIs(t, f.repo.MarkCollection(t.Context(), " "), session.ErrCorrupt)
	require.NoError(t, f.repo.MarkCollection(t.Context(), "2026-10-08"))
	last, err = f.repo.LastCollection(t.Context())
	require.NoError(t, err)
	require.Equal(t, "2026-10-08", last)
	require.NoError(t, f.service.MarkCollection(t.Context(), "2026-10-09"))
	last, err = f.service.LastCollection(t.Context())
	require.NoError(t, err)
	require.Equal(t, "2026-10-09", last)
	require.Contains(t, f.mr.Keys(), f.repo.base+sessionLastCollectionKey, "the marker lives in the layout's global area, not under a scope")
	wrongType, err := NewAgentV3SessionRepository(f.repo.client, "session-test:")
	require.NoError(t, err)
	require.NoError(t, f.repo.client.Del(t.Context(), f.repo.base+sessionLastCollectionKey).Err())
	require.NoError(t, f.repo.client.SAdd(t.Context(), f.repo.base+sessionLastCollectionKey, "x").Err())
	_, err = wrongType.LastCollection(t.Context())
	require.ErrorIs(t, err, session.ErrCorrupt)
}
