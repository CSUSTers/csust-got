package cronjob

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestScheduleC02RelativeAtIsPersistedAsAbsolute(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.FixedZone("+08", 8*3600))
	for _, tt := range []struct{ input, canonical string }{
		{"@at 90s", "@at 2026-09-23T02:01:30Z"},
		{"@at 1h30m", "@at 2026-09-23T03:30:00Z"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			s, err := Resolve(tt.input, "Asia/Shanghai", now)
			require.NoError(t, err)
			assert.True(t, s.IsOnce())
			assert.Equal(t, tt.canonical, s.Expression())
			persisted, err := Parse(s.Expression(), s.Timezone())
			require.NoError(t, err)
			deadline, err := persisted.Next(now.Add(30 * time.Second))
			require.NoError(t, err)
			assert.Equal(t, tt.canonical, "@at "+deadline.Format(time.RFC3339Nano))
		})
	}
}

func TestScheduleC03BareAtWallTimeFindsNextExistingDay(t *testing.T) {
	now := time.Date(2026, 9, 23, 10, 0, 0, 0, time.FixedZone("+08", 8*3600))
	for _, tt := range []struct{ input, want string }{
		{"@at 10:01", "2026-09-23T02:01:00Z"},
		{"@at 10:00", "2026-09-24T02:00:00Z"},
		{"@at 09:59", "2026-09-24T01:59:00Z"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			s, err := Resolve(tt.input, "Asia/Shanghai", now)
			require.NoError(t, err)
			assert.Equal(t, "@at "+tt.want, s.Expression())
		})
	}
}

func TestScheduleC04TomorroUsesLocalCalendarDate(t *testing.T) {
	for _, tt := range []struct{ now, want string }{
		{"2026-09-23T10:00:00+08:00", "2026-09-24T01:00:00Z"},
		{"2026-01-31T23:30:00+08:00", "2026-02-01T01:00:00Z"},
		{"2026-12-31T23:30:00+08:00", "2027-01-01T01:00:00Z"},
	} {
		now, err := time.Parse(time.RFC3339, tt.now)
		require.NoError(t, err)
		s, err := Resolve("@at tomorro 09:00", "Asia/Shanghai", now)
		require.NoError(t, err)
		assert.Equal(t, "@at "+tt.want, s.Expression())
	}
}

func TestScheduleC05AbsoluteAndLocalDateNormalizationAndFutureValidation(t *testing.T) {
	now := time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"@at 2026-09-23T02:01:00Z",
		"@at 2026-09-23T10:01:00+08:00",
		"@at 2026-09-23 10:01",
	} {
		s, err := Resolve(input, "Asia/Shanghai", now)
		require.NoError(t, err, input)
		assert.Equal(t, "@at 2026-09-23T02:01:00Z", s.Expression())
		assert.Equal(t, "Asia/Shanghai", s.Timezone())
	}
	for _, input := range []string{
		"@at 2026-02-30 10:01", "@at 2026-09-23", "@at 2026-09-23 9:01",
		"@at 2026-09-23T02:00:00Z", "@at 2026-09-23T01:59:59Z",
		"@at 2026-09-23 10:00", "@at 0s", "@at -1s",
	} {
		_, err := Resolve(input, "Asia/Shanghai", now)
		require.ErrorIs(t, err, ErrInvalidArgument, input)
	}
}

func TestScheduleC06AliasesPreserveFiveFieldSemantics(t *testing.T) {
	now := time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC)
	for _, tt := range []struct{ alias, cron string }{
		{"@daily 09:05", "5 9 * * *"},
		{"@month 31 09:05", "5 9 31 * *"},
		{"@monthly 31 09:05", "5 9 31 * *"},
		{"@week 0 09:05", "5 9 * * 0"},
		{"@weekly 7 09:05", "5 9 * * 7"},
	} {
		t.Run(tt.alias, func(t *testing.T) {
			alias, err := Resolve(tt.alias, "UTC", now)
			require.NoError(t, err)
			cron, err := Parse(tt.cron, "UTC")
			require.NoError(t, err)
			assert.Equal(t, cron.Expression(), alias.Expression())
			aliasNext, err := alias.Next(now)
			require.NoError(t, err)
			cronNext, err := cron.Next(now)
			require.NoError(t, err)
			assert.Equal(t, cronNext, aliasNext)
			if tt.alias == "@month 31 09:05" {
				assert.Equal(t, time.Date(2027, 3, 31, 9, 5, 0, 0, time.UTC), aliasNext)
			}
		})
	}
}

