/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
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

// CronJobMonitorReconciler reconciles a CronJobMonitor object.
type CronJobMonitorReconciler struct {
	client.Client
	Scheme   *runtime.Scheme
	Recorder record.EventRecorder
	Clock    clock.PassiveClock
}

// now returns the current time, using the injected Clock if set,
// else the real wall clock. Allows deterministic tests.
func (r *CronJobMonitorReconciler) now() time.Time {
	if r.Clock != nil {
		return r.Clock.Now()
	}
	return time.Now()
}

// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

// Reconcile reads the CronJobMonitor, inspects the referenced CronJob and its
// Jobs, and updates status conditions and execution history accordingly.
func (r *CronJobMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	start := time.Now()
	result := metrics.ResultSuccess
	defer func() {
		metrics.ReconcileTotal.WithLabelValues(req.Namespace, req.Name, result).Inc()
		metrics.ReconcileDurationSeconds.WithLabelValues(req.Namespace, req.Name).Observe(time.Since(start).Seconds())
	}()

	cjm := &monitoringv1alpha1.CronJobMonitor{}
	if err := r.Get(ctx, req.NamespacedName, cjm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		result = metrics.ResultError
		return ctrl.Result{}, fmt.Errorf("get CronJobMonitor: %w", err)
	}

	// Snapshot prior axis-condition statuses so we can detect threshold
	// crossings after evaluators run and emit one-shot Kubernetes Events.
	// We deep-copy because meta.SetStatusCondition mutates the underlying
	// slice elements in place when the condition already exists, which
	// would alias these pointers to the post-mutation values.
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
			return ctrl.Result{}, fmt.Errorf("get CronJob: %w", err)
		}
		cronJobFound = false
	}

	if !cronJobFound {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobNotFound,
			fmt.Sprintf("CronJob %q not found in namespace %q", cjKey.Name, cjKey.Namespace))
		if r.Recorder != nil {
			r.Recorder.Event(cjm, corev1.EventTypeWarning, monitoringv1alpha1.ReasonCronJobNotFound,
				fmt.Sprintf("CronJob %q not found", cjKey.Name))
		}
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonNoSchedule,
			Message:            "no schedule available",
			ObservedGeneration: cjm.Generation,
		})
		evaluateExecutionHealthy(cjm)
		evaluateDurationHealthy(cjm)
		evaluateReady(cjm)
		res, err := r.patchStatus(ctx, cjm)
		if err != nil {
			result = metrics.ResultError
		} else if res.RequeueAfter > 0 {
			result = metrics.ResultRequeue
		}
		return res, err
	}

	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobSuspended,
			"referenced CronJob has suspend=true")
		if r.Recorder != nil {
			r.Recorder.Event(cjm, corev1.EventTypeWarning, monitoringv1alpha1.ReasonCronJobSuspended,
				"CronJob is suspended")
		}
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonSuspended,
			Message:            "CronJob is suspended; missed-run counter frozen",
			ObservedGeneration: cjm.Generation,
		})
		evaluateExecutionHealthy(cjm)
		evaluateDurationHealthy(cjm)
		evaluateReady(cjm)
		res, err := r.patchStatus(ctx, cjm)
		if err != nil {
			result = metrics.ResultError
		} else if res.RequeueAfter > 0 {
			result = metrics.ResultRequeue
		}
		return res, err
	}

	scheduleExpr := cjm.Spec.Schedule
	if scheduleExpr == "" {
		scheduleExpr = cj.Spec.Schedule
	}

	parsed, err := schedule.Parse(scheduleExpr)
	if err != nil {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonInvalidSchedule,
			fmt.Sprintf("schedule %q invalid: %v", scheduleExpr, err))
		if r.Recorder != nil {
			r.Recorder.Event(cjm, corev1.EventTypeWarning, monitoringv1alpha1.ReasonInvalidSchedule,
				fmt.Sprintf("schedule %q invalid: %v", scheduleExpr, err))
		}
		evaluateExecutionHealthy(cjm)
		evaluateDurationHealthy(cjm)
		evaluateReady(cjm)
		res, err := r.patchStatus(ctx, cjm)
		if err != nil {
			result = metrics.ResultError
		} else if res.RequeueAfter > 0 {
			result = metrics.ResultRequeue
		}
		return res, err
	}
	cjm.Status.ResolvedSchedule = &scheduleExpr

	owned, err := listOwnedJobs(ctx, r.Client, cj)
	if err != nil {
		result = metrics.ResultError
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

	cjm.Status.LastSuccessTime = nil
	cjm.Status.LastFailureTime = nil
	cjm.Status.LastScheduleTime = nil
	for i := range cjm.Status.RecentExecutions {
		rec := &cjm.Status.RecentExecutions[i]
		if cjm.Status.LastScheduleTime == nil {
			t := rec.StartTime
			cjm.Status.LastScheduleTime = &t
		}
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseSucceeded && cjm.Status.LastSuccessTime == nil {
			cjm.Status.LastSuccessTime = pickEndOrStart(rec)
		}
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseFailed && cjm.Status.LastFailureTime == nil {
			cjm.Status.LastFailureTime = pickEndOrStart(rec)
		}
	}

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

	// Requeue at next expected run + small buffer, or in 1 minute at minimum.
	requeue := time.Until(nextExpected) + 5*time.Second
	if requeue < time.Minute {
		requeue = time.Minute
	}
	r.emitTransitionEvents(cjm, priorReconciled, priorScheduleHealthy, priorExecutionHealthy, priorDurationHealthy)
	res, err := r.patchStatus(ctx, cjm)
	if err != nil {
		result = metrics.ResultError
		return ctrl.Result{}, err
	}
	if res.RequeueAfter > 0 {
		result = metrics.ResultRequeue
		return res, nil
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
}

// pickEndOrStart returns the record's EndTime if set, else its StartTime.
// Used for LastSuccess/LastFailure timestamps; in Kubernetes 1.35+ Failed
// Jobs can have nil CompletionTime, so we fall back to StartTime to ensure
// the metrics reflect that a terminal observation occurred.
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

// snapshotCondition returns a deep copy of the named condition, or nil if
// it is not present. The returned pointer is decoupled from the live
// status slice, so subsequent meta.SetStatusCondition calls do not alias
// its fields.
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
			return ctrl.Result{RequeueAfter: time.Second}, nil
		}
		return ctrl.Result{}, fmt.Errorf("status update: %w", err)
	}
	return ctrl.Result{}, nil
}

// SetupWithManager registers the reconciler with the manager, watching
// CronJobMonitor objects and enqueueing requests for related Job events.
func (r *CronJobMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	mapFn := func(ctx context.Context, obj client.Object) []ctrl.Request {
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

	return ctrl.NewControllerManagedBy(mgr).
		For(&monitoringv1alpha1.CronJobMonitor{}).
		Watches(&batchv1.Job{}, handler.EnqueueRequestsFromMapFunc(mapFn)).
		Complete(r)
}
