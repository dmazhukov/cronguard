package schedule_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/robfig/cron/v3"

	"github.com/dmazhukov/cronguard/internal/schedule"
)

func TestParse(t *testing.T) {
	tests := []struct {
		name    string
		expr    string
		wantErr bool
	}{
		{"every minute", "* * * * *", false},
		{"every 5 minutes", "*/5 * * * *", false},
		{"daily 2am", "0 2 * * *", false},
		{"weekly monday", "0 9 * * 1", false},
		{"descriptor", "@hourly", false},
		{"empty", "", true},
		{"garbage", "not-a-cron", true},
		{"six fields rejected", "0 0 2 * * *", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := schedule.Parse(tt.expr)
			if (err != nil) != tt.wantErr {
				t.Fatalf("Parse(%q) error = %v, wantErr %v", tt.expr, err, tt.wantErr)
			}
		})
	}
}

func TestNext(t *testing.T) {
	s, err := schedule.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 1, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("Next = %v, want %v", got, want)
	}
}

func TestPrev(t *testing.T) {
	s, err := schedule.Parse("0 2 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 3, 0, 0, 0, time.UTC)
	got, found := s.Prev(from)
	want := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	if !found || !got.Equal(want) {
		t.Fatalf("Prev = %v (found=%v), want %v", got, found, want)
	}
}

func TestDrift(t *testing.T) {
	expected := time.Date(2026, 4, 24, 2, 0, 0, 0, time.UTC)
	tests := []struct {
		name   string
		actual time.Time
		want   time.Duration
	}{
		{"exact", expected, 0},
		{"late 30s", expected.Add(30 * time.Second), 30 * time.Second},
		{"early 10s", expected.Add(-10 * time.Second), -10 * time.Second},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := schedule.Drift(tt.actual, expected); got != tt.want {
				t.Fatalf("Drift = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestMissedRunsSince(t *testing.T) {
	s, err := schedule.Parse("0 * * * *") // hourly
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Last successful start at 10:00 UTC. Now is 13:30 UTC with 60s grace.
	// Expected slots within (10:00, 13:30 - 60s]: 11:00, 12:00, 13:00 => 3.
	lastStart := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 24, 13, 30, 0, 0, time.UTC)
	grace := 60 * time.Second
	got := s.MissedRunsSince(lastStart, now, grace)
	if got != 3 {
		t.Fatalf("MissedRunsSince = %d, want 3", got)
	}
}

func TestMissedRunsSinceWithinGrace(t *testing.T) {
	s, err := schedule.Parse("0 * * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 11:00 slot is within grace (now is 11:00:30, grace 60s) -> not missed.
	lastStart := time.Date(2026, 4, 24, 10, 0, 0, 0, time.UTC)
	now := time.Date(2026, 4, 24, 11, 0, 30, 0, time.UTC)
	grace := 60 * time.Second
	got := s.MissedRunsSince(lastStart, now, grace)
	if got != 0 {
		t.Fatalf("MissedRunsSince = %d, want 0", got)
	}
}

// A sparse schedule queried several days past its last slot.
func TestPrevWeeklyResolvesAcrossDays(t *testing.T) {
	s, err := schedule.Parse("0 9 * * 1") // Monday 09:00
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// Friday 2026-04-24 12:00 UTC — most recent Monday 09:00 is 2026-04-20.
	from := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	got, found := s.Prev(from)
	want := time.Date(2026, 4, 20, 9, 0, 0, 0, time.UTC)
	if !found || !got.Equal(want) {
		t.Fatalf("Prev = %v (found=%v), want %v", got, found, want)
	}
}

// A dense schedule queried mid-slot: the answer is the slot that just passed,
// not the next one.
func TestPrevHourlyResolvesTheSlotJustPassed(t *testing.T) {
	s, err := schedule.Parse("0 * * * *") // every hour
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 12, 30, 0, 0, time.UTC)
	got, found := s.Prev(from)
	want := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	if !found || !got.Equal(want) {
		t.Fatalf("Prev = %v (found=%v), want %v", got, found, want)
	}
}

// TestMissedRunsSinceLastStartInFuture covers the early-return path in
// MissedRunsSince when the last successful start is at or after `now-grace`.
func TestMissedRunsSinceLastStartInFuture(t *testing.T) {
	s, err := schedule.Parse("0 * * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	now := time.Date(2026, 4, 24, 12, 0, 0, 0, time.UTC)
	// lastStart in the future relative to now.
	lastStart := now.Add(time.Hour)
	if got := s.MissedRunsSince(lastStart, now, 60*time.Second); got != 0 {
		t.Fatalf("MissedRunsSince (lastStart in future) = %d, want 0", got)
	}
	// lastStart equal to now-grace boundary — also early-returns 0.
	if got := s.MissedRunsSince(now.Add(-60*time.Second), now, 60*time.Second); got != 0 {
		t.Fatalf("MissedRunsSince (boundary) = %d, want 0", got)
	}
}

// TestParseInLocationNewYork verifies that "0 9 * * *" parsed in
// America/New_York fires at 09:00 NY local — which is 13:00 UTC during EDT
// (UTC-4) and 14:00 UTC during EST (UTC-5).
func TestParseInLocationNewYork(t *testing.T) {
	ny, err := time.LoadLocation("America/New_York")
	if err != nil {
		t.Fatalf("LoadLocation: %v", err)
	}
	s, err := schedule.ParseInLocation("0 9 * * *", ny)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}

	// 2026-07-15 — EDT (UTC-4). 09:00 NY = 13:00 UTC.
	from := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("EDT Next = %v, want %v", got, want)
	}

	// 2026-12-15 — EST (UTC-5). 09:00 NY = 14:00 UTC.
	from = time.Date(2026, 12, 15, 12, 0, 0, 0, time.UTC)
	got = s.Next(from)
	want = time.Date(2026, 12, 15, 14, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("EST Next = %v, want %v", got, want)
	}
}

// TestParseInLocationNilDefaultsUTC verifies a nil location parses as UTC,
// and matches Parse(expr) bit-for-bit.
func TestParseInLocationNilDefaultsUTC(t *testing.T) {
	a, err := schedule.ParseInLocation("0 9 * * *", nil)
	if err != nil {
		t.Fatalf("ParseInLocation(nil): %v", err)
	}
	b, err := schedule.Parse("0 9 * * *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	from := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	if !a.Next(from).Equal(b.Next(from)) {
		t.Fatalf("nil-location Next %v != UTC Next %v", a.Next(from), b.Next(from))
	}
}

// TestParseInLocationInlinePrefixWins covers the case where the expression
// already carries a CRON_TZ= prefix — the inline location must take precedence
// over the loc argument so user expressions remain explicit.
func TestParseInLocationInlinePrefixWins(t *testing.T) {
	utc, _ := time.LoadLocation("UTC")
	s, err := schedule.ParseInLocation("CRON_TZ=America/New_York 0 9 * * *", utc)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}
	// EDT: 09:00 NY = 13:00 UTC, regardless of the UTC argument.
	from := time.Date(2026, 7, 15, 12, 0, 0, 0, time.UTC)
	got := s.Next(from)
	want := time.Date(2026, 7, 15, 13, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("inline-TZ Next = %v, want %v", got, want)
	}
}

// TestParseInLocationInvalidExpression surfaces the parser error path through
// ParseInLocation (covers the error wrap branch).
func TestParseInLocationInvalidExpression(t *testing.T) {
	_, err := schedule.ParseInLocation("not-a-cron", time.UTC)
	if err == nil {
		t.Fatalf("ParseInLocation(invalid) returned nil error")
	}
}

// countingSchedule wraps a cron.Schedule to measure how much work Prev does.
type countingSchedule struct {
	inner cron.Schedule
	calls int
}

func (c *countingSchedule) Next(t time.Time) time.Time {
	c.calls++
	return c.inner.Next(t)
}

// M6 — the previous slot may be years back. The old implementation stepped
// back one day at a time for at most 366 iterations and then gave up, so a
// leap-day schedule silently reported "no previous run" and drift was never
// computed.
func TestPrevResolvesGapsWiderThanAYear(t *testing.T) {
	s, err := schedule.Parse("0 0 29 2 *") // 29 February
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cases := []struct {
		at   string
		want string
		why  string
	}{
		{at: "2025-01-15T00:00:00Z", want: "2024-02-29T00:00:00Z", why: "321 days back — inside the old bound too"},
		{at: "2025-03-05T00:00:00Z", want: "2024-02-29T00:00:00Z", why: "370 days back — just past the old 366-day bound"},
		{at: "2026-06-10T12:00:00Z", want: "2024-02-29T00:00:00Z", why: "the ordinary four-year leap gap"},
		{at: "2028-02-29T00:00:00Z", want: "2028-02-29T00:00:00Z", why: "at-or-before is inclusive"},
	}
	for _, tc := range cases {
		at, err := time.Parse(time.RFC3339, tc.at)
		if err != nil {
			t.Fatalf("bad at %q: %v", tc.at, err)
		}
		got, found := s.Prev(at)
		if !found {
			t.Errorf("Prev(%s): not found — want %s (%s)", tc.at, tc.want, tc.why)
			continue
		}
		want, _ := time.Parse(time.RFC3339, tc.want)
		if !got.Equal(want) {
			t.Errorf("Prev(%s) = %s, want %s (%s)", tc.at, got.Format(time.RFC3339), tc.want, tc.why)
		}
	}
}

// The widest gap any expression in this grammar can have is the century
// non-leap boundary: 2096-02-29 to 2104-02-29, because 2100 is not a leap
// year. Crossing it needs more reach than a single robfig Next call has, so
// these rows discriminate the chained-stride implementation from a naive
// "floor at year(at)-5" one, which resolves 2101 but fails 2102 and 2103.
func TestPrevCrossesCenturyNonLeapGap(t *testing.T) {
	s, err := schedule.Parse("0 0 29 2 *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	for _, tc := range []struct{ at, want string }{
		{"2101-06-01T00:00:00Z", "2096-02-29T00:00:00Z"},
		{"2102-06-01T00:00:00Z", "2096-02-29T00:00:00Z"},
		{"2103-06-01T00:00:00Z", "2096-02-29T00:00:00Z"},
		{"2104-02-28T00:00:00Z", "2096-02-29T00:00:00Z"},
		{"2104-03-01T00:00:00Z", "2104-02-29T00:00:00Z"},
	} {
		at, _ := time.Parse(time.RFC3339, tc.at)
		got, found := s.Prev(at)
		if !found {
			t.Errorf("Prev(%s): not found — want %s", tc.at, tc.want)
			continue
		}
		want, _ := time.Parse(time.RFC3339, tc.want)
		if !got.Equal(want) {
			t.Errorf("Prev(%s) = %s, want %s", tc.at, got.Format(time.RFC3339), tc.want)
		}
	}
}

// These expressions parse and pass the CRD's CEL shape check, but no calendar
// date satisfies them. The old Prev looped forever on them — reachable from
// CronJob.spec.schedule, which the monitor's CEL never inspects, and with one
// reconcile worker that wedges the operator entirely.
func TestPrevTerminatesOnUnsatisfiableSchedules(t *testing.T) {
	at := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	for _, expr := range []string{
		"0 0 30 2 *", "0 0 31 2 *", "0 0 31 4 *",
		"0 0 31 6 *", "0 0 31 9 *", "0 0 31 11 *",
	} {
		s, err := schedule.Parse(expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", expr, err)
		}
		done := make(chan bool, 1)
		go func() {
			_, found := s.Prev(at)
			done <- found
		}()
		select {
		case found := <-done:
			if found {
				t.Errorf("Prev(%q) reported a slot for a date that never occurs", expr)
			}
		case <-time.After(5 * time.Second):
			t.Fatalf("Prev(%q) did not terminate", expr)
		}
	}
}

// The horizon claim, asserted rather than asserted-about: every day/month pair
// the calendar admits must resolve, and only the six impossible ones must not.
func TestPrevResolvesEverySatisfiableDayMonthPair(t *testing.T) {
	// Guarded: six of these pairs are the ones that used to loop forever, and
	// an unguarded sweep would wedge the package at the 10-minute timeout
	// instead of reporting a clean failure.
	done := make(chan struct{})
	go func() {
		defer close(done)
		prevDayMonthSweep(t)
	}()
	select {
	case <-done:
	case <-time.After(30 * time.Second):
		t.Fatal("day/month sweep did not terminate")
	}
}

func prevDayMonthSweep(t *testing.T) {
	t.Helper()
	at := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
	maxDay := map[int]int{1: 31, 2: 29, 3: 31, 4: 30, 5: 31, 6: 30, 7: 31, 8: 31, 9: 30, 10: 31, 11: 30, 12: 31}
	for month := 1; month <= 12; month++ {
		for dom := 1; dom <= 31; dom++ {
			expr := fmt.Sprintf("0 0 %d %d *", dom, month)
			s, err := schedule.Parse(expr)
			if err != nil {
				t.Fatalf("Parse(%q): %v", expr, err)
			}
			_, found := s.Prev(at)
			if want := dom <= maxDay[month]; found != want {
				t.Errorf("Prev(%q) found=%v, want %v", expr, found, want)
			}
		}
	}
}

// Work per call must stay bounded and independent of how dense the schedule
// is. The old implementation needed 1441 Next calls for a per-minute schedule
// on every reconcile; this pins that a future change cannot quietly reintroduce
// a linear scan.
func TestPrevWorkIsBounded(t *testing.T) {
	const budget = 64
	for _, tc := range []struct {
		expr string
		at   string
	}{
		{"* * * * *", "2026-04-24T12:30:00Z"},
		{"0 0 1 1 *", "2026-12-20T00:00:00Z"},
		{"0 0 29 2 *", "2026-06-10T00:00:00Z"},
	} {
		parsed, err := cron.ParseStandard("CRON_TZ=UTC " + tc.expr)
		if err != nil {
			t.Fatalf("ParseStandard(%q): %v", tc.expr, err)
		}
		counter := &countingSchedule{inner: parsed}
		s := schedule.NewForTest(counter)
		at, _ := time.Parse(time.RFC3339, tc.at)
		if _, _ = s.Prev(at); counter.calls > budget {
			t.Errorf("Prev(%q) used %d Next calls, budget is %d", tc.expr, counter.calls, budget)
		}
	}
}

// An unsatisfiable schedule must not fabricate missed runs either: the old
// loop ran to its safety rail and reported 100001.
func TestMissedRunsSinceUnsatisfiableSchedule(t *testing.T) {
	s, err := schedule.Parse("0 0 30 2 *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	got := s.MissedRunsSince(
		time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC),
		time.Minute)
	if got != 0 {
		t.Errorf("MissedRunsSince = %d, want 0 — a schedule that can never fire misses nothing", got)
	}
}

// A zero from Next means "nothing within robfig's reach", not "nothing ever".
// Stopping on it would trade the 100001 fabrication for a silent under-count
// on sparse schedules across a long gap — the same class of bug in the other
// direction.
func TestMissedRunsSinceSpansGapsWiderThanRobfigReach(t *testing.T) {
	s, err := schedule.Parse("0 0 29 2 *")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// 2096-03-01 to 2105-01-01 contains exactly one 29 February: 2104, since
	// 2100 is not a leap year. No single Next call can see that far.
	got := s.MissedRunsSince(
		time.Date(2096, 3, 1, 0, 0, 0, 0, time.UTC),
		time.Date(2105, 1, 1, 0, 0, 0, 0, time.UTC),
		0)
	if got != 1 {
		t.Errorf("MissedRunsSince across the century gap = %d, want 1 (2104-02-29)", got)
	}
}
