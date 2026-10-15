package session

import (
	"testing"
	"time"
	_ "time/tzdata"
)

func TestNextCollectionCalendar(t *testing.T) {
	zone := time.FixedZone("local", 8*60*60)
	after := time.Date(2026, 10, 7, 1, 59, 0, 0, zone)
	next, err := NextCollection(after, zone)
	if err != nil || !next.Equal(time.Date(2026, 10, 7, 2, 0, 0, 0, zone)) {
		t.Fatalf("next local 02:00: %v %v", next, err)
	}
	next, err = NextCollection(next, zone)
	if err != nil || next.Day() != 8 {
		t.Fatalf("next date: %v %v", next, err)
	}
	berlin, err := time.LoadLocation("Europe/Berlin")
	if err != nil {
		t.Fatal(err)
	}
	next, err = NextCollection(time.Date(2026, 3, 29, 0, 0, 0, 0, berlin), berlin)
	if err != nil || next.Hour() != 3 {
		t.Fatalf("skipped 02:00: %v %v", next, err)
	}
	first, err := NextCollection(time.Date(2026, 10, 25, 0, 0, 0, 0, berlin), berlin)
	if err != nil || first.Hour() != 2 {
		t.Fatalf("first repeated 02:00: %v %v", first, err)
	}
	next, err = NextCollection(first.Add(30*time.Minute), berlin)
	if err != nil || next.Day() != 26 {
		t.Fatalf("must not repeat the same civil date: %v %v", next, err)
	}
	_, firstOffset := first.Zone()
	if firstOffset != 2*60*60 {
		t.Fatalf("expected first fold occurrence, offset %d", firstOffset)
	}
	localNext, err := NextCollection(after, nil)
	if err != nil || localNext.Location() != time.Local || localNext.Hour() != 2 {
		t.Fatalf("local default: %v %v", localNext, err)
	}
}
