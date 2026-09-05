package schedule_test

import (
	"errors"
	"testing"
	"time"

	// Embed the IANA database so these assertions do not depend on the test
	// runner shipping /usr/share/zoneinfo. Without it the suite would silently
	// skip on a distroless or Windows runner instead of failing.
	_ "time/tzdata"

	"github.com/dmazhukov/cronguard/internal/schedule"
)

// DST semantics pinned here are described in the package doc of
// internal/schedule. Summary of what these tests defend:
//
//   - spring forward: a slot whose wall-clock time does not exist is not a run
//   - fall back:      a slot whose wall-clock time occurs twice is two runs
//
// Both follow from robfig/cron advancing absolute time against a wall-clock
// field matcher, which is exactly what kube-controller-manager's CronJob
// controller does. CronGuard must agree with the scheduler it monitors, so
// these are not preferences — they are the observed system's behaviour, and a
// change here means CronGuard has started disagreeing with Kubernetes.

func mustLoad(t *testing.T, name string) *time.Location {
	t.Helper()
	loc, err := time.LoadLocation(name)
	if err != nil {
		t.Fatalf("LoadLocation(%q): %v", name, err)
	}
	return loc
}

// guardZone fails with a legible message when the tzdata rules for a zone are
// not the ones these tests were written against. Without it a tzdata update
// surfaces as an inscrutable timestamp mismatch several assertions later.
func guardZone(t *testing.T, name string, instant time.Time, wantOffset int) {
	t.Helper()
	loc := mustLoad(t, name)
	_, off := instant.In(loc).Zone()
	if off != wantOffset {
		t.Fatalf("%s offset at %s = %d, want %d — tzdata rules changed",
			name, instant.Format(time.RFC3339), off, wantOffset)
	}
}

func mustParseIn(t *testing.T, expr, zone string) *schedule.Schedule {
	t.Helper()
	s, err := schedule.ParseInLocation(expr, mustLoad(t, zone))
	if err != nil {
		t.Fatalf("ParseInLocation(%q, %q): %v", expr, zone, err)
	}
	return s
}

// nextN collects the next n slots as UTC instants.
func nextN(s *schedule.Schedule, from time.Time, n int) []time.Time {
	out := make([]time.Time, 0, n)
	cur := from
	for i := 0; i < n; i++ {
		cur = s.Next(cur)
		out = append(out, cur.UTC())
	}
	return out
}

func assertInstants(t *testing.T, label string, got []time.Time, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s: got %d slots, want %d", label, len(got), len(want))
	}
	for i := range want {
		w, err := time.Parse(time.RFC3339, want[i])
		if err != nil {
			t.Fatalf("%s: bad want[%d] %q: %v", label, i, want[i], err)
		}
		if !got[i].Equal(w) {
			t.Errorf("%s slot %d = %s, want %s", label, i, got[i].Format(time.RFC3339), want[i])
		}
	}
}

// A daily 02:30 job has ZERO expected runs on a spring-forward day: 02:30 never
// exists on the wall clock, so the scheduler never fires it and CronGuard must
// not count it as missed.
func TestDSTSpringForwardSkipsNonexistentSlot(t *testing.T) {
	cases := []struct {
		zone  string
		expr  string
		from  string // RFC3339 with zone offset
		want  []string
		guard struct {
			at     string
			offset int
		}
	}{
		{
			zone: "America/New_York", expr: "30 2 * * *",
			// 2026-03-08 02:00 EST -> 03:00 EDT.
			from: "2026-03-06T03:00:00-05:00",
			want: []string{
				"2026-03-07T07:30:00Z", // 02:30 EST
				"2026-03-09T06:30:00Z", // 02:30 EDT — 03-08 skipped entirely
				"2026-03-10T06:30:00Z",
			},
		},
		{
			zone: "Europe/Berlin", expr: "30 2 * * *",
			// 2026-03-29 02:00 CET -> 03:00 CEST.
			from: "2026-03-27T03:00:00+01:00",
			want: []string{
				"2026-03-28T01:30:00Z", // 02:30 CET
				"2026-03-30T00:30:00Z", // 02:30 CEST — 03-29 skipped
				"2026-03-31T00:30:00Z",
			},
		},
		{
			// Southern hemisphere: the transition runs the other way round in
			// the calendar year, which is why one zone is not enough.
			zone: "Australia/Sydney", expr: "30 2 * * *",
			// 2026-10-04 02:00 AEST -> 03:00 AEDT.
			from: "2026-10-02T03:00:00+10:00",
			want: []string{
				"2026-10-02T16:30:00Z", // 10-03 02:30 AEST
				"2026-10-04T15:30:00Z", // 10-05 02:30 AEDT — 10-04 skipped
				"2026-10-05T15:30:00Z",
			},
		},
	}
	for _, tc := range cases {
		from, err := time.Parse(time.RFC3339, tc.from)
		if err != nil {
			t.Fatalf("bad from %q: %v", tc.from, err)
		}
		s := mustParseIn(t, tc.expr, tc.zone)
		assertInstants(t, tc.zone+" spring-forward", nextN(s, from, len(tc.want)), tc.want)
	}
}

