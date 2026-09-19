// Package history provides a ring-buffer merge for ExecutionRecord slices.
package history

import (
	"sort"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// Merge combines existing and incoming records into a newest-first slice
// truncated to `limit`. Records with the same JobName are deduplicated;
// shouldReplace decides which of two views of one Job survives.
func Merge(existing, incoming []monitoringv1alpha1.ExecutionRecord, limit int) []monitoringv1alpha1.ExecutionRecord {
	if limit <= 0 {
		return nil
	}
	byName := make(map[string]monitoringv1alpha1.ExecutionRecord, len(existing)+len(incoming))
	for _, r := range existing {
		byName[r.JobName] = r
	}
	for _, r := range incoming {
		prev, ok := byName[r.JobName]
		if !ok {
			byName[r.JobName] = r
			continue
		}
		if !shouldReplace(prev, r) {
			continue
		}
		// Carry forward drift annotations: jobToRecord constructs records
		// from terminal Job state and doesn't know the originally expected
		// start time. The Running snapshot computed it once; preserve.
		if r.ExpectedStartTime == nil && prev.ExpectedStartTime != nil {
			r.ExpectedStartTime = prev.ExpectedStartTime
		}
		if r.DriftSeconds == nil && prev.DriftSeconds != nil {
			r.DriftSeconds = prev.DriftSeconds
		}
		byName[r.JobName] = r
	}

	out := make([]monitoringv1alpha1.ExecutionRecord, 0, len(byName))
	for _, r := range byName {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].StartTime.Equal(&out[j].StartTime) {
			return out[i].StartTime.After(out[j].StartTime.Time)
		}
		return out[i].JobName > out[j].JobName // tie-break for determinism
	})
	if len(out) > limit {
		out = out[:limit]
	}
	return out
}

// shouldReplace returns true when the new record supersedes the existing one.
// Both carry the same JobName. The phase decides first, because it is the
// only reliable signal: Kubernetes sets status.completionTime only when a Job
// succeeds, so a failed Job's EndTime comes from its conditions and may be
// absent, and the first Running
// sighting can carry the creationTimestamp fallback while the terminal view
// carries the real, earlier status.startTime.
//
//   - A terminal view always replaces a Running one.
//   - A Running view replaces a terminal one only if it started later. Terminal
//     phases come from the Job's final conditions, which a Job never leaves,
//     so a later Running view is a new Job that reuses the name (a manual
//     `kubectl create job --from=cronjob/...` after the old one was deleted).
//   - Within one phase class, a later StartTime wins, and on equal StartTime a
//     record with an EndTime beats one without.
func shouldReplace(prev, next monitoringv1alpha1.ExecutionRecord) bool {
	prevTerminal, nextTerminal := isTerminal(prev.Phase), isTerminal(next.Phase)
	switch {
	case nextTerminal && !prevTerminal:
		return true
	case prevTerminal && !nextTerminal:
		return next.StartTime.After(prev.StartTime.Time)
	}
	if next.StartTime.After(prev.StartTime.Time) {
		return true
	}
	if next.StartTime.Equal(&prev.StartTime) && prev.EndTime == nil && next.EndTime != nil {
		return true
	}
	return false
}

func isTerminal(p monitoringv1alpha1.ExecutionPhase) bool {
	return p == monitoringv1alpha1.ExecutionPhaseSucceeded || p == monitoringv1alpha1.ExecutionPhaseFailed
}
