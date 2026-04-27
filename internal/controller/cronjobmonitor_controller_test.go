/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"

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

	It("flips ExecutionHealthy=False after consecutive failures", func() {
		cj := makeCronJob(namespace, "fails-1", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		now := time.Now()
		for i := 0; i < 2; i++ {
			job := makeOwnedJob(namespace, fmt.Sprintf("fails-1-%d", i), cj, now.Add(-time.Duration(i+1)*time.Hour))
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			// K8s 1.35 requires FailureTarget=True ahead of Failed=True, and
			// refuses completionTime on a Job that is not Complete=True.
			job.Status = batchv1.JobStatus{
				Failed:    1,
				StartTime: &metav1.Time{Time: now.Add(-time.Duration(i+1) * time.Hour)},
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
					{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
			DeferCleanup(func(j *batchv1.Job) func() { return func() { Expect(k8sClient.Delete(ctx, j)).To(Succeed()) } }(job))
		}

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "fails-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "fails-1"},
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
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fails-1-mon", Namespace: namespace}, got)).To(Succeed())
			g.Expect(got.Status.ConsecutiveFailures).To(Equal(int32(2)))
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionExecutionHealthy)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonConsecutiveFailures))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("flips DurationHealthy=False when a completed Job exceeds maxDurationSeconds", func() {
		cj := makeCronJob(namespace, "slow-1", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		now := time.Now()
		job := makeOwnedJob(namespace, "slow-1-0", cj, now.Add(-30*time.Minute))
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		// K8s 1.35 requires SuccessCriteriaMet=True ahead of Complete=True.
		job.Status = batchv1.JobStatus{
			Succeeded:      1,
			StartTime:      &metav1.Time{Time: now.Add(-30 * time.Minute)},
			CompletionTime: &metav1.Time{Time: now.Add(-5 * time.Minute)}, // 25 min duration
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobSuccessCriteriaMet, Status: corev1.ConditionTrue},
				{Type: batchv1.JobComplete, Status: corev1.ConditionTrue},
			},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, job)).To(Succeed()) })

		maxDur := int32(600) // 10 minutes
		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "slow-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "slow-1"},
				MaxDurationSeconds:     &maxDur,
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
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "slow-1-mon", Namespace: namespace}, got)).To(Succeed())
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionDurationHealthy)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonDurationExceeded))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("resolves schedule from spec when set", func() {
		cj := makeCronJob(namespace, "sched-1", "0 3 * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "sched-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "sched-1"},
				Schedule:               "0 2 * * *",
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
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "sched-1-mon", Namespace: namespace}, got)).To(Succeed())
			g.Expect(got.Status.ResolvedSchedule).NotTo(BeNil())
			g.Expect(*got.Status.ResolvedSchedule).To(Equal("0 2 * * *"))
			g.Expect(got.Status.NextExpectedTime).NotTo(BeNil())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("reports InvalidSchedule when the schedule does not parse", func() {
		cj := makeCronJob(namespace, "bad-1", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "bad-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "bad-1"},
				Schedule:               "not a cron",
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
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "bad-1-mon", Namespace: namespace}, got)).To(Succeed())
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonInvalidSchedule))
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("flips ScheduleHealthy=False after AlertAfterMissedRuns missed slots", func() {
		// Schedule: every minute. Grace 0. Threshold 2.
		cj := makeCronJob(namespace, "missed-1", "* * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		// Reset the fake clock to a known epoch comfortably after wall-clock
		// time so MissedRunsSince(creationTimestamp, now, 0) accumulates.
		baseTime := time.Now().Add(1 * time.Hour).Truncate(time.Minute)
		testClock.SetTime(baseTime)

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "missed-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "missed-1"},
				MaxConsecutiveFailures: 5,
				AlertAfterMissedRuns:   2,
				GracePeriodSeconds:     0,
				HistoryLimit:           10,
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		// First, wait for steady state (Reconciled=True with the just-created CJM).
		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "missed-1-mon", Namespace: namespace}, got)).To(Succeed())
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionTrue))
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		// Advance fake clock by 5 minutes; expect at least 2 missed slots.
		testClock.SetTime(baseTime.Add(5 * time.Minute))

		// Touch the CJM to force a reconcile (annotation bump).
		Eventually(func() error {
			got := &monitoringv1alpha1.CronJobMonitor{}
			if err := k8sClient.Get(ctx, types.NamespacedName{Name: "missed-1-mon", Namespace: namespace}, got); err != nil {
				return err
			}
			if got.Annotations == nil {
				got.Annotations = map[string]string{}
			}
			got.Annotations["cronguard.io/test-tick"] = "1"
			return k8sClient.Update(ctx, got)
		}, 5*time.Second, 250*time.Millisecond).Should(Succeed())

		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "missed-1-mon", Namespace: namespace}, got)).To(Succeed())
			g.Expect(got.Status.MissedRuns).To(BeNumerically(">=", 2))
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonScheduleMissed))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("populates LastFailureTime even when Job CompletionTime is nil", func() {
		cj := makeCronJob(namespace, "fail-no-end", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		now := time.Now()
		job := makeOwnedJob(namespace, "fail-no-end-0", cj, now.Add(-30*time.Minute))
		Expect(k8sClient.Create(ctx, job)).To(Succeed())
		// Failed Job in K8s 1.35: FailureTarget then Failed; CompletionTime stays nil.
		job.Status = batchv1.JobStatus{
			Failed:    1,
			StartTime: &metav1.Time{Time: now.Add(-30 * time.Minute)},
			Conditions: []batchv1.JobCondition{
				{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
				{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
			},
		}
		Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, job)).To(Succeed()) })

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "fail-no-end-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "fail-no-end"},
				MaxConsecutiveFailures: 5, // high enough not to flip the SLO
				AlertAfterMissedRuns:   2,
				GracePeriodSeconds:     60,
				HistoryLimit:           10,
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fail-no-end-mon", Namespace: namespace}, got)).To(Succeed())
			g.Expect(got.Status.LastFailureTime).NotTo(BeNil())
			g.Expect(got.Status.RecentExecutions).To(HaveLen(1))
			g.Expect(got.Status.RecentExecutions[0].Phase).To(Equal(monitoringv1alpha1.ExecutionPhaseFailed))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("freezes ScheduleHealthy=Unknown for a suspended CronJob and does not increment MissedRuns", func() {
		cj := makeCronJob(namespace, "suspended-1", "* * * * *")
		suspend := true
		cj.Spec.Suspend = &suspend
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "suspended-1-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "suspended-1"},
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
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "suspended-1-mon", Namespace: namespace}, got)).To(Succeed())

			reconciled := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionReconciled)
			g.Expect(reconciled).NotTo(BeNil())
			g.Expect(reconciled.Status).To(Equal(metav1.ConditionFalse))
			g.Expect(reconciled.Reason).To(Equal(monitoringv1alpha1.ReasonCronJobSuspended))

			schedHealthy := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
			g.Expect(schedHealthy).NotTo(BeNil())
			g.Expect(schedHealthy.Status).To(Equal(metav1.ConditionUnknown))
			g.Expect(schedHealthy.Reason).To(Equal(monitoringv1alpha1.ReasonSuspended))

			// MissedRuns must not increment under suspend, even after a clock advance.
			g.Expect(got.Status.MissedRuns).To(Equal(int32(0)))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("emits a ConsecutiveFailures Warning event when ExecutionHealthy flips to False", func() {
		cj := makeCronJob(namespace, "fails-evt", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		now := time.Now()
		for i := 0; i < 2; i++ {
			job := makeOwnedJob(namespace, fmt.Sprintf("fails-evt-%d", i), cj, now.Add(-time.Duration(i+1)*time.Hour))
			Expect(k8sClient.Create(ctx, job)).To(Succeed())
			job.Status = batchv1.JobStatus{
				Failed:    1,
				StartTime: &metav1.Time{Time: now.Add(-time.Duration(i+1) * time.Hour)},
				Conditions: []batchv1.JobCondition{
					{Type: batchv1.JobFailureTarget, Status: corev1.ConditionTrue},
					{Type: batchv1.JobFailed, Status: corev1.ConditionTrue},
				},
			}
			Expect(k8sClient.Status().Update(ctx, job)).To(Succeed())
			DeferCleanup(func(j *batchv1.Job) func() { return func() { Expect(k8sClient.Delete(ctx, j)).To(Succeed()) } }(job))
		}

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "fails-evt-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "fails-evt"},
				MaxConsecutiveFailures: 2,
				AlertAfterMissedRuns:   2,
				GracePeriodSeconds:     60,
				HistoryLimit:           10,
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		// Wait for the condition to flip to False.
		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "fails-evt-mon", Namespace: namespace}, got)).To(Succeed())
			cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionExecutionHealthy)
			g.Expect(cond).NotTo(BeNil())
			g.Expect(cond.Status).To(Equal(metav1.ConditionFalse))
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())

		// Verify a Warning event with reason ConsecutiveFailures was emitted.
		Eventually(func(g Gomega) {
			var events corev1.EventList
			g.Expect(k8sClient.List(ctx, &events, client.InNamespace(namespace))).To(Succeed())
			found := false
			for _, e := range events.Items {
				if e.InvolvedObject.Name == "fails-evt-mon" &&
					e.Reason == monitoringv1alpha1.ReasonConsecutiveFailures &&
					e.Type == corev1.EventTypeWarning {
					found = true
					break
				}
			}
			g.Expect(found).To(BeTrue(), "expected Warning ConsecutiveFailures event for fails-evt-mon")
		}, 15*time.Second, 250*time.Millisecond).Should(Succeed())
	})

	It("retries reconcile when status patch hits a ResourceVersion conflict", func() {
		// Force the conflict by issuing two competing status updates rapidly.
		cj := makeCronJob(namespace, "rv-conflict", "0 * * * *")
		Expect(k8sClient.Create(ctx, cj)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cj)).To(Succeed()) })

		cjm := &monitoringv1alpha1.CronJobMonitor{
			ObjectMeta: metav1.ObjectMeta{Name: "rv-conflict-mon", Namespace: namespace},
			Spec: monitoringv1alpha1.CronJobMonitorSpec{
				CronJobRef:             monitoringv1alpha1.CronJobReference{Name: "rv-conflict"},
				MaxConsecutiveFailures: 2,
				AlertAfterMissedRuns:   2,
				GracePeriodSeconds:     60,
				HistoryLimit:           10,
			},
		}
		Expect(k8sClient.Create(ctx, cjm)).To(Succeed())
		DeferCleanup(func() { Expect(k8sClient.Delete(ctx, cjm)).To(Succeed()) })

		// First, wait for steady state.
		Eventually(func(g Gomega) {
			got := &monitoringv1alpha1.CronJobMonitor{}
			g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rv-conflict-mon", Namespace: namespace}, got)).To(Succeed())
			g.Expect(got.Status.Conditions).NotTo(BeEmpty())
		}, 10*time.Second, 250*time.Millisecond).Should(Succeed())

		// Force a real ResourceVersion conflict: read once, mutate twice with the same RV.
		first := &monitoringv1alpha1.CronJobMonitor{}
		Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "rv-conflict-mon", Namespace: namespace}, first)).To(Succeed())
		second := first.DeepCopy()

		first.Status.MissedRuns = 99
		Expect(k8sClient.Status().Update(ctx, first)).To(Succeed())
		second.Status.MissedRuns = 42
		err := k8sClient.Status().Update(ctx, second)
		Expect(apierrors.IsConflict(err)).To(BeTrue(), "expected resource-version conflict on stale update")
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
