package cronjob

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestParseUntil(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ raw, zone, want string }{
		{"2026-10-31T09:00:00+08:00", "Asia/Shanghai", "2026-10-31T01:00:00Z"},
		{"2026-10-31 09:00", "Asia/Shanghai", "2026-10-31T01:00:00Z"},
		{"2026-10-31T01:00:00.123456789Z", "Asia/Shanghai", "2026-10-31T01:00:00.123456789Z"},
		{"2026-10-31T01:00:00Z", "America/New_York", "2026-10-31T01:00:00Z"},
	} {
		t.Run(tc.raw+tc.zone, func(t *testing.T) {
			got, err := ParseUntil(tc.raw, tc.zone, now)
			require.NoError(t, err)
			require.Equal(t, tc.want, got.Format(time.RFC3339Nano))
			require.Equal(t, time.UTC, got.Location())
		})
	}
	for _, raw := range []string{"", "1h", "09:00", "3", "@daily 09:00", "2026-10-31T01:00:00", "2026-02-30 09:00", "2026-10-31 25:00", "2026-10-15T00:00:00Z", "2026-10-14T23:59:59Z", "2026-10-31T01:00:00+24:00", "2026-10-31T01:00:00+01:60", "2026-10-31T1:00:00Z"} {
		_, err := ParseUntil(raw, "UTC", now)
		require.ErrorIs(t, err, ErrInvalidArgument, raw)
	}
	_, err := ParseUntil("2026-10-31 09:00", "Unknown/Zone", now)
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestUntilDSTGapAndFold(t *testing.T) {
	_, err := ParseUntil("2026-03-08 02:30", "America/New_York", time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrInvalidArgument)
	for _, tc := range []struct{ now, want string }{
		{"2026-11-01T05:00:00Z", "2026-11-01T05:30:00Z"},
		{"2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
		{"2026-11-01T06:00:00Z", "2026-11-01T06:30:00Z"},
	} {
		now, err := time.Parse(time.RFC3339, tc.now)
		require.NoError(t, err)
		got, err := ParseUntil("2026-11-01 01:30", "America/New_York", now)
		require.NoError(t, err)
		require.Equal(t, tc.want, got.Format(time.RFC3339))
	}
	_, err = ParseUntil("2026-11-01 01:30", "America/New_York", time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrInvalidArgument)
}

func TestScheduleAfterRunUntil(t *testing.T) {
	now := time.Date(2026, 10, 15, 0, 0, 10, 123456789, time.UTC)
	for _, expression := range []string{"* * * * *", "@every 90s", "@at 2026-10-15T01:00:00Z"} {
		s, err := Parse(expression, "UTC")
		require.NoError(t, err)
		unbounded, hasNext, err := s.AfterRun(now)
		require.NoError(t, err)
		if !hasNext {
			next, ok, nextErr := s.AfterRunUntil(now, now.Add(24*time.Hour))
			require.NoError(t, nextErr)
			require.False(t, ok)
			require.True(t, next.IsZero())
			continue
		}
		for _, delta := range []time.Duration{-time.Nanosecond, 0, time.Nanosecond} {
			next, ok, nextErr := s.AfterRunUntil(now, unbounded.Add(delta))
			require.NoError(t, nextErr)
			require.Equal(t, delta >= 0, ok)
			if ok {
				require.Equal(t, unbounded, next)
			} else {
				require.True(t, next.IsZero())
			}
		}
	}
	require.True(t, AllowsScheduledAt(now, now))
	require.False(t, AllowsScheduledAt(now, now.Add(time.Nanosecond)))
}
