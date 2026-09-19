package history_test

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/history"
)

func rec(name string, start time.Time, phase monitoringv1alpha1.ExecutionPhase) monitoringv1alpha1.ExecutionRecord {
	return monitoringv1alpha1.ExecutionRecord{
		JobName:   name,
		StartTime: metav1.NewTime(start),
		Phase:     phase,
	}
}

func TestRingKeepsNewestFirstWithinCap(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	in := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now.Add(1*time.Hour), monitoringv1alpha1.ExecutionPhaseSucceeded),
		rec("job-2", now.Add(2*time.Hour), monitoringv1alpha1.ExecutionPhaseFailed),
		rec("job-3", now.Add(3*time.Hour), monitoringv1alpha1.ExecutionPhaseRunning),
	}
	got := history.Merge(nil, in, 10)
	if len(got) != 3 {
		t.Fatalf("len = %d, want 3", len(got))
	}
	if got[0].JobName != "job-3" {
		t.Fatalf("got[0] = %q, want job-3 (newest first)", got[0].JobName)
	}
	if got[2].JobName != "job-1" {
		t.Fatalf("got[2] = %q, want job-1", got[2].JobName)
	}
}

func TestRingTruncatesToCap(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	in := make([]monitoringv1alpha1.ExecutionRecord, 0, 15)
	for i := 0; i < 15; i++ {
		in = append(in, rec(
			"job-"+time.Duration(i).String(),
			now.Add(time.Duration(i)*time.Hour),
			monitoringv1alpha1.ExecutionPhaseSucceeded,
		))
	}
	got := history.Merge(nil, in, 10)
	if len(got) != 10 {
		t.Fatalf("len = %d, want 10", len(got))
	}
	wantFirst := in[14].JobName
	if got[0].JobName != wantFirst {
		t.Fatalf("got[0] = %q, want %q", got[0].JobName, wantFirst)
	}
}

func TestRingMergesExistingWithIncoming(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	existing := []monitoringv1alpha1.ExecutionRecord{
		rec("old-1", now.Add(-2*time.Hour), monitoringv1alpha1.ExecutionPhaseSucceeded),
	}
	incoming := []monitoringv1alpha1.ExecutionRecord{
		rec("new-1", now.Add(1*time.Hour), monitoringv1alpha1.ExecutionPhaseSucceeded),
	}
	got := history.Merge(existing, incoming, 10)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].JobName != "new-1" {
		t.Fatalf("got[0] = %q, want new-1", got[0].JobName)
	}
}

func TestRingDeduplicatesByJobName(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	existing := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now, monitoringv1alpha1.ExecutionPhaseRunning),
	}
	completion := metav1.NewTime(now.Add(time.Minute))
	incoming := []monitoringv1alpha1.ExecutionRecord{
		{
			JobName:   "job-1",
			StartTime: metav1.NewTime(now),
			EndTime:   &completion,
			Phase:     monitoringv1alpha1.ExecutionPhaseSucceeded,
		},
	}
	got := history.Merge(existing, incoming, 10)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1 (dedup)", len(got))
	}
	if got[0].Phase != monitoringv1alpha1.ExecutionPhaseSucceeded {
		t.Fatalf("phase = %q, want Succeeded (newer wins)", got[0].Phase)
	}
}

// TestRingRejectsZeroLimit covers the early-return branch in Merge.
func TestRingRejectsZeroLimit(t *testing.T) {
	in := []monitoringv1alpha1.ExecutionRecord{
		rec("job-a", time.Now(), monitoringv1alpha1.ExecutionPhaseSucceeded),
	}
	got := history.Merge(nil, in, 0)
	if got != nil {
		t.Fatalf("Merge with limit=0 returned %v, want nil", got)
	}
}

// TestRingShouldReplaceByLaterStart covers the primary "later StartTime wins"
// branch in shouldReplace AND the JobName tie-break in sort. Two existing
// records share a StartTime to exercise the sort tie-break; incoming has a
// later StartTime to exercise the replace path.
func TestRingShouldReplaceByLaterStart(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	existing := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now, monitoringv1alpha1.ExecutionPhaseRunning),
		rec("job-2", now, monitoringv1alpha1.ExecutionPhaseRunning),
	}
	incoming := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now.Add(5*time.Minute), monitoringv1alpha1.ExecutionPhaseSucceeded),
	}
	got := history.Merge(existing, incoming, 10)
	if len(got) != 2 {
		t.Fatalf("len = %d, want 2", len(got))
	}
	if got[0].JobName != "job-1" {
		t.Fatalf("got[0] = %q, want job-1 (newest start)", got[0].JobName)
	}
	if got[0].Phase != monitoringv1alpha1.ExecutionPhaseSucceeded {
		t.Fatalf("got[0].Phase = %q, want Succeeded", got[0].Phase)
	}
}

