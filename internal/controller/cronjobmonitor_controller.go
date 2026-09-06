/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
	"sync"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/history"
	"github.com/dmazhukov/cronguard/internal/metrics"
	"github.com/dmazhukov/cronguard/internal/schedule"
)

// Tunables. Named so the rationale lives next to the constant rather than
// scattered across the reconcile body.
const (
	// requeueAfterError is the requeue interval for early-return error paths
	// (CronJob not found, suspended, invalid schedule, invalid timezone). Short
	// enough that the operator notices recovery quickly; long enough that a
	// stuck monitor doesn't generate a reconcile every few seconds.
	requeueAfterError = 30 * time.Second

	// requeueAfterConflict requeues after a status-update ResourceVersion
	// conflict. controller-runtime's RateLimited backoff would also work; an
	// explicit short requeue gets us back faster on a plain race.
	requeueAfterConflict = time.Second

	// requeueLeadJitter is added to the next-expected-run delta so the
	// reconciler doesn't fire microseconds before the slot.
	requeueLeadJitter = 5 * time.Second

	// requeueAfterReconcileMin is the floor on the happy-path requeue
	// interval. Cron expressions with sub-minute frequencies still requeue
	// at least every minute.
	requeueAfterReconcileMin = time.Minute
)

// CronJobMonitorReconciler reconciles a CronJobMonitor object.
type CronJobMonitorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Clock    clock.PassiveClock

	// lastMissed tracks the previously-observed MissedRunsSince result per
	// monitor. The cronguard_missed_runs_total counter increments by the
	// positive delta between current and last; resets to 0 on a successful
	// run (which sets MissedRunsSince to 0). Guarded by lastMissedMu so
	// the controller stays correct under any MaxConcurrentReconciles value
	// (controller-runtime workqueue dedupes per-key, but different keys
	// reconcile in parallel goroutines).
	lastMissed   map[types.NamespacedName]int32
	lastMissedMu sync.Mutex
}

