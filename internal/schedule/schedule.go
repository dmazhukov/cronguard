// Package schedule parses cron expressions and provides helpers for
// computing expected run times, drift, and missed-run counts.
//
// # Daylight-saving semantics
//
// The expected-run set is defined as the set of slots kube-controller-manager
// will attempt, not as a calendar-arithmetic ideal. That is not a preference:
// a monitor that disagrees with the scheduler it watches either alerts on runs
// Kubernetes never intended to start, or stays silent on ones it did.
//
// The identity holds by construction rather than by coincidence. This package
// builds cron.NewParser with the field mask Minute|Hour|Dom|Month|Dow|
// Descriptor, which is byte-identical to the mask behind cron.ParseStandard —
// the entry point Kubernetes' CronJob controller uses. Kubernetes prefixes the
// expression with "TZ=<zone> " and this package with "CRON_TZ=<zone> "; robfig
// routes both prefixes through the same branch. Same parser, same spec, same
// slot sequence.
//
// Two consequences follow, and both are pinned by dst_test.go:
//
//   - Spring forward: a slot whose wall-clock time does not exist is not a
//     run. Next never returns it, MissedRunsSince never counts it, Prev never
//     reports it. A daily 02:30 job has zero expected runs on the gap day, and
//     the absence is not a missed run.
//   - Fall back: a slot whose wall-clock time occurs twice is two runs, at two
//     distinct instants. A daily 02:30 job has two expected runs on the
//     overlap day, so a job that runs once that day has missed one.
//
// A schedule finer than the DST step can therefore return a Next whose local
// wall clock is earlier than the previous slot's. Instants are strictly
// increasing; wall clock is not. Consumers must compare instants — status
// fields are metav1.Time and always marshal as UTC, so status itself is
// unambiguous.
//
// The two rules above are claimed for zones whose DST step is a whole hour,
// which is every IANA zone in current use except Australia/Lord_Howe (a
// 30-minute step). There, robfig advances the hour field by an absolute hour
// and so lands half an hour off the grid after the transition, which can walk
// past a midnight slot that genuinely exists — Next("0 0 * * *") from the
// preceding slot skips local 2026-10-05 00:00, and MissedRunsSince
// consequently under-counts by one across that transition. Prev is unaffected:
// it is derived from Next by bisection, so the two agree by construction, and
// TestNextPrevRoundTrip enforces that. The Lord Howe skip is a robfig
// behaviour CronGuard inherits, pinned by TestDSTLordHoweSubHourStep so it
// cannot change unnoticed.
//
// Limit of the guarantee: when a CronJob declares no spec.timeZone,
// kube-controller-manager evaluates it in the controller-manager process's
// local zone while this package defaults to UTC. Standard control-plane images
// run in UTC, so the two agree in practice — but an operator who sets TZ to a
// DST zone on kube-controller-manager will see the two disagree for one hour a
// year.
package schedule

import (
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/robfig/cron/v3"
)

// ErrEmpty is returned when the expression is empty.
var ErrEmpty = errors.New("schedule: empty expression")

// ErrRelativeSchedule is returned for @every expressions.
//
// @every is an interval, not a wall-clock grid: robfig computes each slot
// relative to the previous fire, so there is no expected start time to compare
// a run against. Drift is therefore always ~0 and missed runs are
// uncountable, which makes every SLO axis this package feeds meaningless.
//
// A CEL rule rejects @every on CronJobMonitor.spec.schedule, but the
// reconciler also falls back to CronJob.spec.schedule, which Kubernetes
// accepts and no CEL rule of ours ever sees. Rejecting here closes that path:
// the monitor reports InvalidSchedule instead of silently publishing numbers
// that mean nothing.
var ErrRelativeSchedule = errors.New("schedule: @every is not supported; use a cron expression")

// isRelativeExpr reports whether expr is an @every descriptor, looking past an
// optional CRON_TZ=/TZ= prefix.
func isRelativeExpr(expr string) bool {
	body := expr
	if i := strings.IndexByte(body, ' '); i > 0 {
		if p := body[:i]; strings.HasPrefix(p, "CRON_TZ=") || strings.HasPrefix(p, "TZ=") {
			body = strings.TrimLeft(body[i+1:], " ")
		}
	}
	return strings.HasPrefix(body, "@every")
}

var parser = cron.NewParser(
	cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow | cron.Descriptor,
)

// Schedule is a parsed cron expression.
type Schedule struct {
	expr cron.Schedule
}

// Parse parses a 5-field cron expression or a supported descriptor
// (@hourly, @daily, @weekly, @monthly, @yearly) in UTC.
//
// Equivalent to ParseInLocation(expr, time.UTC).
func Parse(expr string) (*Schedule, error) {
	return ParseInLocation(expr, time.UTC)
}

// ParseInLocation parses a cron expression and binds it to loc. If loc is nil
// the expression is evaluated in UTC.
//
// If the expression itself carries a CRON_TZ=/TZ= prefix, the inline location
// wins and loc is ignored — this preserves robfig/cron native syntax. Without
// a prefix the parser would otherwise fall back to time.Local, which depends
// on the container's TZ env and silently drifts schedules; binding to UTC by
// default eliminates that footgun.
func ParseInLocation(expr string, loc *time.Location) (*Schedule, error) {
	if expr == "" {
		return nil, ErrEmpty
	}
	if isRelativeExpr(expr) {
		return nil, fmt.Errorf("schedule: parse %q: %w", expr, ErrRelativeSchedule)
	}
	fullExpr := expr
	if !strings.HasPrefix(expr, "CRON_TZ=") && !strings.HasPrefix(expr, "TZ=") {
		if loc == nil {
			loc = time.UTC
		}
		fullExpr = "CRON_TZ=" + loc.String() + " " + expr
	}
	s, err := parser.Parse(fullExpr)
	if err != nil {
		return nil, fmt.Errorf("schedule: parse %q: %w", expr, err)
	}
	return &Schedule{expr: s}, nil
}