// TestRingKeepsExistingWhenIncomingIsEarlier covers the "return false" path
// in shouldReplace — existing has a later StartTime, incoming must lose.
func TestRingKeepsExistingWhenIncomingIsEarlier(t *testing.T) {
	now := time.Date(2026, 4, 24, 0, 0, 0, 0, time.UTC)
	existing := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now, monitoringv1alpha1.ExecutionPhaseSucceeded),
	}
	incoming := []monitoringv1alpha1.ExecutionRecord{
		rec("job-1", now.Add(-5*time.Minute), monitoringv1alpha1.ExecutionPhaseRunning),
	}
	got := history.Merge(existing, incoming, 10)
	if len(got) != 1 {
		t.Fatalf("len = %d, want 1", len(got))
	}
	if got[0].Phase != monitoringv1alpha1.ExecutionPhaseSucceeded {
		t.Fatalf("got[0].Phase = %q, want Succeeded (existing kept)", got[0].Phase)
	}
}

// TestMergePreservesDriftAnnotations checks that when an incoming record
// supersedes an existing one with the same JobName, ExpectedStartTime and
// DriftSeconds carry over from the old record if the new record has them
// nil. This matches the v0.3 design: drift is computed once when the Job
// transitions from Pending → Running and must survive the Running →
// Succeeded/Failed transition.
func TestMergePreservesDriftAnnotations(t *testing.T) {
	start := metav1.Date(2026, 5, 6, 12, 0, 0, 0, time.UTC)
	end := metav1.Date(2026, 5, 6, 12, 30, 0, 0, time.UTC)
	expected := metav1.Date(2026, 5, 6, 11, 59, 0, 0, time.UTC)
	driftSec := int32(60)

	existing := []monitoringv1alpha1.ExecutionRecord{
		{
			JobName:           "settle-1",
			StartTime:         start,
			Phase:             monitoringv1alpha1.ExecutionPhaseRunning,
			ExpectedStartTime: &expected,
			DriftSeconds:      &driftSec,
		},
	}
	incoming := []monitoringv1alpha1.ExecutionRecord{
		{
			JobName:   "settle-1",
			StartTime: start,
			EndTime:   &end,
			Phase:     monitoringv1alpha1.ExecutionPhaseSucceeded,
		},
	}

	merged := history.Merge(existing, incoming, 10)
	if len(merged) != 1 {
		t.Fatalf("Merge returned %d records, want 1", len(merged))
	}
	rec := merged[0]
	if rec.Phase != monitoringv1alpha1.ExecutionPhaseSucceeded {
		t.Errorf("Phase = %q, want Succeeded", rec.Phase)
	}
	if rec.ExpectedStartTime == nil || !rec.ExpectedStartTime.Equal(&expected) {
		t.Errorf("ExpectedStartTime = %v, want %v", rec.ExpectedStartTime, expected)
	}
	if rec.DriftSeconds == nil || *rec.DriftSeconds != driftSec {
		t.Errorf("DriftSeconds = %v, want %d", rec.DriftSeconds, driftSec)
	}
}

// A Job the operator first saw Running and that then failed arrives with the
// same StartTime and no EndTime: Kubernetes sets status.completionTime only
// when a Job succeeds. The terminal phase alone must be enough to replace the
// Running record, or the failure is never counted.
func TestMergeRunningToFailedWithoutEndTime(t *testing.T) {
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	existing := []monitoringv1alpha1.ExecutionRecord{rec("nightly-1", start, monitoringv1alpha1.ExecutionPhaseRunning)}
	incoming := []monitoringv1alpha1.ExecutionRecord{rec("nightly-1", start, monitoringv1alpha1.ExecutionPhaseFailed)}

	merged := history.Merge(existing, incoming, 10)
	if len(merged) != 1 || merged[0].Phase != monitoringv1alpha1.ExecutionPhaseFailed {
		t.Fatalf("got %+v, want one Failed record", merged)
	}
}

