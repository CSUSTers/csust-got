package cronjob

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseRejectsInvalidExpressions(t *testing.T) {
	tests := []string{
		"* * * *",
		"* * * * * *",
		"@daily",
		"60 * * * *",
		"* 24 * * *",
		"* * 0 * *",
		"* * * 13 *",
		"* * * * 8",
		"* * * JAN *",
		"* * * * ?",
		"*/0 * * * *",
		"1-2/999999999999999999 * * * *",
		"1/2 * * * *",
		"5-2 * * * *",
		"1,,2 * * * *",
	}
	for _, expression := range tests {
		t.Run(expression, func(t *testing.T) {
			_, err := Parse(expression, "UTC")
			require.Error(t, err)
			assert.ErrorIs(t, err, ErrInvalidArgument)
		})
	}

	_, err := Parse("* * * * *", "Mars/Olympus")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArgument)
}

func TestScheduleNextSupportsListsRangesStepsAndStrictAfter(t *testing.T) {
	schedule, err := Parse("0,30 9-10/1 * * 1-5", "Asia/Shanghai")
	require.NoError(t, err)
	location, err := time.LoadLocation("Asia/Shanghai")
	require.NoError(t, err)

	after := time.Date(2026, time.September, 21, 9, 0, 0, 0, location)
	next, err := schedule.Next(after)
	require.NoError(t, err)
	assert.True(t, next.Equal(time.Date(2026, time.September, 21, 9, 30, 0, 0, location)))
	assert.Equal(t, "0,30 9-10/1 * * 1-5", schedule.Expression())
	assert.Equal(t, "Asia/Shanghai", schedule.Timezone())
}

func TestScheduleDayOfMonthAndWeekUseCronOR(t *testing.T) {
	schedule, err := Parse("0 8 13 * 5", "UTC")
	require.NoError(t, err)

	next, err := schedule.Next(time.Date(2026, time.February, 12, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.February, 13, 8, 0, 0, 0, time.UTC), next)

	next, err = schedule.Next(time.Date(2026, time.February, 13, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.February, 20, 8, 0, 0, 0, time.UTC), next)
}

func TestScheduleWildcardStepKeepsWildcardDaySemantics(t *testing.T) {
	schedule, err := Parse("0 8 */2 * 1", "UTC")
	require.NoError(t, err)

	next, err := schedule.Next(time.Date(2026, time.September, 6, 8, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.September, 7, 8, 0, 0, 0, time.UTC), next)

	next, err = schedule.Next(next)
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.September, 21, 8, 0, 0, 0, time.UTC), next)
}

func TestScheduleAcceptsSundaySeven(t *testing.T) {
	schedule, err := Parse("0 0 * * 7", "UTC")
	require.NoError(t, err)

	next, err := schedule.Next(time.Date(2026, time.September, 19, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2026, time.September, 20, 0, 0, 0, 0, time.UTC), next)
}

func TestScheduleFindsLeapDayAndBoundsImpossibleDates(t *testing.T) {
	leap, err := Parse("0 0 29 2 *", "UTC")
	require.NoError(t, err)
	next, err := leap.Next(time.Date(2025, time.March, 1, 0, 0, 0, 0, time.UTC))
	require.NoError(t, err)
	assert.Equal(t, time.Date(2028, time.February, 29, 0, 0, 0, 0, time.UTC), next)

	impossible, err := Parse("0 0 31 2 *", "UTC")
	require.NoError(t, err)
	_, err = impossible.Next(time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC))
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrInvalidArgument)
}

func TestScheduleSkipsNonexistentDSTMinute(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	schedule, err := Parse("30 2 * * *", "America/New_York")
	require.NoError(t, err)

	next, err := schedule.Next(time.Date(2026, time.March, 8, 0, 0, 0, 0, location))
	require.NoError(t, err)
	assert.True(t, next.Equal(time.Date(2026, time.March, 9, 2, 30, 0, 0, location)))
}

func TestScheduleReturnsBothRepeatedDSTMinutes(t *testing.T) {
	location, err := time.LoadLocation("America/New_York")
	require.NoError(t, err)
	schedule, err := Parse("30 1 * * *", "America/New_York")
	require.NoError(t, err)

	first, err := schedule.Next(time.Date(2026, time.November, 1, 0, 0, 0, 0, location))
	require.NoError(t, err)
	second, err := schedule.Next(first)
	require.NoError(t, err)
	assert.Equal(t, time.Hour, second.Sub(first))
	assert.Equal(t, 1, first.In(location).Hour())
	assert.Equal(t, 30, first.In(location).Minute())
	assert.Equal(t, 1, second.In(location).Hour())
	assert.Equal(t, 30, second.In(location).Minute())
}

func TestScheduleChoosesEarliestInstantAcrossNewYorkFold(t *testing.T) {
	schedule, err := Parse("* 1 * * *", "America/New_York")
	require.NoError(t, err)

	tests := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "first fold advances one local minute",
			after: time.Date(2026, time.November, 1, 5, 0, 0, 0, time.UTC),
			want:  time.Date(2026, time.November, 1, 5, 1, 0, 0, time.UTC),
		},
		{
			name:  "end of first fold enters second fold",
			after: time.Date(2026, time.November, 1, 5, 59, 0, 0, time.UTC),
			want:  time.Date(2026, time.November, 1, 6, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, nextErr := schedule.Next(tt.after)
			require.NoError(t, nextErr)
			assert.Equal(t, tt.want, next)
		})
	}
}

func TestScheduleChoosesEarliestInstantAcrossHalfHourFold(t *testing.T) {
	schedule, err := Parse("* 1 * * *", "Australia/Lord_Howe")
	require.NoError(t, err)

	tests := []struct {
		name  string
		after time.Time
		want  time.Time
	}{
		{
			name:  "first fold advances before second fold",
			after: time.Date(2026, time.April, 4, 14, 30, 0, 0, time.UTC),
			want:  time.Date(2026, time.April, 4, 14, 31, 0, 0, time.UTC),
		},
		{
			name:  "end of first fold enters repeated half hour",
			after: time.Date(2026, time.April, 4, 14, 59, 0, 0, time.UTC),
			want:  time.Date(2026, time.April, 4, 15, 0, 0, 0, time.UTC),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			next, nextErr := schedule.Next(tt.after)
			require.NoError(t, nextErr)
			assert.Equal(t, tt.want, next)
		})
	}
}

func TestDomainErrorsSupportErrorsIs(t *testing.T) {
	err := NewError(CodeConflict, "stale version")
	assert.True(t, errors.Is(err, ErrConflict))
	assert.False(t, errors.Is(err, ErrNotFound))
}