func TestScheduleWeekdayNamesCanonicalizeToNumericCalendarCron(t *testing.T) {
	now := time.Date(2026, 9, 23, 1, 5, 0, 0, time.UTC)
	for weekday, name := range []string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"} {
		short := name[:3]
		canonical := fmt.Sprintf("5 9 * * %d", weekday)
		for _, input := range []string{
			"@week " + short + " 09:05",
			"@weekly " + name + " 09:05",
			"@every " + strings.ToUpper(short) + " 09:05",
			"@week " + strings.ToUpper(name) + " 09:05",
		} {
			t.Run(input, func(t *testing.T) {
				alias, err := Resolve(input, "Asia/Shanghai", now)
				require.NoError(t, err)
				require.Equal(t, canonical, alias.Expression())
				require.Equal(t, "Asia/Shanghai", alias.Timezone())
				stored, err := Parse(alias.Expression(), alias.Timezone())
				require.NoError(t, err)
				for _, after := range []time.Time{now, now.Add(time.Minute), now.Add(8 * 24 * time.Hour)} {
					aliasNext, err := alias.Next(after)
					require.NoError(t, err)
					storedNext, err := stored.Next(after)
					require.NoError(t, err)
					require.Equal(t, storedNext, aliasNext)
					require.True(t, aliasNext.After(after))
				}
			})
		}
	}
}

func TestScheduleNamedSundayPreservesCronGapAndFold(t *testing.T) {
	for _, tt := range []struct {
		input, cron, after, first, second string
	}{
		{"@weekly Sunday 02:30", "30 2 * * 0", "2026-03-08T05:00:00Z", "2026-03-15T06:30:00Z", "2026-03-22T06:30:00Z"},
		{"@every SUN 01:30", "30 1 * * 0", "2026-11-01T04:00:00Z", "2026-11-01T05:30:00Z", "2026-11-01T06:30:00Z"},
	} {
		t.Run(tt.input, func(t *testing.T) {
			now, err := time.Parse(time.RFC3339, tt.after)
			require.NoError(t, err)
			alias, err := Resolve(tt.input, "America/New_York", now)
			require.NoError(t, err)
			require.Equal(t, tt.cron, alias.Expression())
			cron, err := Parse(tt.cron, "America/New_York")
			require.NoError(t, err)
			for _, want := range []string{tt.first, tt.second} {
				aliasNext, err := alias.Next(now)
				require.NoError(t, err)
				cronNext, err := cron.Next(now)
				require.NoError(t, err)
				require.Equal(t, cronNext, aliasNext)
				require.Equal(t, want, aliasNext.Format(time.RFC3339))
				now = aliasNext
			}
		})
	}
}

func TestScheduleC07EveryUsesFinishOrRecoveryTimeNotCreationAnchor(t *testing.T) {
	created := time.Date(2026, 9, 23, 2, 0, 0, 345678900, time.UTC)
	s, err := Resolve("@every 90s", "UTC", created)
	require.NoError(t, err)
	assert.Equal(t, "@every 1m30s", s.Expression())
	assert.False(t, s.IsOnce())
	for _, after := range []time.Time{created, created.Add(3 * time.Minute), created.Add(27 * time.Minute)} {
		next, hasNext, err := s.AfterRun(after)
		require.NoError(t, err)
		assert.True(t, hasNext)
		assert.Equal(t, after.Add(90*time.Second), next)
	}
	storedNext, err := s.Next(created)
	require.NoError(t, err)
	assert.Equal(t, created.Add(90*time.Second), storedNext) // prompt/report changes retain this persisted value
}

func TestScheduleC08C09RejectUnrecognizedOrOutOfRangeSyntax(t *testing.T) {
	now := time.Date(2026, 9, 23, 2, 0, 0, 0, time.UTC)
	for _, input := range []string{
		"@every 0s", "@every -1s", "@every 1ns", "@every 1.5ms", "@every 2562048h",
		"@at 1ns", "@at 10000000000000000000h", "@at 1d", "@daily 24:00", "@daily 09:60",
		"@month 0 09:00", "@monthly 32 09:00", "@weekly 8 09:00", "@weekly -1 09:00",
		"@daily", "@yearly", "@daily 09:00 extra", "@at tomorrow 09:00",
		"@every Wed", "@weekly Wed", "@week Wednesday", "@every 90s 09:00", "@every 3 09:00",
		"@week Weds 09:00", "@weekly Thurs 09:00", "@every 星期三 09:00", "@week ſaturday 09:00",
		"@weekly Mon,Tue 09:00", "@week Mon-Fri 09:00", "@every Wed 09:00 extra",
		"@weekly Wed 9:00", "@every Wed 24:00", "@week Wed 09:60",
		"@month Wednesday 09:05", "0 9 * * Wed",
		"@at tomorro 09:00 extra", "@at 09:60", "@at 2026-09-23 10:01 extra",
	} {
		t.Run(input, func(t *testing.T) {
			_, err := Resolve(input, "UTC", now)
			require.ErrorIs(t, err, ErrInvalidArgument)
		})
	}
	for _, input := range []string{"@at 90s", "@at 10:01", "@daily 09:00", "@weekly 1 09:00", "@every Wed 09:00"} {
		_, err := Parse(input, "UTC")
		require.ErrorIs(t, err, ErrInvalidArgument, input)
	}
}