// now returns the current time via the injected Clock, or wall clock.
func (r *CronJobMonitorReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors,verbs=get;list;watch;update;patch
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile reads the CronJobMonitor, inspects the referenced CronJob and its
// Jobs, and updates status conditions and execution history accordingly.
func (r *CronJobMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	log := ctrl.LoggerFrom(ctx)
	start := r.now()
	result := metrics.ResultSuccess
	deleted := false
	defer func() {
		if deleted {
			// CR is gone: drop this key's series instead of re-creating them.
			// The increment below would otherwise resurrect exactly the series
			// the NotFound branch is trying to clean up (M3).
			metrics.ReconcileDurationSeconds.DeleteLabelValues(req.Namespace, req.Name)
			for _, res := range []string{metrics.ResultSuccess, metrics.ResultError, metrics.ResultRequeue} {
				metrics.ReconcileTotal.DeleteLabelValues(req.Namespace, req.Name, res)
			}
			return
		}
		metrics.ReconcileTotal.WithLabelValues(req.Namespace, req.Name, result).Inc()
		metrics.ReconcileDurationSeconds.WithLabelValues(req.Namespace, req.Name).Observe(r.now().Sub(start).Seconds())
	}()

	cjm := &monitoringv1alpha1.CronJobMonitor{}
	if err := r.Get(ctx, req.NamespacedName, cjm); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("CronJobMonitor deleted, nothing to reconcile")
			deleted = true
			r.lastMissedMu.Lock()
			delete(r.lastMissed, req.NamespacedName)
			r.lastMissedMu.Unlock()
			// Drop the per-monitor counter series so a churny namespace (CI,
			// preview envs) doesn't grow registry cardinality without bound.
			// The gauge collector is list-driven and self-cleans; these
			// registered vecs are not (M3). ReconcileTotal/Duration are
			// cleaned by the deferred handler (deleted=true) so its own
			// increment can't resurrect them.
			metrics.MissedRunsTotal.DeleteLabelValues(req.Namespace, req.Name)
			return ctrl.Result{}, nil
		}
		result = metrics.ResultError
		log.Error(err, "get CronJobMonitor")
		return ctrl.Result{}, fmt.Errorf("get CronJobMonitor: %w", err)
	}
	log.V(1).Info("reconciling", "cronJobRef", cjm.Spec.CronJobRef.Name, "generation", cjm.Generation)

	// Snapshot prior axis statuses for transition-event detection.
	priorReconciled := snapshotCondition(cjm.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
	priorScheduleHealthy := snapshotCondition(cjm.Status.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
	priorExecutionHealthy := snapshotCondition(cjm.Status.Conditions, monitoringv1alpha1.ConditionExecutionHealthy)
	priorDurationHealthy := snapshotCondition(cjm.Status.Conditions, monitoringv1alpha1.ConditionDurationHealthy)

	cj := &batchv1.CronJob{}
	cjKey := types.NamespacedName{Namespace: cjm.Namespace, Name: cjm.Spec.CronJobRef.Name}
	cronJobFound := true
	if err := r.Get(ctx, cjKey, cj); err != nil {
		if !apierrors.IsNotFound(err) {
			result = metrics.ResultError
			log.Error(err, "get CronJob", "cronjob", cjKey.Name)
			return ctrl.Result{}, fmt.Errorf("get CronJob: %w", err)
		}
		cronJobFound = false
	}

	if !cronJobFound {
		log.V(1).Info("CronJob not found", "cronjob", cjKey.Name)
		// The owner is gone and the garbage collector cascades its Jobs away,
		// so any Running record left in history is phantom by construction —
		// the same shape as the suspend case, and more permanent, since the
		// CronJob will not come back on its own. No Job List is needed to
		// conclude it: an empty live set is the observation.
		if names := pruneOrphanedRunning(cjm, nil, &batchv1.CronJob{}, r.now()); len(names) > 0 {
			log.V(1).Info("dropped orphaned Running records", "jobs", names, "path", "cronjob-absent")
		}
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobNotFound,
			fmt.Sprintf("CronJob %q not found in namespace %q", cjKey.Name, cjKey.Namespace))
		// CronJob may reappear; reset schedule numerics so the metric reflects
		// "we're not measuring anything right now" rather than the last value
		// from before the CronJob disappeared.
		cjm.Status.MissedRuns = 0
		cjm.Status.ScheduleDriftSeconds = 0
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonNoSchedule,
			Message:            "no schedule available",
			ObservedGeneration: cjm.Generation,
		})
		return r.finishEarlyReturn(ctx, req.NamespacedName, cjm, priorReconciled,
			corev1.EventTypeWarning, monitoringv1alpha1.ReasonCronJobNotFound,
			fmt.Sprintf("CronJob %q not found", cjKey.Name), &result)
	}

	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		log.V(1).Info("CronJob suspended", "cronjob", cjKey.Name)
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobSuspended,
			"referenced CronJob has suspend=true")
		// Spec §5.6: missed-run counter frozen on suspend (not reset).
		// Drift left as-is too; both will refresh on unsuspend.
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonSuspended,
			Message:            "CronJob is suspended; missed-run counter frozen",
			ObservedGeneration: cjm.Generation,
		})
		// A CronJob suspended mid-run is precisely the shape that strands a
		// phantom Running record: no newer run will ever arrive to push it out
		// of the ring, so without this the count stays wrong forever. The
		// freeze above is about the missed-run counter; it is not a licence to
		// keep reporting a Job as running after it is gone. A List failure
		// here is not fatal — the path's job is to report suspension.
		if owned, lerr := listOwnedJobs(ctx, r.Client, cj); lerr != nil {
			log.V(1).Info("skip orphan prune on suspend path", "err", lerr.Error())
		} else if names := pruneOrphanedRunning(cjm, owned, cj, r.now()); len(names) > 0 {
			log.V(1).Info("dropped orphaned Running records", "jobs", names, "path", "suspend")
		}
		return r.finishEarlyReturn(ctx, req.NamespacedName, cjm, priorReconciled,
			corev1.EventTypeWarning, monitoringv1alpha1.ReasonCronJobSuspended,
			"CronJob is suspended", &result)
	}

	scheduleExpr := cjm.Spec.Schedule
	scheduleOverridden := scheduleExpr != "" && scheduleExpr != cj.Spec.Schedule
	if scheduleExpr == "" {
		scheduleExpr = cj.Spec.Schedule
	}

	tzName := cjm.Spec.TimeZone
	if tzName == "" && cj.Spec.TimeZone != nil {
		tzName = *cj.Spec.TimeZone
	}
	loc := time.UTC
	resolvedTZ := "UTC"
	if tzName != "" {
		var lerr error
		loc, lerr = time.LoadLocation(tzName)
		if lerr != nil {
			log.V(1).Info("invalid timezone", "tz", tzName, "err", lerr.Error())
			r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonInvalidTimeZone,
				fmt.Sprintf("timeZone %q invalid: %v", tzName, lerr))
			cjm.Status.MissedRuns = 0
			cjm.Status.ScheduleDriftSeconds = 0
			meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
				Type:               monitoringv1alpha1.ConditionScheduleHealthy,
				Status:             metav1.ConditionUnknown,
				Reason:             monitoringv1alpha1.ReasonInvalidTimeZone,
				Message:            "timeZone failed to load",
				ObservedGeneration: cjm.Generation,
			})
			return r.finishEarlyReturn(ctx, req.NamespacedName, cjm, priorReconciled,
				corev1.EventTypeWarning, monitoringv1alpha1.ReasonInvalidTimeZone,
				fmt.Sprintf("timeZone %q invalid: %v", tzName, lerr), &result)
		}
		resolvedTZ = tzName
	}

	parsed, err := schedule.ParseInLocation(scheduleExpr, loc)
	if err != nil {
		log.V(1).Info("invalid schedule expression", "schedule", scheduleExpr, "err", err.Error())
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonInvalidSchedule,
			fmt.Sprintf("schedule %q invalid: %v", scheduleExpr, err))
		cjm.Status.MissedRuns = 0
		cjm.Status.ScheduleDriftSeconds = 0
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonInvalidSchedule,
			Message:            "schedule expression failed to parse",
			ObservedGeneration: cjm.Generation,
		})
		return r.finishEarlyReturn(ctx, req.NamespacedName, cjm, priorReconciled,
			corev1.EventTypeWarning, monitoringv1alpha1.ReasonInvalidSchedule,
			fmt.Sprintf("schedule %q invalid: %v", scheduleExpr, err), &result)
	}
	cjm.Status.ResolvedSchedule = &scheduleExpr
	cjm.Status.ResolvedTimeZone = &resolvedTZ
	log.V(2).Info("schedule resolved", "schedule", scheduleExpr, "tz", resolvedTZ)

	// Spec §5.6: warn when CronJobMonitor.spec.schedule overrides
	// CronJob.spec.schedule. Gate on observedGeneration so the warning fires
	// once per spec change, not once per reconcile (Kubernetes' built-in
	// event coalescing handles repetition within a 10-minute window, but
	// we still want to avoid the per-reconcile etcd writes).
	if scheduleOverridden && r.Recorder != nil && cjm.Status.ObservedGeneration != cjm.Generation {
		r.Recorder.Eventf(cjm, corev1.EventTypeWarning, monitoringv1alpha1.ReasonScheduleMismatch,
			"spec.schedule %q differs from CronJob.spec.schedule %q; SLO computed against spec.schedule",
			scheduleExpr, cj.Spec.Schedule)
	}

	owned, err := listOwnedJobs(ctx, r.Client, cj)
	if err != nil {
		result = metrics.ResultError
		log.Error(err, "list owned Jobs", "cronjob", cjKey.Name)
		return ctrl.Result{}, err
	}
	sortJobsNewestFirst(owned)

	incoming := make([]monitoringv1alpha1.ExecutionRecord, 0, len(owned))
	for i := range owned {
		incoming = append(incoming, jobToRecord(&owned[i]))
	}

	limit := int(cjm.Spec.HistoryLimit)
	if limit <= 0 {
		limit = 10
	}
	cjm.Status.RecentExecutions = history.Merge(cjm.Status.RecentExecutions, incoming, limit)

	updateLastObservedTimes(cjm)

	now := r.now()
	nextExpected := parsed.Next(now)
	if nextExpected.IsZero() {
		// The expression parses but matches no date the calendar has — Feb 30,
		// April 31. Kubernetes accepts those on a CronJob, and our CEL never
		// sees CronJob.spec.schedule, so this reaches us. Before the schedule
		// layer was bounded it showed up as a wedged worker or 100001 missed
		// runs; without this branch it would show up as nothing at all —
		// missed=0, drift=0, ScheduleHealthy=True, a green dashboard row for a
		// job that will never run again. A monitoring tool may report a
		// problem badly, but it may not report it as health.
		log.V(1).Info("schedule matches no calendar date", "schedule", scheduleExpr)
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonUnsatisfiableSchedule,
			fmt.Sprintf("schedule %q parses but matches no date on the calendar; it will never run", scheduleExpr))
		cjm.Status.MissedRuns = 0
		cjm.Status.ScheduleDriftSeconds = 0
		cjm.Status.NextExpectedTime = nil
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonUnsatisfiableSchedule,
			Message:            "schedule matches no date on the calendar",
			ObservedGeneration: cjm.Generation,
		})
		return r.finishEarlyReturn(ctx, req.NamespacedName, cjm, priorReconciled,
			corev1.EventTypeWarning, monitoringv1alpha1.ReasonUnsatisfiableSchedule,
			fmt.Sprintf("schedule %q matches no date on the calendar", scheduleExpr), &result)
	}
	nextT := metav1.NewTime(nextExpected)
	cjm.Status.NextExpectedTime = &nextT

	// Compute drift for the most recent run. The per-record annotation is
	// gated on the newest record actually being the run LastScheduleTime
	// refers to: that scalar is a monotone latch, so after an orphan prune or
	// a ring truncation the newest survivor can belong to an earlier slot, and
	// both writing this slot's drift onto it and clearing its own would
	// mislabel it permanently — history.Merge carries annotations forward.
	if cjm.Status.LastScheduleTime != nil {
		annotatable := len(cjm.Status.RecentExecutions) > 0 &&
			cjm.Status.RecentExecutions[0].StartTime.Equal(cjm.Status.LastScheduleTime)
		expected, found := parsed.Prev(cjm.Status.LastScheduleTime.Time)
		if found {
			drift := schedule.Drift(cjm.Status.LastScheduleTime.Time, expected)
			cjm.Status.ScheduleDriftSeconds = int32(drift.Seconds())
			if annotatable {
				rec := &cjm.Status.RecentExecutions[0]
				exp := metav1.NewTime(expected)
				rec.ExpectedStartTime = &exp
				driftSec := int32(drift.Seconds())
				rec.DriftSeconds = &driftSec
			}
		} else {
			// This start belongs to no slot within the lookback horizon.
			// Publish "not measured" rather than leaving the last real drift
			// frozen: the collector republishes that value on every scrape, so
			// a stale one ranks in the runbook's topk query forever.
			cjm.Status.ScheduleDriftSeconds = 0
			if annotatable {
				rec := &cjm.Status.RecentExecutions[0]
				rec.ExpectedStartTime = nil
				rec.DriftSeconds = nil
			}
			log.V(1).Info("expected slot unresolvable", "schedule", scheduleExpr,
				"lastScheduleTime", cjm.Status.LastScheduleTime.Time)
		}
	}

	// M4: a Job observed Running and then garbage-collected before its
	// terminal transition was seen stays Running in history forever, so
	// cronguard_running_jobs counts a job that no longer exists. Placed AFTER
	// the drift block on purpose: LastScheduleTime has already been latched
	// monotonically and RecentExecutions[0] already carries its drift
	// annotation, so pruning here cannot change any derived scalar — only the
	// running count. Only the happy path prunes; the early-return paths never
	// list Jobs and so have no live observation to justify a removal.
	if names := pruneOrphanedRunning(cjm, owned, cj, now); len(names) > 0 {
		log.V(1).Info("dropped orphaned Running records", "jobs", names)
	}

	// Missed-runs count since last observed start (or CJM creation time if no runs).
	var lastStart time.Time
	if cjm.Status.LastScheduleTime != nil {
		lastStart = cjm.Status.LastScheduleTime.Time
	} else {
		lastStart = cjm.CreationTimestamp.Time
	}
	// M5: Status.LastScheduleTime only advances from Jobs this operator saw,
	// and Kubernetes garbage-collects finished Jobs (ttlSecondsAfterFinished,
	// successfulJobsHistoryLimit). So "no Job object" is a proxy for "the slot
	// did not fire" that decays with time: after operator downtime longer than
	// the Job TTL, every slot that ran perfectly is charged as missed. Raise
	// the floor with the scheduler's own record, which is never GC'd.
	//
	// The witness is one-sided in the safe direction: kube-controller-manager
	// writes CronJob.Status.LastScheduleTime only after a Job POST succeeds,
	// and does NOT advance it on a create failure, a startingDeadlineSeconds
	// miss, or a Forbid skip — so every case a user would call a real miss
	// leaves it un-advanced and the slot still countable. When the whole
	// control plane is down the witness goes stale too, which is what keeps
	// the never-fired / operator-down detection working.
	//
	// LastSuccessfulTime is deliberately NOT admitted: it is a completion
	// time, so a long-running Forbid job would push the floor past slots its
	// own runtime suppressed and mask them.
	//
	// Only admissible when we are monitoring the CronJob's own slot grid — an
	// overridden schedule or timezone means the CronJob's slots are not ours.
	//
	// Clamped to now: the witness is only ever used to move the floor toward
	// the present, so a value past now carries no information. Without the
	// clamp, clock skew between the controller-manager's node and ours — or a
	// hand-patched status — zeroes the count for as long as it stays ahead,
	// and the gauge, the condition and the counter all derive from that same
	// suppressed value, so nothing anywhere would say so.
	if !scheduleOverridden && cjm.Spec.TimeZone == "" &&
		cj.Status.LastScheduleTime != nil &&
		cj.Status.LastScheduleTime.After(lastStart) &&
		!cj.Status.LastScheduleTime.After(now) {
		lastStart = cj.Status.LastScheduleTime.Time
	}
	missed := int32(parsed.MissedRunsSince(lastStart, now, time.Duration(cjm.Spec.GracePeriodSeconds)*time.Second)) //nolint:gosec // G115: missed-run count is bounded by reconcile cadence and fits in int32

	// Counter for burn-rate alerts: increment by the positive delta only.
	// MissedRunsSince resets to 0 when the operator observes a fresh
	// successful run (lastStart advances), so a decrease just means
	// "the missed-run streak ended" — not an event to count.
	//
	// C2: lastMissed is in-memory and starts empty on every process start and
	// leader failover. Status.MissedRuns is persisted, so on the first sight of
	// a key we seed the baseline from it rather than from an implicit 0 —
	// otherwise the post-failover delta would be the entire persisted backlog,
	// firing a spurious BurnFast (or, after a counter reset, masking a real
	// burn). Seeding makes the first post-failover delta 0; only genuinely new
	// misses observed by this process increment the counter.
	//
	// F9: this only READS the baseline. Advancing it and emitting the delta is
	// deferred to commitMissed, after the status patch lands — see the
	// invariant documented there. The read must stay above
	// evaluateScheduleHealthy, which overwrites cjm.Status.MissedRuns with
	// `missed`; reading after it would seed prev from missed and silently zero
	// every delta.
	r.lastMissedMu.Lock()
	prev, seen := r.lastMissed[req.NamespacedName]
	if !seen {
		prev = cjm.Status.MissedRuns
	}
	r.lastMissedMu.Unlock()

	evaluateScheduleHealthy(cjm, missed)
	evaluateExecutionHealthy(cjm)
	evaluateDurationHealthy(cjm)

	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionReconciled,
		Status:             metav1.ConditionTrue,
		Reason:             monitoringv1alpha1.ReasonReconcileSuccess,
		Message:            "CronJob resolved, schedule parsed",
		ObservedGeneration: cjm.Generation,
	})
	evaluateReady(cjm)
	cjm.Status.ObservedGeneration = cjm.Generation

	// Requeue at next expected run + small jitter, with a one-minute floor.
	requeue := nextExpected.Sub(r.now()) + requeueLeadJitter
	if requeue < requeueAfterReconcileMin {
		requeue = requeueAfterReconcileMin
	}
	r.emitTransitionEvents(cjm, priorReconciled, priorScheduleHealthy, priorExecutionHealthy, priorDurationHealthy)
	res, err := r.patchStatus(ctx, cjm)
	if err != nil {
		result = metrics.ResultError
		log.Error(err, "status update")
		return ctrl.Result{}, err
	}
	if res.RequeueAfter > 0 {
		result = metrics.ResultRequeue
		log.V(2).Info("requeue after status conflict", "after", res.RequeueAfter)
		return res, nil
	}
	// Status.MissedRuns is durable from here on, so the baseline may advance.
	r.commitMissed(req.NamespacedName, missed, prev)
	log.V(2).Info("reconciled", "missed", missed, "drift_s", cjm.Status.ScheduleDriftSeconds, "next_expected", nextExpected, "requeue_in", requeue)
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// finishEarlyReturn writes the remaining axis conditions, emits a
// transition-gated Reconciled event, patches status, and returns a 30s
// requeue. Common tail of the four early-return paths so the spam-gating
// + requeue policy are in one place.
func (r *CronJobMonitorReconciler) finishEarlyReturn(
	ctx context.Context,
	key types.NamespacedName,
	cjm *monitoringv1alpha1.CronJobMonitor,
	priorReconciled *metav1.Condition,
	eventType, eventReason, eventMessage string,
	result *string,
) (ctrl.Result, error) {
	evaluateExecutionHealthy(cjm)
	evaluateDurationHealthy(cjm)
	evaluateReady(cjm)

	if r.Recorder != nil && shouldEmitReconciledEvent(priorReconciled, eventReason) {
		r.Recorder.Event(cjm, eventType, eventReason, eventMessage)
	}

	res, err := r.patchStatus(ctx, cjm)
	if err != nil {
		*result = metrics.ResultError
		return ctrl.Result{}, err
	}
	if res.RequeueAfter > 0 {
		*result = metrics.ResultRequeue
		return res, nil
	}
	// Four of these paths persist Status.MissedRuns = 0 as a "not measuring"
	// marker and one (suspend) freezes it. The in-memory baseline must NOT
	// follow a persisted 0 downwards: LastScheduleTime is a monotone latch,
	// so the next happy reconcile recomputes the whole streak and would
	// re-emit every miss this process already counted (measured: +18 for 5
	// real misses across a CronJob delete/recreate). The baseline is only
	// ever raised here; see syncMissedBaseline for the trade-off.
	r.syncMissedBaseline(key, cjm.Status.MissedRuns)
	*result = metrics.ResultRequeue
	return ctrl.Result{RequeueAfter: requeueAfterError}, nil
}

