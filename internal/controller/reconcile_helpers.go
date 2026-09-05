/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
	"sort"
	"time"

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

// orphanPruneMinAge is the minimum age a Running record must reach before its
// absence from the live Job set is believed.
//
// It is a narrow guard, and worth describing honestly rather than dressing up:
// the periodic requeue is already at least a minute, so on the periodic path
// this delays nothing. What it buys is the Watch path — a Job event can drive
// a reconcile within seconds of a Job being created, and this stops a
// not-yet-listed brand-new Job from being read as a vanished one. It is
// deliberately NOT derived from spec.gracePeriodSeconds: that is a tolerance
// for schedule lateness, has no causal link to how long after a Job vanishes
// we should believe it gone, and is legal up to 86399 — which would silently
// delay phantom cleanup by most of a day.
//
// The real safety comes from elsewhere: a failed Job List aborts the reconcile
// before any prune, and a record can only enter history from a live sighting.
const orphanPruneMinAge = time.Minute

// liveJobNames unions two independently-written views of which Jobs exist
// now: the Job informer's list, and the set kube-controller-manager still
// publishes in CronJob.status.active. status.active is used only as a veto —
// its presence withholds a prune, its absence never asserts that a Job is
// gone — so a garbage-collected Job (absent from both) is still prunable.
func liveJobNames(owned []batchv1.Job, cj *batchv1.CronJob) map[string]struct{} {
	live := make(map[string]struct{}, len(owned)+len(cj.Status.Active))
	for i := range owned {
		live[owned[i].Name] = struct{}{}
	}
	for _, ref := range cj.Status.Active {
		live[ref.Name] = struct{}{}
	}
	return live
}

// dropOrphanedRunning removes Running records whose Job is absent from the
// live set and which are older than minAge, returning the filtered slice and
// the names dropped. Terminal records are never touched.
//
// history.Merge is a union keyed by JobName: a record it has already seen can
// only leave the ring by being pushed out by newer runs. A Job observed
// mid-flight and then deleted before its terminal transition was seen
// therefore stays Running forever, and cronguard_running_jobs counts it. The
// live set is the only evidence of that deletion, and the reconciler already
// holds it — so the reconciliation happens here rather than in the collector,
// which stays list-driven over status.
//
// Removal rather than re-marking is deliberate: ExecutionPhase is a closed
// enum with no honest value for "terminated, outcome unobserved". Writing
// Failed would poison ConsecutiveFailures on a run that may have succeeded;
// writing Succeeded would fabricate a success. The truthful action is to stop
// asserting a phase we cannot know.
func dropOrphanedRunning(
	recs []monitoringv1alpha1.ExecutionRecord,
	live map[string]struct{},
	now time.Time,
	minAge time.Duration,
) ([]monitoringv1alpha1.ExecutionRecord, []string) {
	var dropped []string
	out := make([]monitoringv1alpha1.ExecutionRecord, 0, len(recs))
	for _, rec := range recs {
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseRunning {
			if _, alive := live[rec.JobName]; !alive && now.Sub(rec.StartTime.Time) >= minAge {
				dropped = append(dropped, rec.JobName)
				continue
			}
		}
		out = append(out, rec)
	}
	if dropped == nil {
		return recs, nil
	}
	return out, dropped
}

// pruneOrphanedRunning reconciles a monitor's history against the live Job set
// and reports which records it dropped. The caller owns the logging so the two
// reconcile paths that need it can say where the prune happened.
func pruneOrphanedRunning(
	cjm *monitoringv1alpha1.CronJobMonitor,
	owned []batchv1.Job,
	cj *batchv1.CronJob,
	now time.Time,
) []string {
	pruned, names := dropOrphanedRunning(
		cjm.Status.RecentExecutions, liveJobNames(owned, cj), now, orphanPruneMinAge)
	cjm.Status.RecentExecutions = pruned
	return names
}
