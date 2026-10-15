package cronjob

import (
	"regexp"
	"strings"
	"time"
)

var untilInstantPattern = regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d{1,9})?(Z|[+-](0\d|1\d|2[0-3]):[0-5]\d)$`)

// ParseUntil resolves an absolute cutoff using the task's captured timezone.
func ParseUntil(raw, timezone string, now time.Time) (time.Time, error) {
	location, err := loadScheduleLocation(timezone)
	if err != nil {
		return time.Time{}, err
	}
	var until time.Time
	if strings.Contains(raw, "T") {
		if !untilInstantPattern.MatchString(raw) {
			return time.Time{}, NewError(CodeInvalidArgument, "until instant must be RFC3339 with offset")
		}
		until, err = time.Parse(time.RFC3339Nano, raw)
	} else if len(raw) == 16 && raw[10] == ' ' {
		var date time.Time
		date, err = time.Parse("2006-01-02", raw[:10])
		if err == nil && date.Format("2006-01-02") == raw[:10] {
			var minute, hour int
			minute, hour, err = parseWallTime(raw[11:])
			if err == nil {
				until = matchingWallInstant(location, date.Year(), date.Month(), date.Day(), hour, minute, now)
			}
		}
	}
	if err != nil || !until.After(now) {
		return time.Time{}, NewError(CodeInvalidArgument, "until must be a future RFC3339 instant with offset or an existing local YYYY-MM-DD HH:MM")
	}
	return until.UTC(), nil
}

// AllowsScheduledAt compares domain instants without losing sub-millisecond precision.
func AllowsScheduledAt(until, scheduledAt time.Time) bool {
	return until.IsZero() || !scheduledAt.After(until)
}

// AfterRunUntil clips the next ordinary occurrence to the optional inclusive cutoff.
func (s *Schedule) AfterRunUntil(after, until time.Time) (time.Time, bool, error) {
	next, hasNext, err := s.AfterRun(after)
	if err != nil {
		return time.Time{}, false, err
	}
	if !hasNext || !AllowsScheduledAt(until, next) {
		return time.Time{}, false, nil
	}
	return next, true, nil
}