func TestScheduleC10NewYorkGapFoldAndRecurringFold(t *testing.T) {
	for _, input := range []string{"@at 2026-03-08 02:30", "@at tomorro 02:30"} {
		now := time.Date(2026, 3, 7, 12, 0, 0, 0, time.UTC)
		_, err := Resolve(input, "America/New_York", now)
		require.ErrorIs(t, err, ErrInvalidArgument)
	}
	bare, err := Resolve("@at 02:30", "America/New_York", time.Date(2026, 3, 8, 5, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "@at 2026-03-09T06:30:00Z", bare.Expression())
	for _, tt := range []struct{ now, want string }{
		{"2026-11-01T04:00:00Z", "@at 2026-11-01T05:30:00Z"},
		{"2026-11-01T05:30:00Z", "@at 2026-11-01T06:30:00Z"},
	} {
		now, err := time.Parse(time.RFC3339, tt.now)
		require.NoError(t, err)
		s, err := Resolve("@at 2026-11-01 01:30", "America/New_York", now)
		require.NoError(t, err)
		assert.Equal(t, tt.want, s.Expression())
		next, hasNext, err := s.AfterRun(now.Add(24 * time.Hour))
		require.NoError(t, err)
		assert.False(t, hasNext)
		assert.True(t, next.IsZero())
	}
	recurring, err := Resolve("@daily 01:30", "America/New_York", time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	first, err := recurring.Next(time.Date(2026, 11, 1, 4, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	second, err := recurring.Next(first)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, second.Sub(first))
}

func TestScheduleC11LordHoweHalfHourGapFoldAndTomorro(t *testing.T) {
	_, err := Resolve("@at 2026-10-04 02:15", "Australia/Lord_Howe", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrInvalidArgument)
	_, err = Resolve("@at tomorro 02:15", "Australia/Lord_Howe", time.Date(2026, 10, 2, 15, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrInvalidArgument)
	for _, tt := range []struct{ now, want string }{
		{"2026-04-04T13:00:00Z", "@at 2026-04-04T14:45:00Z"},
		{"2026-04-04T14:45:00Z", "@at 2026-04-04T15:15:00Z"},
	} {
		now, err := time.Parse(time.RFC3339, tt.now)
		require.NoError(t, err)
		s, err := Resolve("@at 2026-04-05 01:45", "Australia/Lord_Howe", now)
		require.NoError(t, err)
		assert.Equal(t, tt.want, s.Expression())
	}
	tomorrow, err := Resolve("@at tomorro 03:00", "Australia/Lord_Howe", time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, "@at 2026-10-03T16:00:00Z", tomorrow.Expression())
}

func TestScheduleC12ExpiredAbsoluteCanBeReparsedInAnyTimezone(t *testing.T) {
	now := time.Date(2026, 9, 23, 2, 0, 0, 123456789, time.UTC)
	s, err := Resolve("@at 1s", "Asia/Shanghai", now)
	require.NoError(t, err)
	assert.Equal(t, "@at 2026-09-23T02:00:01.123456789Z", s.Expression())
	for _, zone := range []string{s.Timezone(), "UTC", "America/New_York"} {
		reloaded, err := Parse(s.Expression(), zone)
		require.NoError(t, err)
		assert.Equal(t, zone, reloaded.Timezone())
		assert.Equal(t, s.Expression(), reloaded.Expression())
		_, err = reloaded.Next(now.Add(time.Hour))
		require.ErrorIs(t, err, ErrInvalidArgument)
	}
}

func TestScheduleC13NanosecondClockIsNeverTruncated(t *testing.T) {
	now := time.Date(2026, 9, 23, 2, 0, 0, 999999999, time.UTC)
	for _, input := range []string{"@at 1ms", "@every 1ms"} {
		s, err := Resolve(input, "UTC", now)
		require.NoError(t, err)
		next, err := s.Next(now)
		require.NoError(t, err)
		assert.Equal(t, now.Add(time.Millisecond), next)
		assert.True(t, next.After(time.UnixMilli(next.UnixMilli())))
		if s.IsOnce() {
			assert.Equal(t, "@at 2026-09-23T02:00:01.000999999Z", s.Expression())
		}
	}
	for _, input := range []string{"@at 999999ns", "@every 999999ns"} {
		_, err := Resolve(input, "UTC", now)
		require.ErrorIs(t, err, ErrInvalidArgument)
	}
}

func TestScheduleC14OnlyOnceIsExhaustedWithoutAnError(t *testing.T) {
	s, err := Parse("@at 2020-01-01T00:00:00Z", "UTC")
	require.NoError(t, err)
	next, hasNext, err := s.AfterRun(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.False(t, hasNext)
	assert.True(t, next.IsZero())
	impossible, err := Parse("0 0 31 2 *", "UTC")
	require.NoError(t, err)
	next, hasNext, err = impossible.AfterRun(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	require.ErrorIs(t, err, ErrInvalidArgument)
	assert.False(t, hasNext)
	assert.True(t, next.IsZero())
}