// A daily 01:30/02:30 job has TWO expected runs on a fall-back day: the wall
// clock passes through the slot twice, at two distinct instants, and the
// scheduler fires both. A job that runs only once that day has genuinely
// missed a run.
func TestDSTFallBackEmitsDoubledSlotTwice(t *testing.T) {
	cases := []struct {
		zone string
		expr string
		from string
		want []string
	}{
		{
			zone: "America/New_York", expr: "30 1 * * *",
			// 2026-11-01 02:00 EDT -> 01:00 EST.
			from: "2026-10-30T03:00:00-04:00",
			want: []string{
				"2026-10-31T05:30:00Z", // 01:30 EDT
				"2026-11-01T05:30:00Z", // 01:30 EDT — first pass
				"2026-11-01T06:30:00Z", // 01:30 EST — second pass, same wall clock
				"2026-11-02T06:30:00Z",
			},
		},
		{
			zone: "Europe/Berlin", expr: "30 2 * * *",
			// 2026-10-25 03:00 CEST -> 02:00 CET.
			from: "2026-10-23T03:00:00+02:00",
			want: []string{
				"2026-10-24T00:30:00Z", // 02:30 CEST
				"2026-10-25T00:30:00Z", // 02:30 CEST — first pass
				"2026-10-25T01:30:00Z", // 02:30 CET  — second pass
				"2026-10-26T01:30:00Z",
			},
		},
	}
	for _, tc := range cases {
		from, err := time.Parse(time.RFC3339, tc.from)
		if err != nil {
			t.Fatalf("bad from %q: %v", tc.from, err)
		}
		s := mustParseIn(t, tc.expr, tc.zone)
		got := nextN(s, from, len(tc.want))
		assertInstants(t, tc.zone+" fall-back", got, tc.want)
		// The doubled pair shares a wall clock but not an instant. Both
		// properties are load-bearing: equal wall clock is what makes it a
		// "double fire", distinct instants are what stops Next from looping.
		if !got[1].Before(got[2]) {
			t.Errorf("%s: doubled slots must advance in absolute time: %s !< %s",
				tc.zone, got[1], got[2])
		}
	}
}

