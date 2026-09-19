// Package schedule evaluates configured run windows.
//
// Windows gate only the start of new work: a run in flight when a window
// closes finishes rather than aborting, because a safe stop beats a punctual
// one.
package schedule

import (
	"fmt"
	"strings"
	"time"

	"github.com/thomasmeadows/hivedispatch/internal/config"
)

type window struct {
	days  map[time.Weekday]bool // nil = every day
	start int                   // minutes since midnight
	end   int                   // exclusive
}

// Schedule is a parsed set of run windows.
type Schedule struct {
	loc     *time.Location
	windows []window
}

var dayNames = map[string]time.Weekday{
	"sun": time.Sunday, "sunday": time.Sunday,
	"mon": time.Monday, "monday": time.Monday,
	"tue": time.Tuesday, "tuesday": time.Tuesday,
	"wed": time.Wednesday, "wednesday": time.Wednesday,
	"thu": time.Thursday, "thursday": time.Thursday,
	"fri": time.Friday, "friday": time.Friday,
	"sat": time.Saturday, "saturday": time.Saturday,
}

// Parse validates and compiles cfg.
func Parse(cfg config.RunWindows) (*Schedule, error) {
	loc := time.Local
	if cfg.Timezone != "" {
		l, err := time.LoadLocation(cfg.Timezone)
		if err != nil {
			return nil, fmt.Errorf("schedule: timezone %q: %w", cfg.Timezone, err)
		}
		loc = l
	}
	s := &Schedule{loc: loc}
	for i, w := range cfg.Windows {
		start, err := minutes(w.Start)
		if err != nil {
			return nil, fmt.Errorf("schedule: window %d start: %w", i, err)
		}
		end, err := minutes(w.End)
		if err != nil {
			return nil, fmt.Errorf("schedule: window %d end: %w", i, err)
		}
		var days map[time.Weekday]bool
		if len(w.Days) > 0 {
			days = map[time.Weekday]bool{}
			for _, d := range w.Days {
				wd, ok := dayNames[strings.ToLower(strings.TrimSpace(d))]
				if !ok {
					return nil, fmt.Errorf("schedule: window %d: unknown day %q", i, d)
				}
				days[wd] = true
			}
		}
		s.windows = append(s.windows, window{days: days, start: start, end: end})
	}
	return s, nil
}

func minutes(hhmm string) (int, error) {
	t, err := time.Parse("15:04", hhmm)
	if err != nil {
		return 0, fmt.Errorf("want HH:MM, got %q", hhmm)
	}
	return t.Hour()*60 + t.Minute(), nil
}

// Open reports whether new work may start at t. A nil or empty schedule is
// always open.
func (s *Schedule) Open(t time.Time) bool {
	if s == nil || len(s.windows) == 0 {
		return true
	}
	lt := t.In(s.loc)
	m := lt.Hour()*60 + lt.Minute()
	today := lt.Weekday()
	yesterday := (today + 6) % 7
	for _, w := range s.windows {
		if w.start <= w.end {
			if w.on(today) && m >= w.start && m < w.end {
				return true
			}
			continue
		}
		// Spans midnight: the evening part belongs to today, the morning
		// part to the window that started yesterday.
		if w.on(today) && m >= w.start {
			return true
		}
		if w.on(yesterday) && m < w.end {
			return true
		}
	}
	return false
}

func (w window) on(d time.Weekday) bool {
	return w.days == nil || w.days[d]
}
