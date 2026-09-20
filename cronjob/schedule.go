package cronjob

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

const searchYears = 8

type scheduleFieldError string

func (e scheduleFieldError) Error() string {
	return string(e)
}

type cronField struct {
	min      int
	max      int
	allowed  []bool
	wildcard bool
}

// Schedule is a parsed five-field cron schedule in a named timezone.
type Schedule struct {
	expression string
	timezone   string
	location   *time.Location
	minute     cronField
	hour       cronField
	day        cronField
	month      cronField
	weekday    cronField
}

// Parse validates and parses a five-field cron expression and timezone.
func Parse(expression, timezone string) (*Schedule, error) {
	parts := strings.Fields(expression)
	if len(parts) != 5 {
		return nil, NewError(CodeInvalidArgument, "cron must contain exactly five fields")
	}
	location, err := time.LoadLocation(timezone)
	if err != nil {
		return nil, NewError(CodeInvalidArgument, fmt.Sprintf("invalid timezone %q", timezone))
	}

	fields := make([]cronField, 5)
	specs := []struct {
		min int
		max int
		dow bool
	}{{0, 59, false}, {0, 23, false}, {1, 31, false}, {1, 12, false}, {0, 7, true}}
	for i, spec := range specs {
		field, parseErr := parseCronField(parts[i], spec.min, spec.max, spec.dow)
		if parseErr != nil {
			return nil, NewError(CodeInvalidArgument, fmt.Sprintf("invalid cron field %d: %v", i+1, parseErr))
		}
		fields[i] = field
	}

	return &Schedule{
		expression: strings.Join(parts, " "),
		timezone:   timezone,
		location:   location,
		minute:     fields[0],
		hour:       fields[1],
		day:        fields[2],
		month:      fields[3],
		weekday:    fields[4],
	}, nil
}

// Expression returns the normalized five-field cron expression.
func (s *Schedule) Expression() string {
	return s.expression
}

// Timezone returns the schedule's named timezone.
func (s *Schedule) Timezone() string {
	return s.timezone
}

// Next returns the first matching instant strictly after the supplied time.
func (s *Schedule) Next(after time.Time) (time.Time, error) {
	if s == nil || s.location == nil {
		return time.Time{}, NewError(CodeInvalidArgument, "schedule is not initialized")
	}

	limit := after.AddDate(searchYears, 0, 0)
	localStart := after.In(s.location)
	localLimit := limit.In(s.location)
	date := time.Date(localStart.Year(), localStart.Month(), localStart.Day(), 0, 0, 0, 0, time.UTC)
	lastDate := time.Date(localLimit.Year(), localLimit.Month(), localLimit.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1)
	hours := s.hour.values()
	minutes := s.minute.values()

	for !date.After(lastDate) {
		year, month, day := date.Date()
		if s.month.matches(int(month)) && s.matchesDay(year, month, day) {
			offsets := timezoneOffsets(s.location, year, month, day)
			candidate := earliestDateInstant(s.location, offsets, year, month, day, hours, minutes, after, limit)
			if !candidate.IsZero() {
				return candidate, nil
			}
		}
		date = date.AddDate(0, 0, 1)
	}
	return time.Time{}, NewError(CodeInvalidArgument, "cron has no matching time within eight years")
}

func (s *Schedule) matchesDay(year int, month time.Month, day int) bool {
	domMatches := s.day.matches(day)
	weekday := int(time.Date(year, month, day, 0, 0, 0, 0, time.UTC).Weekday())
	dowMatches := s.weekday.matches(weekday)
	if s.day.wildcard || s.weekday.wildcard {
		return domMatches && dowMatches
	}
	return domMatches || dowMatches
}

