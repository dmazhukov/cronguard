// Package history provides a ring-buffer merge for ExecutionRecord slices.
package history

import (
	"sort"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// Merge combines existing and incoming records into a newest-first slice
// truncated to `limit`. Records with the same JobName are deduplicated;
// the record with the later StartTime (ties broken by a non-nil EndTime) wins.
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
// Both describe the same Job (Merge keys by JobName), so the phase decides
// first: a terminal view always replaces a Running one, and a Running view
// never replaces a terminal one. The phase is the only reliable signal here —
// Kubernetes sets status.completionTime only when a Job succeeds, so a failed
// Job never gains an EndTime, and the first Running sighting can carry the
// creationTimestamp fallback while the terminal view carries the real, earlier
// status.startTime. Within the same phase class, a later StartTime wins, and
// on equal StartTime a record with an EndTime beats one without.
func shouldReplace(prev, next monitoringv1alpha1.ExecutionRecord) bool {
	prevTerminal, nextTerminal := isTerminal(prev.Phase), isTerminal(next.Phase)
	if nextTerminal != prevTerminal {
		return nextTerminal
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
