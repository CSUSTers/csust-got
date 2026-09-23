package cronjob

import (
	"fmt"
	"strconv"
	"strings"
	"time"
)

type scheduleKind uint8

const (
	monthAlias   = "@month"
	monthlyAlias = "@monthly"
)

const (
	scheduleCron scheduleKind = iota
	scheduleOnce
	scheduleEvery
)

func loadScheduleLocation(timezone string) (*time.Location, error) {
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, NewError(CodeInvalidArgument, fmt.Sprintf("invalid timezone %q", timezone))
	}
	return location, nil
}

func parseScheduleDuration(raw string) (time.Duration, error) {
	duration, err := time.ParseDuration(raw)
	if err != nil || duration <= 0 || duration%time.Millisecond != 0 {
		return 0, NewError(CodeInvalidArgument, "duration must be positive whole milliseconds within Go duration range")
	}
	return duration, nil
}

// Resolve turns user-facing schedule syntax into a canonical schedule with a future first run.
func Resolve(expression, timezone string, now time.Time) (*Schedule, error) {
	parts := strings.Fields(expression)
	if len(parts) == 0 {
		return nil, NewError(CodeInvalidArgument, "schedule is empty")
	}
	var schedule *Schedule
	var err error
	switch parts[0] {
	case "@at":
		schedule, err = resolveAt(parts, timezone, now)
	case "@daily", monthAlias, monthlyAlias, "@week", "@weekly":
		canonical, aliasErr := resolveAlias(parts)
		if aliasErr != nil {
			return nil, aliasErr
		}
		schedule, err = Parse(canonical, timezone)
	default:
		schedule, err = Parse(expression, timezone)
	}
	if err != nil {
		return nil, err
	}
	if _, err = schedule.Next(now); err != nil {
		return nil, err
	}
	return schedule, nil
}

func resolveAt(parts []string, timezone string, now time.Time) (*Schedule, error) {
	location, err := loadScheduleLocation(timezone)
	if err != nil {
		return nil, err
	}
	var deadline time.Time
	switch {
	case len(parts) == 2:
		raw := parts[1]
		switch {
		case isWallTime(raw):
			minute, hour, parseErr := parseWallTime(raw)
			if parseErr != nil {
				return nil, parseErr
			}
			wall, cronErr := Parse(fmt.Sprintf("%d %d * * *", minute, hour), timezone)
			if cronErr != nil {
				return nil, cronErr
			}
			deadline, err = wall.Next(now)
		case strings.Contains(raw, "T"):
			deadline, err = time.Parse(time.RFC3339Nano, raw)
			if err != nil {
				return nil, NewError(CodeInvalidArgument, "@at instant must be RFC3339 with offset")
			}
		default:
			var duration time.Duration
			duration, err = parseScheduleDuration(raw)
			if err != nil {
				return nil, err
			}
			deadline = now.Add(duration)
		}
	case len(parts) == 3 && parts[1] == "tomorro":
		minute, hour, parseErr := parseWallTime(parts[2])
		if parseErr != nil {
			return nil, parseErr
		}
		local := now.In(location)
		date := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
		deadline = matchingWallInstant(location, date.Year(), date.Month(), date.Day(), hour, minute, now)
	case len(parts) == 3:
		minute, hour, parseErr := parseWallTime(parts[2])
		if parseErr != nil {
			return nil, parseErr
		}
		date, dateErr := time.Parse("2006-01-02", parts[1])
		if dateErr != nil || date.Format("2006-01-02") != parts[1] {
			return nil, NewError(CodeInvalidArgument, "@at local date must be YYYY-MM-DD")
		}
		deadline = matchingWallInstant(location, date.Year(), date.Month(), date.Day(), hour, minute, now)
	default:
		return nil, NewError(CodeInvalidArgument, "invalid @at schedule")
	}
	if err != nil {
		return nil, err
	}
	if !deadline.After(now) {
		return nil, NewError(CodeInvalidArgument, "@at deadline must exist and be in the future")
	}
	canonical := "@at " + deadline.UTC().Format(time.RFC3339Nano)
	return Parse(canonical, timezone)
}

func matchingWallInstant(location *time.Location, year int, month time.Month, day, hour, minute int, after time.Time) time.Time {
	var earliest time.Time
	for _, offset := range timezoneOffsets(location, year, month, day) {
		wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
		candidate := wall.Add(-time.Duration(offset) * time.Second)
		local := candidate.In(location)
		if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
			continue
		}
		if candidate.After(after) && (earliest.IsZero() || candidate.Before(earliest)) {
			earliest = candidate
		}
	}
	return earliest
}

func isWallTime(raw string) bool {
	return len(raw) == 5 && raw[2] == ':'
}

func parseWallTime(raw string) (minute, hour int, err error) {
	if !isWallTime(raw) || !asciiDigits(raw[:2]) || !asciiDigits(raw[3:]) {
		return 0, 0, NewError(CodeInvalidArgument, "time must be HH:MM in 24-hour format")
	}
	hour, _ = strconv.Atoi(raw[:2])
	minute, _ = strconv.Atoi(raw[3:])
	if hour > 23 || minute > 59 {
		return 0, 0, NewError(CodeInvalidArgument, "time must be HH:MM in 24-hour format")
	}
	return minute, hour, nil
}

func asciiDigits(raw string) bool {
	if raw == "" {
		return false
	}
	for i := range len(raw) {
		if raw[i] < '0' || raw[i] > '9' {
			return false
		}
	}
	return true
}

func resolveAlias(parts []string) (string, error) {
	if len(parts) != 2 && len(parts) != 3 {
		return "", NewError(CodeInvalidArgument, "invalid recurring schedule")
	}
	var wall string
	var selector string
	switch parts[0] {
	case "@daily":
		if len(parts) != 2 {
			return "", NewError(CodeInvalidArgument, "@daily requires HH:MM")
		}
		wall = parts[1]
	case monthAlias, monthlyAlias, "@week", "@weekly":
		if len(parts) != 3 || !asciiDigits(parts[1]) {
			return "", NewError(CodeInvalidArgument, "recurring schedule requires a numeric day and HH:MM")
		}
		selector = parts[1]
		wall = parts[2]
	}
	minute, hour, err := parseWallTime(wall)
	if err != nil {
		return "", err
	}
	day, weekday := "*", "*"
	if selector != "" {
		value, parseErr := strconv.Atoi(selector)
		if parseErr != nil {
			return "", NewError(CodeInvalidArgument, "day is out of range")
		}
		if parts[0] == monthAlias || parts[0] == monthlyAlias {
			if value < 1 || value > 31 {
				return "", NewError(CodeInvalidArgument, "day must be between 1 and 31")
			}
			day = strconv.Itoa(value)
		} else {
			if value > 7 {
				return "", NewError(CodeInvalidArgument, "weekday must be between 0 and 7")
			}
			weekday = strconv.Itoa(value)
		}
	}
	return fmt.Sprintf("%d %d %s * %s", minute, hour, day, weekday), nil
}