func parseCronField(raw string, minValue, maxValue int, dayOfWeek bool) (cronField, error) {
	storageMax := maxValue
	if dayOfWeek {
		storageMax = 6
	}
	field := cronField{min: minValue, max: storageMax, allowed: make([]bool, storageMax+1)}
	field.wildcard = strings.HasPrefix(raw, "*")
	if raw == "" {
		return field, scheduleFieldError("empty field")
	}

	for _, item := range strings.Split(raw, ",") {
		if item == "" {
			return field, scheduleFieldError("empty list item")
		}
		base, step, err := parseStep(item)
		if err != nil {
			return field, err
		}
		if step > maxValue-minValue+1 {
			return field, scheduleFieldError(fmt.Sprintf("step %d exceeds field range", step))
		}

		start, end := minValue, maxValue
		switch {
		case base == "*":
		case strings.Contains(base, "-"):
			bounds := strings.Split(base, "-")
			if len(bounds) != 2 {
				return field, scheduleFieldError(fmt.Sprintf("invalid range %q", base))
			}
			start, err = parseCronNumber(bounds[0], minValue, maxValue)
			if err != nil {
				return field, err
			}
			end, err = parseCronNumber(bounds[1], minValue, maxValue)
			if err != nil {
				return field, err
			}
			if start > end {
				return field, scheduleFieldError(fmt.Sprintf("descending range %q", base))
			}
		default:
			if step != 1 {
				return field, scheduleFieldError("step requires * or a range")
			}
			start, err = parseCronNumber(base, minValue, maxValue)
			if err != nil {
				return field, err
			}
			end = start
		}

		for value := start; value <= end; value += step {
			if dayOfWeek && value == 7 {
				field.allowed[0] = true
			} else {
				field.allowed[value] = true
			}
		}
	}
	return field, nil
}

func parseStep(item string) (string, int, error) {
	parts := strings.Split(item, "/")
	if len(parts) > 2 || parts[0] == "" {
		return "", 0, scheduleFieldError(fmt.Sprintf("invalid step %q", item))
	}
	if len(parts) == 1 {
		return parts[0], 1, nil
	}
	step, err := strconv.Atoi(parts[1])
	if err != nil || step <= 0 {
		return "", 0, scheduleFieldError(fmt.Sprintf("invalid step %q", item))
	}
	return parts[0], step, nil
}

func parseCronNumber(raw string, minValue, maxValue int) (int, error) {
	value, err := strconv.Atoi(raw)
	if err != nil || value < minValue || value > maxValue {
		return 0, scheduleFieldError(fmt.Sprintf("value %q outside %d-%d", raw, minValue, maxValue))
	}
	return value, nil
}

func (f cronField) matches(value int) bool {
	return value >= 0 && value < len(f.allowed) && f.allowed[value]
}

func (f cronField) values() []int {
	values := make([]int, 0, len(f.allowed))
	for value := f.min; value <= f.max; value++ {
		if f.matches(value) {
			values = append(values, value)
		}
	}
	return values
}

func timezoneOffsets(location *time.Location, year int, month time.Month, day int) []int {
	center := time.Date(year, month, day, 12, 0, 0, 0, time.UTC)
	seen := make(map[int]struct{}, 2)
	for hour := -48; hour <= 48; hour += 6 {
		_, offset := center.Add(time.Duration(hour) * time.Hour).In(location).Zone()
		seen[offset] = struct{}{}
	}
	offsets := make([]int, 0, len(seen))
	for offset := range seen {
		offsets = append(offsets, offset)
	}
	sort.Ints(offsets)
	return offsets
}

func earliestDateInstant(location *time.Location, offsets []int, year int, month time.Month, day int, hours, minutes []int, after, limit time.Time) time.Time {
	var earliest time.Time
	for _, offset := range offsets {
		candidate := firstDateInstantForOffset(location, offset, year, month, day, hours, minutes, after, limit)
		if !candidate.IsZero() && (earliest.IsZero() || candidate.Before(earliest)) {
			earliest = candidate
		}
	}
	return earliest
}

func firstDateInstantForOffset(location *time.Location, offset, year int, month time.Month, day int, hours, minutes []int, after, limit time.Time) time.Time {
	for _, hour := range hours {
		for _, minute := range minutes {
			wall := time.Date(year, month, day, hour, minute, 0, 0, time.UTC)
			candidate := wall.Add(-time.Duration(offset) * time.Second)
			local := candidate.In(location)
			if local.Year() != year || local.Month() != month || local.Day() != day || local.Hour() != hour || local.Minute() != minute {
				continue
			}
			if candidate.After(after) && !candidate.After(limit) {
				return candidate
			}
		}
	}
	return time.Time{}
}