// syncMissedBaseline raises the in-memory burn-counter baseline to a value
// that has just been persisted, without emitting anything. It never lowers
// it: within one process the baseline is the high-water mark of misses
// already emitted, and a transient path that persists 0 has not un-happened
// any of them. Keeping it high is what stops the recovery reconcile from
// re-emitting the streak.
//
// The accepted residual is the cross-restart window: a process that dies
// while a monitor sits on such a path leaves 0 persisted, and its successor
// seeds from that 0 and re-emits the streak once on recovery. That is the
// pre-v0.4.0 behaviour, bounded to one restart coinciding with an outage of
// the monitored CronJob; the alternative (a separate persisted checkpoint)
// is a CRD change and is deferred.
func (r *CronJobMonitorReconciler) syncMissedBaseline(key types.NamespacedName, missed int32) {
	r.lastMissedMu.Lock()
	if cur, seen := r.lastMissed[key]; !seen || missed > cur {
		r.lastMissed[key] = missed
	}
	r.lastMissedMu.Unlock()
}

// shouldEmitReconciledEvent returns true when the new event reason represents
// a transition rather than a continuation of the previous reconcile state.
// Eliminates per-reconcile event spam on stuck-state monitors (e.g., CJM
// pointing at a deleted CronJob would otherwise emit a Warning every 30s).
func shouldEmitReconciledEvent(prior *metav1.Condition, reason string) bool {
	if prior == nil {
		return true
	}
	if prior.Status != metav1.ConditionFalse {
		return true
	}
	return prior.Reason != reason
}

