/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"fmt"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// evaluateExecutionHealthy counts terminal failures in a row from the newest
// record backwards. ExecutionHealthy flips to False once the count reaches
// MaxConsecutiveFailures.
func evaluateExecutionHealthy(cjm *monitoringv1alpha1.CronJobMonitor) {
	var count int32
	sawAny := false
	for _, rec := range cjm.Status.RecentExecutions {
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseRunning {
			continue
		}
		sawAny = true
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseSucceeded {
			break
		}
		count++
	}
	cjm.Status.ConsecutiveFailures = count

	if !sawAny {
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionExecutionHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonNoRuns,
			Message:            "no completed runs observed yet",
			ObservedGeneration: cjm.Generation,
		})
		return
	}
	if count >= cjm.Spec.MaxConsecutiveFailures && cjm.Spec.MaxConsecutiveFailures > 0 {
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionExecutionHealthy,
			Status:             metav1.ConditionFalse,
			Reason:             monitoringv1alpha1.ReasonConsecutiveFailures,
			Message:            fmt.Sprintf("%d consecutive failures (threshold %d)", count, cjm.Spec.MaxConsecutiveFailures),
			ObservedGeneration: cjm.Generation,
		})
		return
	}
	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionExecutionHealthy,
		Status:             metav1.ConditionTrue,
		Reason:             monitoringv1alpha1.ReasonRecentSuccess,
		Message:            "last completed run succeeded",
		ObservedGeneration: cjm.Generation,
	})
}

// evaluateDurationHealthy examines the most recently completed (non-running) run.
func evaluateDurationHealthy(cjm *monitoringv1alpha1.CronJobMonitor) {
	if cjm.Spec.MaxDurationSeconds == nil {
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionDurationHealthy,
			Status:             metav1.ConditionUnknown,
			Reason:             monitoringv1alpha1.ReasonCheckDisabled,
			Message:            "maxDurationSeconds is unset",
			ObservedGeneration: cjm.Generation,
		})
		return
	}
	for _, rec := range cjm.Status.RecentExecutions {
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseRunning {
			continue
		}
		if rec.DurationSeconds == nil {
			continue
		}
		if *rec.DurationSeconds > *cjm.Spec.MaxDurationSeconds {
			meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
				Type:               monitoringv1alpha1.ConditionDurationHealthy,
				Status:             metav1.ConditionFalse,
				Reason:             monitoringv1alpha1.ReasonDurationExceeded,
				Message:            fmt.Sprintf("last run took %ds (budget %ds)", *rec.DurationSeconds, *cjm.Spec.MaxDurationSeconds),
				ObservedGeneration: cjm.Generation,
			})
			return
		}
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionDurationHealthy,
			Status:             metav1.ConditionTrue,
			Reason:             monitoringv1alpha1.ReasonWithinBudget,
			Message:            fmt.Sprintf("last run took %ds (budget %ds)", *rec.DurationSeconds, *cjm.Spec.MaxDurationSeconds),
			ObservedGeneration: cjm.Generation,
		})
		return
	}
	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionDurationHealthy,
		Status:             metav1.ConditionUnknown,
		Reason:             monitoringv1alpha1.ReasonNoRuns,
		Message:            "no completed runs observed yet",
		ObservedGeneration: cjm.Generation,
	})
}

// evaluateReady aggregates the four axis conditions into Ready.
// Any False -> Ready=False; any Unknown -> Ready=Unknown; all True -> Ready=True.
func evaluateReady(cjm *monitoringv1alpha1.CronJobMonitor) {
	types := []string{
		monitoringv1alpha1.ConditionReconciled,
		monitoringv1alpha1.ConditionScheduleHealthy,
		monitoringv1alpha1.ConditionExecutionHealthy,
		monitoringv1alpha1.ConditionDurationHealthy,
	}
	status := metav1.ConditionTrue
	reason := monitoringv1alpha1.ReasonAllChecksPass
	message := "all SLO checks pass"
	for _, t := range types {
		cond := meta.FindStatusCondition(cjm.Status.Conditions, t)
		if cond == nil {
			continue
		}
		if cond.Status == metav1.ConditionFalse {
			status = metav1.ConditionFalse
			reason = cond.Reason
			message = fmt.Sprintf("%s: %s", t, cond.Message)
			break
		}
		if cond.Status == metav1.ConditionUnknown && status == metav1.ConditionTrue {
			status = metav1.ConditionUnknown
			reason = cond.Reason
			message = fmt.Sprintf("%s: %s", t, cond.Message)
		}
	}
	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionReady,
		Status:             status,
		Reason:             reason,
		Message:            message,
		ObservedGeneration: cjm.Generation,
	})
}

// evaluateScheduleHealthy inspects LastScheduleTime / NextExpectedTime / MissedRuns
// and sets ScheduleHealthy accordingly.
// It is called only after a schedule has been successfully parsed.
func evaluateScheduleHealthy(cjm *monitoringv1alpha1.CronJobMonitor, missed int32) {
	cjm.Status.MissedRuns = missed

	if cjm.Spec.AlertAfterMissedRuns > 0 && missed >= cjm.Spec.AlertAfterMissedRuns {
		meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
			Type:               monitoringv1alpha1.ConditionScheduleHealthy,
			Status:             metav1.ConditionFalse,
			Reason:             monitoringv1alpha1.ReasonScheduleMissed,
			Message:            fmt.Sprintf("%d missed runs (threshold %d)", missed, cjm.Spec.AlertAfterMissedRuns),
			ObservedGeneration: cjm.Generation,
		})
		return
	}
	meta.SetStatusCondition(&cjm.Status.Conditions, metav1.Condition{
		Type:               monitoringv1alpha1.ConditionScheduleHealthy,
		Status:             metav1.ConditionTrue,
		Reason:             monitoringv1alpha1.ReasonOnSchedule,
		Message:            "runs landing within schedule + grace",
		ObservedGeneration: cjm.Generation,
	})
}
