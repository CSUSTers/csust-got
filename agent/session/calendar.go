package session

import (
	"errors"
	"time"
)

var errCollectionTime = errors.New("cannot find next local collection time")

// NextCollection defaults to time.Local and 02:00. Missing DST hours use the first
// existing instant after 02:00; repeated hours use the first occurrence only.
func NextCollection(after time.Time, location *time.Location) (time.Time, error) {
	if location == nil {
		location = time.Local
	}
	local := after.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	for range 3 {
		start := day.Add(-6 * time.Hour)
		var candidate time.Time
		for minute := range 36 * 60 {
			t := start.Add(time.Duration(minute) * time.Minute)
			civil := t.In(location)
			if civil.Year() == day.Year() && civil.YearDay() == day.YearDay() && civil.Hour() >= 2 {
				candidate = t
				break
			}
		}
		if !candidate.IsZero() && candidate.After(after) {
			return candidate, nil
		}
		day = day.AddDate(0, 0, 1)
	}
	return time.Time{}, errCollectionTime
}