// updateLastObservedTimes populates LastScheduleTime / LastSuccessTime /
// LastFailureTime from the merged RecentExecutions. Values are
// monotonically non-decreasing — once we observe a success at time T, we
// never roll back below T even if T's record falls off the ring buffer
// later. This prevents `cronguard_last_success_timestamp_seconds` from
// dropping to 0 when a long failure streak pushes the last success
// out of history.
func updateLastObservedTimes(cjm *monitoringv1alpha1.CronJobMonitor) {
	for i := range cjm.Status.RecentExecutions {
		rec := &cjm.Status.RecentExecutions[i]

		startCopy := rec.StartTime
		if cjm.Status.LastScheduleTime == nil || startCopy.After(cjm.Status.LastScheduleTime.Time) {
			cjm.Status.LastScheduleTime = &startCopy
		}

		t := pickEndOrStart(rec)
		if t == nil {
			continue
		}
		switch rec.Phase {
		case monitoringv1alpha1.ExecutionPhaseSucceeded:
			if cjm.Status.LastSuccessTime == nil || t.After(cjm.Status.LastSuccessTime.Time) {
				cjm.Status.LastSuccessTime = t
			}
		case monitoringv1alpha1.ExecutionPhaseFailed:
			if cjm.Status.LastFailureTime == nil || t.After(cjm.Status.LastFailureTime.Time) {
				cjm.Status.LastFailureTime = t
			}
		}
	}
}