// The first sighting can come from a cache that has the Job but not yet its
// status, so the Running record's StartTime is the creationTimestamp
// fallback. The terminal record then carries the real, earlier
// status.startTime. Same Job, terminal phase: it must still win.
func TestMergeRunningFallbackStartToFailedWithEarlierStart(t *testing.T) {
	created := time.Date(2026, 9, 19, 12, 0, 5, 0, time.UTC)
	started := created.Add(-30 * time.Minute)
	existing := []monitoringv1alpha1.ExecutionRecord{rec("nightly-2", created, monitoringv1alpha1.ExecutionPhaseRunning)}
	incoming := []monitoringv1alpha1.ExecutionRecord{rec("nightly-2", started, monitoringv1alpha1.ExecutionPhaseFailed)}

	merged := history.Merge(existing, incoming, 10)
	if len(merged) != 1 || merged[0].Phase != monitoringv1alpha1.ExecutionPhaseFailed {
		t.Fatalf("got %+v, want one Failed record", merged)
	}
	if !merged[0].StartTime.Time.Equal(started) {
		t.Fatalf("StartTime = %v, want the Job's real start %v", merged[0].StartTime, started)
	}
}

// A stale cache can hand back a Running view of a Job this monitor already
// recorded as terminal. History must not move backwards.
func TestMergeTerminalIsNotReplacedByRunning(t *testing.T) {
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	for _, terminal := range []monitoringv1alpha1.ExecutionPhase{monitoringv1alpha1.ExecutionPhaseFailed, monitoringv1alpha1.ExecutionPhaseSucceeded} {
		existing := []monitoringv1alpha1.ExecutionRecord{rec("nightly-3", start, terminal)}
		incoming := []monitoringv1alpha1.ExecutionRecord{rec("nightly-3", start.Add(time.Second), monitoringv1alpha1.ExecutionPhaseRunning)}

		merged := history.Merge(existing, incoming, 10)
		if len(merged) != 1 || merged[0].Phase != terminal {
			t.Fatalf("%s: got %+v, want the %s record kept", terminal, merged, terminal)
		}
	}
}

// Within one phase class the older rules still decide. A Job is Succeeded as
// soon as status.succeeded > 0, which can be observed before the Complete
// condition and completionTime land; the later view with an EndTime must
// replace the earlier one (it is what yields the run's duration), and a
// stale view without one must not undo it.
func TestMergeSucceededGainsEndTimeAndKeepsIt(t *testing.T) {
	start := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	end := metav1.NewTime(start.Add(7 * time.Minute))
	early := rec("nightly-4", start, monitoringv1alpha1.ExecutionPhaseSucceeded)
	done := early
	done.EndTime = &end

	merged := history.Merge([]monitoringv1alpha1.ExecutionRecord{early}, []monitoringv1alpha1.ExecutionRecord{done}, 10)
	if merged[0].EndTime == nil {
		t.Fatalf("the view with an EndTime must replace the one without: %+v", merged[0])
	}
	merged = history.Merge(merged, []monitoringv1alpha1.ExecutionRecord{early}, 10)
	if merged[0].EndTime == nil {
		t.Fatalf("a stale view without an EndTime must not undo it: %+v", merged[0])
	}
}

// Two Running views of the same Job: the creationTimestamp fallback, then the
// real status.startTime, which is normally later. The later one wins.
func TestMergeRunningRefinesStartTime(t *testing.T) {
	created := time.Date(2026, 9, 19, 12, 0, 0, 0, time.UTC)
	started := created.Add(3 * time.Second)
	merged := history.Merge(
		[]monitoringv1alpha1.ExecutionRecord{rec("nightly-5", created, monitoringv1alpha1.ExecutionPhaseRunning)},
		[]monitoringv1alpha1.ExecutionRecord{rec("nightly-5", started, monitoringv1alpha1.ExecutionPhaseRunning)}, 10)
	if !merged[0].StartTime.Time.Equal(started) {
		t.Fatalf("StartTime = %v, want %v", merged[0].StartTime, started)
	}
	merged = history.Merge(merged,
		[]monitoringv1alpha1.ExecutionRecord{rec("nightly-5", created, monitoringv1alpha1.ExecutionPhaseRunning)}, 10)
	if !merged[0].StartTime.Time.Equal(started) {
		t.Fatalf("an earlier Running view must not replace a later one: %v", merged[0].StartTime)
	}
}