// The SLO consequence of the two behaviours above, stated as counts: a
// spring-forward gap must not inflate the missed-run count, and a fall-back
// overlap must not deflate it.
func TestDSTMissedRunsAcrossTransitions(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	berlin := mustLoad(t, "Europe/Berlin")

	// Guard the tzdata rules these numbers were derived from.
	guardZone(t, "America/New_York", time.Date(2026, 3, 9, 12, 0, 0, 0, time.UTC), -4*3600)
	guardZone(t, "America/New_York", time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC), -5*3600)
	guardZone(t, "Europe/Berlin", time.Date(2026, 3, 30, 12, 0, 0, 0, time.UTC), 2*3600)
	guardZone(t, "Europe/Berlin", time.Date(2026, 10, 26, 12, 0, 0, 0, time.UTC), 1*3600)

	cases := []struct {
		name      string
		expr      string
		loc       *time.Location
		lastStart time.Time
		now       time.Time
		want      int
		why       string
	}{
		{
			name:      "daily slot skipped by spring forward is not a missed run",
			expr:      "30 2 * * *",
			loc:       ny,
			lastStart: time.Date(2026, 3, 6, 2, 30, 0, 0, ny),
			now:       time.Date(2026, 3, 10, 3, 0, 0, 0, ny),
			want:      3, // 03-07, 03-09, 03-10 — the naive calendar would say 4
			why:       "the 03-08 02:30 slot never existed, so it cannot be missed",
		},
		{
			name:      "daily slot doubled by fall back counts twice",
			expr:      "30 1 * * *",
			loc:       ny,
			lastStart: time.Date(2026, 10, 31, 1, 30, 0, 0, ny),
			now:       time.Date(2026, 11, 2, 3, 0, 0, 0, ny),
			want:      3, // 11-01 EDT, 11-01 EST, 11-02 — the naive calendar would say 2
			why:       "the 11-01 01:30 slot occurred twice and both were expected",
		},
		{
			name:      "hourly job has 23 slots on a spring-forward day",
			expr:      "0 * * * *",
			loc:       ny,
			lastStart: time.Date(2026, 3, 8, 0, 0, 0, 0, ny),
			now:       time.Date(2026, 3, 9, 0, 0, 0, 0, ny),
			want:      23,
			why:       "the wall-clock day is 23 hours long",
		},
		{
			name:      "hourly job has 25 slots on a fall-back day",
			expr:      "0 * * * *",
			loc:       ny,
			lastStart: time.Date(2026, 11, 1, 0, 0, 0, 0, ny),
			now:       time.Date(2026, 11, 2, 0, 0, 0, 0, ny),
			want:      25,
			why:       "the wall-clock day is 25 hours long",
		},
		{
			name:      "European spring forward behaves the same way",
			expr:      "0 * * * *",
			loc:       berlin,
			lastStart: time.Date(2026, 3, 29, 0, 0, 0, 0, berlin),
			now:       time.Date(2026, 3, 30, 0, 0, 0, 0, berlin),
			want:      23,
			why:       "EU transition rules differ from US but the mechanism is identical",
		},
		{
			name:      "European fall back behaves the same way",
			expr:      "0 * * * *",
			loc:       berlin,
			lastStart: time.Date(2026, 10, 25, 0, 0, 0, 0, berlin),
			now:       time.Date(2026, 10, 26, 0, 0, 0, 0, berlin),
			want:      25,
			why:       "EU transition rules differ from US but the mechanism is identical",
		},
	}

	for _, tc := range cases {
		s, err := schedule.ParseInLocation(tc.expr, tc.loc)
		if err != nil {
			t.Fatalf("%s: parse: %v", tc.name, err)
		}
		got := s.MissedRunsSince(tc.lastStart, tc.now, 0)
		if got != tc.want {
			t.Errorf("%s: MissedRunsSince = %d, want %d (%s)", tc.name, got, tc.want, tc.why)
		}
	}
}

// Prev resolves against instants, not wall clock, so the doubled fall-back
// slot is attributed unambiguously and the nonexistent spring-forward slot is
// never returned. Drift is computed against Prev, so a wrong answer here shows
// up as a bogus cronguard_schedule_drift_seconds once a year.
func TestDSTPrevResolvesByInstant(t *testing.T) {
	ny := mustLoad(t, "America/New_York")
	s := mustParseIn(t, "30 1 * * *", "America/New_York")

	first := time.Date(2026, 11, 1, 5, 30, 0, 0, time.UTC)  // 01:30 EDT
	second := time.Date(2026, 11, 1, 6, 30, 0, 0, time.UTC) // 01:30 EST

	if got, found := s.Prev(first); !found || !got.Equal(first) {
		t.Errorf("Prev at first occurrence = %s, want %s", got.Format(time.RFC3339), first.Format(time.RFC3339))
	}
	if got, found := s.Prev(second); !found || !got.Equal(second) {
		t.Errorf("Prev at second occurrence = %s, want %s", got.Format(time.RFC3339), second.Format(time.RFC3339))
	}
	// Halfway between the pair: the first occurrence is the most recent slot.
	mid := time.Date(2026, 11, 1, 6, 0, 0, 0, time.UTC)
	if got, found := s.Prev(mid); !found || !got.Equal(first) {
		t.Errorf("Prev between the pair = %s, want %s", got.Format(time.RFC3339), first.Format(time.RFC3339))
	}

	// Spring forward: asking on the gap day must return the previous real
	// slot, never a fabricated 02:30 on the day it did not exist.
	gap := mustParseIn(t, "30 2 * * *", "America/New_York")
	want := time.Date(2026, 3, 7, 2, 30, 0, 0, ny)
	if got, found := gap.Prev(time.Date(2026, 3, 8, 12, 0, 0, 0, ny)); !found || !got.Equal(want) {
		t.Errorf("Prev on gap day = %s, want %s", got.Format(time.RFC3339), want.Format(time.RFC3339))
	}
}