// Next returns the first scheduled time strictly after `from`.
func (s *Schedule) Next(from time.Time) time.Time {
	return s.expr.Next(from)
}

// robfigReachYears mirrors the yearLimit in robfig/cron's SpecSchedule.Next
// (spec.go: `yearLimit := t.Year() + 5`). A single Next(t) therefore resolves
// only the window (t, endOfYear(year(t)+5)], and returns the zero time beyond
// it. That is a fact about the library, not a budget we chose.
const robfigReachYears = 5

// prevLookbackYears is how far back Prev will search.
//
// Derived from the grammar, not picked. This parser accepts five fields plus
// fixed descriptors — no year field, no L/W/#. Any expression with a non-star
// day-of-week recurs at least weekly, so the only way to be sparser than
// annual is a day-of-month/month pair that does not occur every year, and the
// only such pair is (29, 2). Its widest gap between consecutive slots is
// exactly 8 years — 2096-02-29 to 2104-02-29, because 2100 is not a leap year
// — and two consecutive century non-leap years are impossible under the
// 400-year rule, so 8 is the supremum rather than the largest case anyone
// happened to try. Nine gives a year of slack over a proven bound.
const prevLookbackYears = 9

// location returns the zone the expression is evaluated in. robfig computes
// its year limit after converting into that zone, so the search floor has to
// be measured there too.
func (s *Schedule) location() *time.Location {
	if spec, ok := s.expr.(*cron.SpecSchedule); ok && spec.Location != nil {
		return spec.Location
	}
	return time.UTC
}

// noSlotInRange reports whether the schedule has no slot in (x, at].
//
// A zero return from Next does not mean "no slot exists" — it means "no slot
// within robfig's five-year reach from x". That weaker fact is still usable:
// chaining it at five-year strides decides emptiness at any depth, which is
// what lets Prev resolve gaps wider than a single Next call can see. The loop
// advances x by at least five calendar years per iteration, so it terminates.
func (s *Schedule) noSlotInRange(loc *time.Location, x, at time.Time) bool {
	for {
		n := s.expr.Next(x)
		if !n.IsZero() {
			return n.After(at)
		}
		covered := time.Date(x.In(loc).Year()+robfigReachYears, 12, 31, 23, 59, 59, 0, loc)
		if !covered.Before(at) {
			return true
		}
		x = covered
	}
}

// Prev returns the most recent scheduled time at or before `at`, and whether
// one was found within the lookback horizon.
//
// Resolution is by instant, not wall clock — see the daylight-saving section
// in the package doc for what that means across a DST transition.
//
// Implemented as a floor probe plus a bisection on "is there a slot in
// (x, at]", which is monotone decreasing in x. The bisection converges on the
// largest x for which a slot still exists, and Next(x) is then that slot.
// Cost is bounded and uniform — about thirty Next calls — rather than
// proportional to the density of the schedule.
//
// The search window is half-open: a slot at exactly midnight on 1 January of
// year(at)-prevLookbackYears is not reported. Moving the floor back a second
// to include it would cost a year of reach at the top of the window, which is
// the end that matters.
//
// found is false for expressions that parse but can never fire — "0 0 30 2 *",
// "0 0 31 4 *" — and for a timestamp that belongs to no slot within the
// horizon. Callers must not treat the zero time as a real slot: doing so is
// what previously turned an unsatisfiable schedule into an unbounded loop.
func (s *Schedule) Prev(at time.Time) (time.Time, bool) {
	loc := s.location()
	year := at.In(loc).Year()
	if year <= prevLookbackYears {
		return time.Time{}, false
	}
	at = at.Truncate(time.Second)
	floor := time.Date(year-prevLookbackYears, 1, 1, 0, 0, 0, 0, loc)
	if s.noSlotInRange(loc, floor, at) {
		return time.Time{}, false
	}
	lo, hi := floor, at
	for hi.Sub(lo) > time.Second {
		mid := lo.Add(hi.Sub(lo) / 2).Truncate(time.Second)
		if !mid.After(lo) {
			break
		}
		if s.noSlotInRange(loc, mid, at) {
			hi = mid
		} else {
			lo = mid
		}
	}
	return s.expr.Next(lo), true
}

// Drift is actual - expected. Positive means late, negative means early.
func Drift(actual, expected time.Time) time.Duration {
	return actual.Sub(expected)
}

// MissedRunsSince counts scheduled slots in (lastStart, now - grace].
// Returns 0 if the last start is in the future or equal to now.
//
// The count is over slots the scheduler would attempt, so a slot skipped by a
// spring-forward gap is not counted and a slot doubled by a fall-back overlap
// is counted twice — see the daylight-saving section in the package doc.
func (s *Schedule) MissedRunsSince(lastStart, now time.Time, grace time.Duration) int {
	horizon := now.Add(-grace)
	if !horizon.After(lastStart) {
		return 0
	}
	count := 0
	cursor := lastStart
	for {
		next := s.expr.Next(cursor)
		// A zero return means the schedule has no further slot robfig can
		// reach. Without this guard the zero time compares as "not after the
		// horizon" and the loop runs to the safety rail, fabricating 100001
		// missed runs for a schedule that can never fire.
		if next.IsZero() || next.After(horizon) {
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

// NewForTest builds a Schedule around an arbitrary cron.Schedule. It exists so
// tests can instrument the underlying oracle — e.g. to assert that Prev's work
// stays bounded — without exporting the field itself.
func NewForTest(expr cron.Schedule) *Schedule {
	return &Schedule{expr: expr}
}
