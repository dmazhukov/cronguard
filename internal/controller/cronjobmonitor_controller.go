/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"

	batchv1 "k8s.io/api/batch/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/history"
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
	logger := log.FromContext(ctx).WithValues("cronjobmonitor", req.NamespacedName)

	cjm := &monitoringv1alpha1.CronJobMonitor{}
	if err := r.Get(ctx, req.NamespacedName, cjm); err != nil {
		if apierrors.IsNotFound(err) {
			return ctrl.Result{}, nil
		}
		return ctrl.Result{}, fmt.Errorf("get CronJobMonitor: %w", err)
	}

	cj := &batchv1.CronJob{}
	cjKey := types.NamespacedName{Namespace: cjm.Namespace, Name: cjm.Spec.CronJobRef.Name}
	if err := r.Get(ctx, cjKey, cj); err != nil {
		if apierrors.IsNotFound(err) {
			r.setReconciledFalse(cjm, monitoringv1alpha1.ReasonCronJobNotFound,
				fmt.Sprintf("CronJob %q not found in namespace %q", cjKey.Name, cjKey.Namespace))
			return r.patchStatus(ctx, cjm)
		}
		return ctrl.Result{}, fmt.Errorf("get CronJob: %w", err)
	}

	_ = logger

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

	// Populate last-success / last-failure / last-schedule timestamps.
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

	evaluateExecutionHealthy(cjm)
	evaluateDurationHealthy(cjm)

	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionReconciled,
		Status:             metav1.ConditionTrue,
		Reason:             monitoringv1alpha1.ReasonReconcileSuccess,
		Message:            "CronJob resolved",
		ObservedGeneration: cjm.Generation,
	})
	evaluateReady(cjm)
	cjm.Status.ObservedGeneration = cjm.Generation
	return r.patchStatus(ctx, cjm)
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