// pickEndOrStart returns EndTime if set, else StartTime (Failed Jobs may have nil CompletionTime).
func pickEndOrStart(rec *monitoringv1alpha1.ExecutionRecord) *metav1.Time {
	if rec.EndTime != nil {
		return rec.EndTime
	}
	t := rec.StartTime
	return &t
}

func (r *CronJobMonitorReconciler) setReconciledFalse(cjm *monitoringv1alpha1.CronJobMonitor, reason, message string) {
	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionReconciled,
		Status:             metav1.ConditionFalse,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cjm.Generation,
	})
	cjm.Status.ObservedGeneration = cjm.Generation
}

// snapshotCondition returns a deep copy of the named condition, or nil.
func snapshotCondition(conds []metav1.Condition, t string) *metav1.Condition {
	if c := meta.FindStatusCondition(conds, t); c != nil {
		cp := *c
		return &cp
	}
	return nil
}

// emitTransitionEvents compares prior axis-condition statuses against the
// post-evaluation values and emits Kubernetes events for SLO threshold
// crossings: True/Unknown -> False on the three healthy axes, and
// False -> True on Reconciled (recovery signal).
func (r *CronJobMonitorReconciler) emitTransitionEvents(
	cjm *monitoringv1alpha1.CronJobMonitor,
	priorReconciled, priorScheduleHealthy, priorExecutionHealthy, priorDurationHealthy *metav1.Condition,
) {
	if r.Recorder == nil {
		return
	}
	type axis struct {
		typ   string
		prior *metav1.Condition
	}
	axes := []axis{
		{monitoringv1alpha1.ConditionScheduleHealthy, priorScheduleHealthy},
		{monitoringv1alpha1.ConditionExecutionHealthy, priorExecutionHealthy},
		{monitoringv1alpha1.ConditionDurationHealthy, priorDurationHealthy},
	}
	for _, a := range axes {
		curr := meta.FindStatusCondition(cjm.Status.Conditions, a.typ)
		if curr == nil || curr.Status != metav1.ConditionFalse {
			continue
		}
		// Emit when prior was missing, True, or Unknown — i.e., not already False.
		if a.prior != nil && a.prior.Status == metav1.ConditionFalse {
			continue
		}
		r.Recorder.Event(cjm, corev1.EventTypeWarning, curr.Reason, curr.Message)
	}

	// Recovery: Reconciled False -> True
	if priorReconciled != nil && priorReconciled.Status == metav1.ConditionFalse {
		curr := meta.FindStatusCondition(cjm.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
		if curr != nil && curr.Status == metav1.ConditionTrue {
			r.Recorder.Event(cjm, corev1.EventTypeNormal, monitoringv1alpha1.ReasonReconcileSuccess, curr.Message)
		}
	}
}

// commitMissed advances the in-memory burn-counter baseline and emits the
// positive delta. It must be called only after Status.MissedRuns has been
// durably persisted, because that field is the checkpoint the C2 seeding path
// reads back on the first sight of a key after a restart or leader failover.
//
// The invariant is: lastMissed[key] never leads the last persisted value of
// Status.MissedRuns. Break it and a process that dies with an unpersisted
// advance lets its successor seed from the stale-lower value and re-emit a
// delta the predecessor already emitted — Prometheus reads the restart as a
// counter reset, so increase() over the window sums both. That is C2's failure
// mode at reduced magnitude, and the same spurious BurnFast.
//
// Note that a conflict is NOT an error here: patchStatus reports it as a
// RequeueAfter result, so the caller must gate on err == nil AND
// res.RequeueAfter == 0, not on err alone.
//
// The residual is a deliberate at-most-once loss: a crash between the
// successful patch and this call drops that delta for good, and the successor
// — seeding from the now-updated checkpoint — will not re-emit it. That window
// is prior to, not inside, the loss window scrape granularity already imposes;
// what carries the trade is its size, two statements with no I/O against a
// scrape interval measured in tens of seconds.
//
// Under-reporting is the direction to prefer anyway. An over-count inflates
// the burn rate and pages on a healthy monitor, and increase() cannot tell a
// phantom increment from a real one after the fact. An under-count is visible
// through cronguard_reconcile_total{result="error"} and the operator-down
// alert — note that the missed-runs gauge and the ScheduleHealthy condition
// are NOT independent witnesses here: the collector serves them from the
// manager cache, so under a sustained write failure they are exactly as stale
// as the counter is frozen.
func (r *CronJobMonitorReconciler) commitMissed(key types.NamespacedName, missed, prev int32) {
	r.lastMissedMu.Lock()
	r.lastMissed[key] = missed
	r.lastMissedMu.Unlock()
	if missed > prev {
		metrics.MissedRunsTotal.WithLabelValues(key.Namespace, key.Name).
			Add(float64(missed - prev))
	}
}

func (r *CronJobMonitorReconciler) patchStatus(ctx context.Context, cjm *monitoringv1alpha1.CronJobMonitor) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, cjm); err != nil {
		if apierrors.IsConflict(err) {
			// A non-zero RequeueAfter from this function means status was NOT
			// persisted. Callers gate durable-checkpoint work (commitMissed)
			// on it, so any future path that returns a backoff from here must
			// preserve that meaning — returning one on a successful write
			// would silently stop the burn counter.
			return ctrl.Result{RequeueAfter: requeueAfterConflict}, nil
		}
		return ctrl.Result{}, fmt.Errorf("status update: %w", err)
	}
	return ctrl.Result{}, nil
}

