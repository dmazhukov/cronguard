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
		if !ok || shouldReplace(prev, r) {
			byName[r.JobName] = r
		}
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
// Later StartTime wins; on equal StartTime, a record with EndTime beats one
// without (a completed Job supersedes a Running snapshot).
func shouldReplace(prev, next monitoringv1alpha1.ExecutionRecord) bool {
	if next.StartTime.After(prev.StartTime.Time) {
		return true
	}
	if next.StartTime.Equal(&prev.StartTime) && prev.EndTime == nil && next.EndTime != nil {
		return true
	}
	return false
}
