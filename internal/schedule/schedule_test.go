package schedule

import (
	"testing"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

func at(t *testing.T, s string) time.Time {
	t.Helper()
	tm, err := time.Parse("2006-01-02 15:04 MST", s)
	if err != nil {
		t.Fatal(err)
	}
	return tm
}

func TestEmptyIsAlwaysOpen(t *testing.T) {
	s, err := Parse(config.RunWindows{})
	if err != nil {
		t.Fatal(err)
	}
	if !s.Open(time.Now()) {
		t.Error("empty schedule should be open")
	}
	var nilS *Schedule
	if !nilS.Open(time.Now()) {
		t.Error("nil schedule should be open")
	}
}

func TestDaytimeWindow(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "UTC", Windows: []config.WindowConfig{{Days: []string{"mon", "tue"}, Start: "09:00", End: "17:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-21 is a Monday.
	if !s.Open(at(t, "2026-09-21 12:00 UTC")) {
		t.Error("Monday noon should be open")
	}
	if s.Open(at(t, "2026-09-21 17:00 UTC")) {
		t.Error("end is exclusive")
	}
	if s.Open(at(t, "2026-09-23 12:00 UTC")) {
		t.Error("Wednesday should be closed")
	}
}

func TestOvernightWindowSpansMidnight(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "UTC", Windows: []config.WindowConfig{{Days: []string{"friday"}, Start: "22:00", End: "06:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 2026-09-25 is a Friday.
	if !s.Open(at(t, "2026-09-25 23:00 UTC")) {
		t.Error("Friday 23:00 should be open")
	}
	if !s.Open(at(t, "2026-09-26 03:00 UTC")) {
		t.Error("Saturday 03:00 belongs to Friday's window")
	}
	if s.Open(at(t, "2026-09-26 23:00 UTC")) {
		t.Error("Saturday 23:00 should be closed")
	}
	if s.Open(at(t, "2026-09-25 12:00 UTC")) {
		t.Error("Friday noon should be closed")
	}
}

func TestTimezoneApplied(t *testing.T) {
	s, err := Parse(config.RunWindows{Timezone: "America/New_York", Windows: []config.WindowConfig{{Start: "22:00", End: "23:00"}}})
	if err != nil {
		t.Fatal(err)
	}
	// 02:30 UTC on 2026-09-20 is 22:30 EDT on 2026-09-19.
	if !s.Open(at(t, "2026-09-20 02:30 UTC")) {
		t.Error("should be open in New York evening")
	}
}

func TestParseErrors(t *testing.T) {
	cases := []config.RunWindows{
		{Timezone: "Nope/Nowhere"},
		{Windows: []config.WindowConfig{{Start: "9", End: "17:00"}}},
		{Windows: []config.WindowConfig{{Days: []string{"funday"}, Start: "09:00", End: "17:00"}}},
	}
	for i, c := range cases {
		if _, err := Parse(c); err == nil {
			t.Errorf("case %d: expected error", i)
		}
	}
}
