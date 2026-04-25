/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

var _ = Describe("CronJobMonitor controller", func() {
	const namespace = "default"

	It("reports CronJobNotFound when the referenced CronJob does not exist", func() {
		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "missing-ref", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef: monitoringv1alpha1.CronJobReference{Name: "does-not-exist"},
				Schedule:   "0 * * * *",
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "missing-ref", Namespace: namespace,
			}, got)).To(Succeed())

			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonCronJobNotFound))
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("records a succeeded Job in recentExecutions and sets lastSuccessTime", func() {
		cj := makeCronJob(namespace, "settle-1", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		job := makeOwnedJob(namespace, "settle-1-123", cj, time.Now().Add(-10*time.Minute))
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, job)).To(Succeed()) })

		// Mark the Job as succeeded. Note: the SuccessCriteriaMet condition
		// is required by the Kubernetes 1.35 API server before Complete=True
		// may be set on Job status.
		job.Status = batchv1.JobStatus{
			Succeeded:      1,
			StartTime:      &metav1.Time{Time: time.Now().Add(-10 * time.Minute)},
			CompletionTime: &metav1.Time{Time: time.Now().Add(-8 * time.Minute)},
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "settle-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "settle-1"},
				MaxConsecutiveFailures: 2,
				AlertAfterMissedRuns:   2,
				GracePeriodSeconds:     60,
				HistoryLimit:           10,
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{
				Name: "settle-1-mon", Namespace: namespace,
			}, got)).To(Succeed())
			g.Expect(got.Status.RecentExecutions).To(HaveLen(1))
			g.Expect(got.Status.RecentExecutions[0].Phase).To(Equal(monitoringv1alpha1.ExecutionPhaseSucceeded))
			g.Expect(got.Status.LastSuccessTime).NotTo(BeNil())
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})
})

// findCondition is a test helper.
func findCondition(conds []metav1.Condition, t string) *metav1.Condition {
	for i := range conds {
		if conds[i].Type == t {
			return &conds[i]
		}
	}
	return nil
}

func makeCronJob(ns, name, schedule string) *batchv1.CronJob {
	return &batchv1.CronJob{
		ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		Spec: batchv1.CronJobSpec{
			Schedule: schedule,
			JobTemplate: batchv1.JobTemplateSpec{
				Spec: batchv1.JobSpec{
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							RestartPolicy: corev1.RestartPolicyOnFailure,
							Containers: []corev1.Container{{
								Name:  "main",
								Image: "busybox",
							}},
						},
					},
				},
			},
		},
	}
}

func makeOwnedJob(ns, name string, owner *batchv1.CronJob, start time.Time) *batchv1.Job {
	controller := true
	return &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: "batch/v1",
				Kind:       "CronJob",
				Name:       owner.Name,
				UID:        owner.UID,
				Controller: &controller,
			}},
		},
		Spec: batchv1.JobSpec{
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					RestartPolicy: corev1.RestartPolicyOnFailure,
					Containers: []corev1.Container{{
						Name:  "main",
						Image: "busybox",
					}},
				},
			},
		},
	}
}
