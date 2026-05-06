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
	defer func() {
		metrics.ReconcileTotal.WithLabelValues(req.Namespace, req.Name, result).Inc()
		metrics.ReconcileDurationSeconds.WithLabelValues(req.Namespace, req.Name).Observe(r.now().Sub(start).Seconds())
	}()

	cjm := &monitoringv1alpha1.CronJobMonitor{}
	if err := r.Get(ctx, req.NamespacedName, cjm); err != nil {
		if apierrors.IsNotFound(err) {
			log.V(1).Info("CronJobMonitor deleted, nothing to reconcile")
			r.lastMissedMu.Lock()
			delete(r.lastMissed, req.NamespacedName)
			r.lastMissedMu.Unlock()
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
		return r.finishEarlyReturn(ctx, cjm, priorReconciled,
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
		return r.finishEarlyReturn(ctx, cjm, priorReconciled,
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
			return r.finishEarlyReturn(ctx, cjm, priorReconciled,
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
		return r.finishEarlyReturn(ctx, cjm, priorReconciled,
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
	nextT := metav1.NewTime(nextExpected)
	cjm.Status.NextExpectedTime = &nextT

	// Compute drift for the most recent run.
	if cjm.Status.LastScheduleTime != nil {
		expected := parsed.Prev(cjm.Status.LastScheduleTime.Time)
		if !expected.IsZero() {
			drift := schedule.Drift(cjm.Status.LastScheduleTime.Time, expected)
			cjm.Status.ScheduleDriftSeconds = int32(drift.Seconds())
			if len(cjm.Status.RecentExecutions) > 0 {
				rec := &cjm.Status.RecentExecutions[0]
				exp := metav1.NewTime(expected)
				rec.ExpectedStartTime = &exp
				driftSec := int32(drift.Seconds())
				rec.DriftSeconds = &driftSec
			}
		}
	}

	// Missed-runs count since last observed start (or CJM creation time if no runs).
	var lastStart time.Time
	if cjm.Status.LastScheduleTime != nil {
		lastStart = cjm.Status.LastScheduleTime.Time
	} else {
		lastStart = cjm.CreationTimestamp.Time
	}
	missed := int32(parsed.MissedRunsSince(lastStart, now, time.Duration(cjm.Spec.GracePeriodSeconds)*time.Second)) //nolint:gosec // G115: missed-run count is bounded by reconcile cadence and fits in int32

	// Counter for burn-rate alerts: increment by the positive delta only.
	// MissedRunsSince resets to 0 when the operator observes a fresh
	// successful run (lastStart advances), so a decrease just means
	// "the missed-run streak ended" — not an event to count.
	r.lastMissedMu.Lock()
	prev := r.lastMissed[req.NamespacedName]
	r.lastMissed[req.NamespacedName] = missed
	r.lastMissedMu.Unlock()
	if missed > prev {
		metrics.MissedRunsTotal.WithLabelValues(req.Namespace, req.Name, cj.Name).
			Add(float64(missed - prev))
	}

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
	log.V(2).Info("reconciled", "missed", missed, "drift_s", cjm.Status.ScheduleDriftSeconds, "next_expected", nextExpected, "requeue_in", requeue)
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// finishEarlyReturn writes the remaining axis conditions, emits a
// transition-gated Reconciled event, patches status, and returns a 30s
// requeue. Common tail of the four early-return paths so the spam-gating
// + requeue policy are in one place.
func (r *CronJobMonitorReconciler) finishEarlyReturn(
	ctx context.Context,
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
	*result = metrics.ResultRequeue
	return ctrl.Result{RequeueAfter: requeueAfterError}, nil
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

func (r *CronJobMonitorReconciler) patchStatus(ctx context.Context, cjm *monitoringv1alpha1.CronJobMonitor) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, cjm); err != nil {
		if apierrors.IsConflict(err) {
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
