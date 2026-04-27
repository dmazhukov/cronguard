// Package schedule parses cron expressions and provides helpers for
// computing expected run times, drift, and missed-run counts.
package schedule

import (
	"errors"
	"fmt"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrEmpty is returned when the expression is empty.
var ErrEmpty = errors.New("schedule: empty expression")

var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Schedule is a parsed cron expression.
type Schedule struct {
	expr cron.Schedule
}

// Parse parses a 5-field cron expression or a supported descriptor
// (@hourly, @daily, @weekly, @monthly, @yearly).
func Parse(expr string) (*Schedule, error) {
	if expr == "" {
		return nil, ErrEmpty
	}
	s, err := parser.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("schedule: parse %q: %w", expr, err)
	}
	return &Schedule{expr: s}, nil
}

// Next returns the first scheduled time strictly after `from`.
func (s *Schedule) Next(from time.Time) time.Time {
	return s.expr.Next(from)
}

// Prev returns the most recent scheduled time at or before `at`.
// Implemented as a bounded backward scan: step back in large intervals
// and use Next() to find the last slot <= at.
func (s *Schedule) Prev(at time.Time) time.Time {
	// Walk back one day at a time until Next(walkStart) <= at.
	walkStart := at.Add(-24 * time.Hour)
	for i := 0; i < 366; i++ {
		candidate := s.expr.Next(walkStart)
		if candidate.After(at) {
			walkStart = walkStart.Add(-24 * time.Hour)
			continue
		}
		// candidate <= at; find the maximum <= at by walking forward.
		last := candidate
		for {
			nxt := s.expr.Next(last)
			if nxt.After(at) {
				return last
			}
			last = nxt
		}
	}
	// Fallback: the expression has no run in the past year. Return zero.
	return time.Time{}
}

// Drift is actual - expected. Positive means late, negative means early.
func Drift(actual, expected time.Time) time.Duration {
	return actual.Sub(expected)
}

// MissedRunsSince counts scheduled slots in (lastStart, now - grace].
// Returns 0 if the last start is in the future or equal to now.
func (s *Schedule) MissedRunsSince(lastStart, now time.Time, grace time.Duration) int {
	horizon := now.Add(-grace)
	if !horizon.After(lastStart) {
		return 0
	}
	count := 0
	cursor := lastStart
	for {
		next := s.expr.Next(cursor)
		if next.After(horizon) {
			return count
		}
		count++
		cursor = next
		if count > 100000 {
			// Safety rail: never loop unbounded.
			return count
		}
	}
}
