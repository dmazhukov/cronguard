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