// mapJobToMonitors enqueues reconcile requests for any CronJobMonitor whose
// cronJobRef matches the CronJob that owns the given Job (looked up by UID
// via OwnerReferences -> CronJob.UID -> CronJob.Name).
func (r *CronJobMonitorReconciler) mapJobToMonitors(ctx context.Context, obj client.Object) []ctrl.Request {
	job, ok := obj.(*batchv1.Job)
	if !ok {
		return nil
	}
	ownerUID := ""
	for _, ref := range job.OwnerReferences {
		if ref.Controller != nil && *ref.Controller && ref.Kind == "CronJob" {
			ownerUID = string(ref.UID)
			break
		}
	}
	if ownerUID == "" {
		return nil
	}
	var cjmList monitoringv1alpha1.CronJobMonitorList
	if err := r.List(ctx, &cjmList, client.InNamespace(job.Namespace)); err != nil {
		return nil
	}
	var cjList batchv1.CronJobList
	if err := r.List(ctx, &cjList, client.InNamespace(job.Namespace)); err != nil {
		return nil
	}
	cjNameByUID := make(map[string]string, len(cjList.Items))
	for _, cj := range cjList.Items {
		cjNameByUID[string(cj.UID)] = cj.Name
	}
	ownerName := cjNameByUID[ownerUID]
	if ownerName == "" {
		return nil
	}
	var out []ctrl.Request
	for _, cjm := range cjmList.Items {
		if cjm.Spec.CronJobRef.Name == ownerName {
			out = append(out, ctrl.Request{NamespacedName: types.NamespacedName{
				Namespace: cjm.Namespace, Name: cjm.Name,
			}})
		}
	}
	return out
}

// SetupWithManager registers the reconciler with the manager, watching
// CronJobMonitor objects and enqueueing requests for related Job events.
func (r *CronJobMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	if r.lastMissed == nil {
		r.lastMissed = make(map[types.NamespacedName]int32)
	}
	return ctrl.NewControllerManagedBy(mgr).
		For(&monitoringv1alpha1.CronJobMonitor{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(r.mapJobToMonitors)).
		Complete(r)
}
