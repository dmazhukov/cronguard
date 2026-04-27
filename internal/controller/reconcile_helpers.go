/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
	"sort"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// listOwnedJobs returns Jobs in the same namespace whose controller
// OwnerReference points at the given CronJob UID.
func listOwnedJobs(ctx context.Context, c client.Client, cj *batchv1.CronJob) ([]batchv1.Job, error) {
	var jobs batchv1.JobList
	if err := c.List(ctx, &jobs, client.InNamespace(cj.Namespace)); err != nil {
		return nil, fmt.Errorf("list jobs: %w", err)
	}
	var owned []batchv1.Job
	for i := range jobs.Items {
		for _, ref := range jobs.Items[i].OwnerReferences {
			if ref.Controller != nil && *ref.Controller && ref.UID == cj.UID {
				owned = append(owned, jobs.Items[i])
				break
			}
		}
	}
	return owned, nil
}

func jobPhase(job *batchv1.Job) monitoringv1alpha1.ExecutionPhase {
	for _, cond := range job.Status.Conditions {
		if cond.Status != corev1.ConditionTrue {
			continue
		}
		switch cond.Type {
		case batchv1.JobComplete:
			return monitoringv1alpha1.ExecutionPhaseSucceeded
		case batchv1.JobFailed:
			return monitoringv1alpha1.ExecutionPhaseFailed
		}
	}
	if job.Status.Succeeded > 0 {
		return monitoringv1alpha1.ExecutionPhaseSucceeded
	}
	if job.Status.Failed > 0 && job.Status.Active == 0 {
		return monitoringv1alpha1.ExecutionPhaseFailed
	}
	return monitoringv1alpha1.ExecutionPhaseRunning
}

func jobToRecord(job *batchv1.Job) monitoringv1alpha1.ExecutionRecord {
	rec := monitoringv1alpha1.ExecutionRecord{
		JobName: job.Name,
		Phase:   jobPhase(job),
	}
	if job.Status.StartTime != nil {
		rec.StartTime = *job.Status.StartTime
	} else {
		rec.StartTime = metav1.NewTime(job.CreationTimestamp.Time)
	}
	if job.Status.CompletionTime != nil {
		rec.EndTime = job.Status.CompletionTime
		dur := int32(job.Status.CompletionTime.Sub(rec.StartTime.Time).Seconds())
		rec.DurationSeconds = &dur
	}
	return rec
}

func sortJobsNewestFirst(jobs []batchv1.Job) {
	sort.Slice(jobs, func(i, j int) bool {
		ti := jobStartTime(&jobs[i])
		tj := jobStartTime(&jobs[j])
		return ti.After(tj.Time)
	})
}

func jobStartTime(job *batchv1.Job) metav1.Time {
	if job.Status.StartTime != nil {
		return *job.Status.StartTime
	}
	return job.CreationTimestamp
}