// Next and Prev must describe the same slot set. Prev is CronGuard's own
// construction — a bisection over Next — and this is the assertion that keeps
// the two from drifting apart: a slot Next produces must be the slot Prev
// resolves for that instant, in every zone the suite covers, across a full
// year including both transitions.
func TestNextPrevRoundTrip(t *testing.T) {
	zones := []string{
		"UTC",
		"America/New_York",
		"Europe/Berlin",
		"Australia/Sydney",
		"America/Santiago",
		"Pacific/Chatham",     // 45-minute standard offset
		"Australia/Lord_Howe", // 30-minute DST step
	}
	exprs := []string{"0 0 * * *", "@daily", "0 * * * *", "30 2 * * *", "0 0 * * 0", "*/15 * * * *"}

	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	end := time.Date(2027, 1, 1, 0, 0, 0, 0, time.UTC)

	for _, zone := range zones {
		loc := mustLoad(t, zone)
		for _, expr := range exprs {
			s, err := schedule.ParseInLocation(expr, loc)
			if err != nil {
				t.Fatalf("%s %s: %v", zone, expr, err)
			}
			for cur := start; cur.Before(end); {
				n := s.Next(cur)
				if n.IsZero() {
					break
				}
				got, found := s.Prev(n)
				if !found || !got.Equal(n) {
					t.Fatalf("%s %s: Next(%s) = %s but Prev of that = %s (found=%v)",
						zone, expr, cur.Format(time.RFC3339), n.Format(time.RFC3339),
						got.Format(time.RFC3339), found)
				}
				cur = n
			}
		}
	}
}

// Australia/Lord_Howe is the one zone whose DST step is half an hour, and the
// guarantee stated in the package doc does not hold there. robfig advances the
// hour field by an absolute hour, so after the transition the cursor sits on
// :30 and the day's midnight slot is walked past — but only when the search
// starts before the transition. The result is path-dependent, and this test
// pins the actual behaviour so it cannot change unnoticed. It asserts what the
// code DOES, not what it ought to do.
func TestDSTLordHoweSubHourStep(t *testing.T) {
	lh := mustLoad(t, "Australia/Lord_Howe")
	s, err := schedule.ParseInLocation("0 0 * * *", lh)
	if err != nil {
		t.Fatalf("ParseInLocation: %v", err)
	}
	// 2026-10-04 02:00 +10:30 -> 02:30 +11:00.
	beforeTransition := time.Date(2026, 10, 3, 13, 30, 0, 0, time.UTC) // local 10-04 00:00
	afterTransition := time.Date(2026, 10, 4, 12, 59, 59, 0, time.UTC)

	skipped := s.Next(beforeTransition).UTC()
	direct := s.Next(afterTransition).UTC()
	wantDirect := time.Date(2026, 10, 4, 13, 0, 0, 0, time.UTC) // local 10-05 00:00

	if !direct.Equal(wantDirect) {
		t.Fatalf("Next after the transition = %s, want %s", direct.Format(time.RFC3339), wantDirect.Format(time.RFC3339))
	}
	if skipped.Equal(wantDirect) {
		t.Fatal("Next is no longer path-dependent on Lord Howe — the divergence documented in the " +
			"package doc has been fixed upstream or worked around; update the doc and delete this test")
	}
	if want := time.Date(2026, 10, 5, 13, 0, 0, 0, time.UTC); !skipped.Equal(want) {
		t.Errorf("Next across the 30-minute step = %s, want %s", skipped.Format(time.RFC3339), want.Format(time.RFC3339))
	}

	// The SLO consequence, written down rather than left to be discovered: one
	// real slot goes uncounted across this transition.
	end := time.Date(2026, 10, 6, 2, 0, 0, 0, time.UTC)
	if got := s.MissedRunsSince(beforeTransition, end, 0); got != 1 {
		t.Errorf("MissedRunsSince across the Lord Howe transition = %d, want 1 "+
			"(the wall clock has 2 slots; the under-count is the documented divergence)", got)
	}
}

// @every has no wall-clock grid, so every SLO number derived from it is
// meaningless. A CEL rule rejects it on the monitor's own spec.schedule, but
// the reconciler also falls back to CronJob.spec.schedule, which Kubernetes
// accepts and our CEL never sees — so the parser is the choke point that
// actually closes the path.
func TestParseRejectsRelativeSchedules(t *testing.T) {
	for _, expr := range []string{
		"@every 1h",
		"@every 30m",
		"CRON_TZ=America/New_York @every 1h",
		"TZ=UTC @every 5m",
	} {
		if _, err := schedule.Parse(expr); err == nil {
			t.Errorf("Parse(%q) accepted an interval schedule", expr)
		} else if !errors.Is(err, schedule.ErrRelativeSchedule) {
			t.Errorf("Parse(%q) error = %v, want ErrRelativeSchedule", expr, err)
		}
	}
	// Descriptors that DO map onto a wall-clock grid must keep working.
	for _, expr := range []string{"@daily", "@hourly", "@weekly", "@monthly", "@yearly"} {
		if _, err := schedule.Parse(expr); err != nil {
			t.Errorf("Parse(%q) = %v, want success", expr, err)
		}
	}
}
