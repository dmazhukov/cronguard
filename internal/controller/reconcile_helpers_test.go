/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

func runningRec(name string, start time.Time) monitoringv1alpha1.ExecutionRecord {
	return monitoringv1alpha1.ExecutionRecord{
		JobName:   name,
		StartTime: metav1.NewTime(start),
		Phase:     monitoringv1alpha1.ExecutionPhaseRunning,
	}
}

func names(recs []monitoringv1alpha1.ExecutionRecord) []string {
	out := make([]string, 0, len(recs))
	for _, r := range recs {
		out = append(out, r.JobName)
	}
	return out
}

func TestDropOrphanedRunning(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	succeeded := monitoringv1alpha1.ExecutionRecord{
		JobName:   "ok-1",
		StartTime: metav1.NewTime(now.Add(-1 * time.Hour)),
		Phase:     monitoringv1alpha1.ExecutionPhaseSucceeded,
	}
	failed := monitoringv1alpha1.ExecutionRecord{
		JobName:   "bad-1",
		StartTime: metav1.NewTime(now.Add(-30 * time.Minute)),
		Phase:     monitoringv1alpha1.ExecutionPhaseFailed,
	}

	cases := []struct {
		name        string
		recs        []monitoringv1alpha1.ExecutionRecord
		live        map[string]struct{}
		minAge      time.Duration
		wantKept    []string
		wantDropped []string
		why         string
	}{
		{
			name:     "live record is kept regardless of age",
			recs:     []monitoringv1alpha1.ExecutionRecord{runningRec("job-a", now.Add(-10*time.Minute))},
			live:     map[string]struct{}{"job-a": {}},
			minAge:   time.Minute,
			wantKept: []string{"job-a"},
			why:      "the Job is in the live set, so it is genuinely running",
		},
		{
			name:        "aged record absent from the live set is dropped",
			recs:        []monitoringv1alpha1.ExecutionRecord{runningRec("gone-a", now.Add(-10*time.Minute))},
			live:        map[string]struct{}{},
			minAge:      time.Minute,
			wantKept:    nil,
			wantDropped: []string{"gone-a"},
			why:         "this is the phantom: Job garbage-collected before its terminal state was seen",
		},
		{
			name:     "fresh record absent from the live set is kept",
			recs:     []monitoringv1alpha1.ExecutionRecord{runningRec("fresh-a", now.Add(-30*time.Second))},
			live:     map[string]struct{}{},
			minAge:   time.Minute,
			wantKept: []string{"fresh-a"},
			why:      "a momentarily empty or stale cache must not clear a record younger than one reconcile cycle",
		},
		{
			name:        "boundary at exactly minAge drops",
			recs:        []monitoringv1alpha1.ExecutionRecord{runningRec("edge-a", now.Add(-time.Minute))},
			live:        map[string]struct{}{},
			minAge:      time.Minute,
			wantKept:    nil,
			wantDropped: []string{"edge-a"},
			why:         "the predicate is >=, not >; pinned so a refactor cannot silently flip it",
		},
		{
			name:     "terminal records are never touched",
			recs:     []monitoringv1alpha1.ExecutionRecord{succeeded, failed},
			live:     map[string]struct{}{},
			minAge:   time.Minute,
			wantKept: []string{"ok-1", "bad-1"},
			why:      "only unresolved Running records are in question; real history must survive",
		},
		{
			name: "mixed slice keeps order of survivors",
			recs: []monitoringv1alpha1.ExecutionRecord{
				runningRec("live-1", now.Add(-10*time.Minute)),
				runningRec("gone-1", now.Add(-10*time.Minute)),
				succeeded,
			},
			live:        map[string]struct{}{"live-1": {}},
			minAge:      time.Minute,
			wantKept:    []string{"live-1", "ok-1"},
			wantDropped: []string{"gone-1"},
			why:         "newest-first ordering from history.Merge must be preserved",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gotRecs, gotDropped := dropOrphanedRunning(tc.recs, tc.live, now, tc.minAge)
			if got := names(gotRecs); !equalStrings(got, tc.wantKept) {
				t.Errorf("kept = %v, want %v (%s)", got, tc.wantKept, tc.why)
			}
			if !equalStrings(gotDropped, tc.wantDropped) {
				t.Errorf("dropped = %v, want %v (%s)", gotDropped, tc.wantDropped, tc.why)
			}
		})
	}
}

func TestOrphanPruneMinAgeIsIndependentOfGracePeriod(t *testing.T) {
	// Pinned so nobody re-couples orphan belief to schedule-lateness
	// tolerance: gracePeriodSeconds is legal up to 86399, and wiring it in
	// here would let a sloppy-scheduler setting delay phantom cleanup by most
	// of a day.
	if orphanPruneMinAge != time.Minute {
		t.Errorf("orphanPruneMinAge = %s, want 1m", orphanPruneMinAge)
	}
}

func TestLiveJobNamesUnionsInformerAndStatusActive(t *testing.T) {
	cj := &batchv1.CronJob{
		Status: batchv1.CronJobStatus{
			Active: []corev1.ObjectReference{{Kind: "Job", Name: "j2"}},
		},
	}
	owned := []batchv1.Job{{ObjectMeta: metav1.ObjectMeta{Name: "j1"}}}

	live := liveJobNames(owned, cj)
	if len(live) != 2 {
		t.Fatalf("live = %v, want exactly j1 and j2", live)
	}
	for _, want := range []string{"j1", "j2"} {
		if _, ok := live[want]; !ok {
			t.Errorf("live is missing %q — status.active must participate as a second view", want)
		}
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
