/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import "testing"

// The worker count is the operator's only knob for how many monitors it can
// reconcile at once; a monitor whose reconcile is slow (a dense schedule with
// a long missed-run floor) otherwise holds the only worker. Zero means the
// field was never set and must keep the historical single worker.
func TestControllerOptionsWorkerCount(t *testing.T) {
	for _, tc := range []struct{ set, want int }{{0, 1}, {1, 1}, {4, 4}} {
		r := &CronJobMonitorReconciler{MaxConcurrentReconciles: tc.set}
		if got := r.controllerOptions().MaxConcurrentReconciles; got != tc.want {
			t.Errorf("MaxConcurrentReconciles=%d: got %d workers, want %d", tc.set, got, tc.want)
		}
	}
}
