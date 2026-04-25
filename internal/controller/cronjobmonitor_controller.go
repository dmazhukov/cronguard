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
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/history"
	"github.com/dmazhukov/cronguard/internal/schedule"
)

// CronJobMonitorReconciler reconciles a CronJobMonitor object.
type CronJobMonitorReconciler struct {
	client.Client
	Scheme *runtime.Scheme
}

// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors,verbs=get;list;watch;create;update;patch;delete
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/status,verbs=get;update;patch
// +kubebuilder:rbac:groups=monitoring.cronguard.io,resources=cronjobmonitors/finalizers,verbs=update
// +kubebuilder:rbac:groups=batch,resources=cronjobs,verbs=get;list;watch
// +kubebuilder:rbac:groups=batch,resources=jobs,verbs=get;list;watch
// +kubebuilder:rbac:groups="",resources=events,verbs=create;patch

func (r *CronJobMonitorReconciler) Reconcile(ctx context.Context, req ctrl.Request) (ctrl.Result, error) {
	cjm := &monitoringv1alpha1.CronJobMonitor{}
	if err := r.Get(ctx, req.NamespacedName, cjm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get CronJobMonitor: %w", err)
	}

	cj := &batchv1.CronJob{}
	cjKey := types.NamespacedName{Namespace: cjm.Namespace, Name: cjm.Spec.CronJobRef.Name}
	cronJobFound := true
	if err := r.Get(ctx, cjKey, cj); err != nil {
		if !apierrors.IsNotFound(err) {
			return ctrl.Result{}, fmt.Errorf("get CronJob: %w", err)
		}
		cronJobFound = false
	}

	if !cronJobFound {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobNotFound,
			fmt.Sprintf("CronJob %q not found in namespace %q", cjKey.Name, cjKey.Namespace))
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
		return r.patchStatus(ctx, cjm)
	}

	if cj.Spec.Suspend != nil && *cj.Spec.Suspend {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobSuspended,
			"referenced CronJob has suspend=true")
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
		return r.patchStatus(ctx, cjm)
	}

	scheduleExpr := cjm.Spec.Schedule
	if scheduleExpr == "" {
		scheduleExpr = cj.Spec.Schedule
	}

	parsed, err := schedule.Parse(scheduleExpr)
	if err != nil {
		r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonInvalidSchedule,
			fmt.Sprintf("schedule %q invalid: %v", scheduleExpr, err))
		evaluateExecutionHealthy(cjm)
		evaluateDurationHealthy(cjm)
		evaluateReady(cjm)
		return r.patchStatus(ctx, cjm)
	}
	cjm.Status.ResolvedSchedule = &scheduleExpr

	owned, err := listOwnedJobs(ctx, r.Client, cj)
	if err != nil {
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
			cjm.Status.LastSuccessTime = rec.EndTime
		}
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseFailed && cjm.Status.LastFailureTime == nil {
			cjm.Status.LastFailureTime = rec.EndTime
		}
	}

	now := time.Now()
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
	missed := int32(parsed.MissedRunsSince(lastStart, now, time.Duration(cjm.Spec.GracePeriodSeconds)*time.Second))

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
	if _, err := r.patchStatus(ctx, cjm); err != nil {
		return ctrl.Result{}, err
	}
	return ctrl.Result{RequeueAfter: requeue}, nil
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

func (r *CronJobMonitorReconciler) patchStatus(ctx context.Context, cjm *monitoringv1alpha1.CronJobMonitor) (ctrl.Result, error) {
	if err := r.Status().Update(ctx, cjm); err != nil {
		if apierrors.IsConflict(err) {
			return ctrl.Result{Requeue: true}, nil
		}
		return ctrl.Result{}, fmt.Errorf("status update: %w", err)
	}
	return ctrl.Result{}, nil
}

func (r *CronJobMonitorReconciler) SetupWithManager(mgr ctrl.Manager) error {
	return ctrl.NewControllerManagedBy(mgr).
		For(&monitoringv1alpha1.CronJobMonitor{}).
		Owns(&batchv1.Job{}).
		Complete(r)
}
