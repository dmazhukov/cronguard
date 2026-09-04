/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"context"
	"fmt"
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"
	"github.com/prometheus/client_golang/prometheus/testutil"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	clocktesting "k8s.io/utils/clock/testing"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/metrics"
)

// reconcilerOver builds a reconciler over an already-constructed client with a
// fresh (empty) lastMissed map — i.e. a newly elected leader or a restarted
// process. Sharing one client between two of these is how a failover is
// modelled without an envtest control plane.
func reconcilerOver(c client.Client, clk *clocktesting.FakePassiveClock) *CronJobMonitorReconciler {
	return &CronJobMonitorReconciler{
		Client:     c,
		Scheme:     scheme,
		Clock:      clk,
		lastMissed: make(map[types.NamespacedName]int32),
	}
}

func storeWith(objs ...client.Object) client.Client {
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		WithStatusSubresource(&monitoringv1alpha1.CronJobMonitor{}).
		Build()
}

func monitorFor(ns, name, cronjob string, created time.Time) *monitoringv1alpha1.CronJobMonitor {
	return &monitoringv1alpha1.CronJobMonitor{
		ObjectMeta: metav1.ObjectMeta{
			Name: name, Namespace: ns,
			CreationTimestamp: metav1.NewTime(created),
		},
		Spec: monitoringv1alpha1.CronJobMonitorSpec{
			CronJobRef:           monitoringv1alpha1.CronJobReference{Name: cronjob},
			AlertAfterMissedRuns: 2,
			GracePeriodSeconds:   0,
			HistoryLimit:         10,
		},
	}
}

// M4 — cronguard_running_jobs must count live Jobs, not history rows. A Job
// observed Running and then garbage-collected before its terminal transition
// was seen used to stay Running in status forever.
var _ = Describe("orphaned Running records (M4)", func() {
	const ns = "cg-m4"

	It("keeps the record while the Job is live", func() {
		base := time.Now().Add(11 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "live", "* * * * *")
		job := makeOwnedJob(ns, "live-1", cj, base.Add(-10*time.Minute))
		job.Status.StartTime = &metav1.Time{Time: base.Add(-10 * time.Minute)}
		cjm := monitorFor(ns, "live-mon", "live", base.Add(-1*time.Hour))

		r := reconcilerOver(storeWith(cj, job, cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "live-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(1), "a genuinely running Job must still be counted")
	})

	It("drops an aged Running record whose Job no longer exists", func() {
		base := time.Now().Add(12 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "gc", "* * * * *")
		cjm := monitorFor(ns, "gc-mon", "gc", base.Add(-1*time.Hour))
		cjm.Status = monitoringv1alpha1.CronJobMonitorStatus{
			LastScheduleTime: &metav1.Time{Time: base.Add(-10 * time.Minute)},
			RecentExecutions: []monitoringv1alpha1.ExecutionRecord{
				runningRec("gc-1", base.Add(-10*time.Minute)),
			},
		}

		r := reconcilerOver(storeWith(cj, cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "gc-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(0), "the Job was garbage-collected; nothing is running")
		// Derived scalars latched before the prune must be untouched.
		Expect(got.Status.LastScheduleTime).NotTo(BeNil())
		Expect(got.Status.LastScheduleTime.Time).To(BeTemporally("==", base.Add(-10*time.Minute)))
		Expect(got.Status.ConsecutiveFailures).To(BeNumerically("==", 0))
		Expect(got.Status.LastSuccessTime).To(BeNil())
	})

	It("does not clear a record younger than one reconcile cycle", func() {
		base := time.Now().Add(13 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "flap", "* * * * *")
		cjm := monitorFor(ns, "flap-mon", "flap", base.Add(-1*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("flap-1", base.Add(-30*time.Second)),
		}

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(storeWith(cj, cjm), clk)
		key := types.NamespacedName{Name: "flap-mon", Namespace: ns}

		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(1), "a transiently empty Job list must not clear a fresh record")

		// The guard only delays the repair; it must not disable it.
		clk.SetTime(base.Add(2 * time.Minute))
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(0), "once past the grace the phantom must be repaired")
	})

	It("treats CronJob.status.active as a veto on pruning", func() {
		base := time.Now().Add(14 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "veto", "* * * * *")
		cj.Status.Active = []corev1.ObjectReference{{Kind: "Job", Name: "veto-1", Namespace: ns}}
		cjm := monitorFor(ns, "veto-mon", "veto", base.Add(-1*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("veto-1", base.Add(-10*time.Minute)),
		}

		store := storeWith(cj, cjm)
		r := reconcilerOver(store, clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "veto-mon", Namespace: ns}

		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(1), "the CronJob controller still lists the Job as active")

		// Releasing the veto must let the prune proceed.
		var live batchv1.CronJob
		Expect(store.Get(ctx, types.NamespacedName{Name: "veto", Namespace: ns}, &live)).To(Succeed())
		live.Status.Active = nil
		Expect(store.Status().Update(ctx, &live)).To(Succeed())

		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(0))
	})

	It("does not mislabel a surviving record with the pruned run's drift", func() {
		// The prune removes the newest record while LastScheduleTime — a
		// monotone latch — keeps pointing at it. Without a guard the next
		// reconcile would write that slot's expected start and drift onto
		// whatever record is newest now, permanently mislabelling a terminal
		// run that history.Merge then carries forward.
		base := time.Now().Add(16 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "annot", "* * * * *")
		cjm := monitorFor(ns, "annot-mon", "annot", base.Add(-2*time.Hour))
		end := metav1.NewTime(base.Add(-69 * time.Minute))
		cjm.Status = monitoringv1alpha1.CronJobMonitorStatus{
			LastScheduleTime: &metav1.Time{Time: base.Add(-10 * time.Minute)},
			RecentExecutions: []monitoringv1alpha1.ExecutionRecord{
				runningRec("orphan-1", base.Add(-10*time.Minute)),
				{
					JobName:   "succ-1",
					StartTime: metav1.NewTime(base.Add(-70 * time.Minute)),
					EndTime:   &end,
					Phase:     monitoringv1alpha1.ExecutionPhaseSucceeded,
				},
			},
		}

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(storeWith(cj, cjm), clk)
		key := types.NamespacedName{Name: "annot-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		clk.SetTime(base.Add(1 * time.Minute))
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.RecentExecutions).To(HaveLen(1))
		Expect(got.Status.RecentExecutions[0].JobName).To(Equal("succ-1"))
		Expect(got.Status.RecentExecutions[0].ExpectedStartTime).To(BeNil(),
			"the surviving record belongs to an earlier slot and must not inherit the orphan's annotation")
		Expect(got.Status.RecentExecutions[0].DriftSeconds).To(BeNil())
	})

	It("prunes on the suspend path too", func() {
		// Suspending a CronJob mid-run is the classic way to strand a phantom:
		// nothing newer will ever arrive to push it out of the ring.
		base := time.Now().Add(19 * time.Hour).Truncate(time.Minute)
		suspend := true
		cj := makeCronJob(ns, "susp", "* * * * *")
		cj.Spec.Suspend = &suspend
		cjm := monitorFor(ns, "susp-mon", "susp", base.Add(-2*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("susp-1", base.Add(-10*time.Minute)),
		}

		r := reconcilerOver(storeWith(cj, cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "susp-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(0))
		cond := findCondition(got.Status.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Reason).To(Equal(monitoringv1alpha1.ReasonSuspended),
			"pruning must not disturb the suspend freeze")
	})

	It("does not prune when the Job list fails", func() {
		// The whole safety argument rests on a List error aborting before the
		// prune. Pinned so a refactor cannot swallow the error and then prune
		// against an empty list.
		base := time.Now().Add(20 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "listerr", "* * * * *")
		cjm := monitorFor(ns, "listerr-mon", "listerr", base.Add(-2*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("listerr-1", base.Add(-10*time.Minute)),
		}
		store := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cj, cjm).
			WithStatusSubresource(&monitoringv1alpha1.CronJobMonitor{}).
			WithInterceptorFuncs(interceptor.Funcs{
				List: func(ctx context.Context, c client.WithWatch, list client.ObjectList,
					opts ...client.ListOption) error {
					if _, ok := list.(*batchv1.JobList); ok {
						return fmt.Errorf("simulated list failure")
					}
					return c.List(ctx, list, opts...)
				},
			}).Build()

		r := reconcilerOver(store, clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "listerr-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).To(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(store.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(1), "no live observation means no removal")
	})

	It("removes only the orphan from a mixed history", func() {
		base := time.Now().Add(21 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "mixed", "* * * * *")
		job := makeOwnedJob(ns, "live-1", cj, base.Add(-2*time.Minute))
		job.Status.StartTime = &metav1.Time{Time: base.Add(-2 * time.Minute)}
		end := metav1.NewTime(base.Add(-29 * time.Minute))
		cjm := monitorFor(ns, "mixed-mon", "mixed", base.Add(-2*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("gone-1", base.Add(-10*time.Minute)),
			{
				JobName:   "ok-1",
				StartTime: metav1.NewTime(base.Add(-30 * time.Minute)),
				EndTime:   &end,
				Phase:     monitoringv1alpha1.ExecutionPhaseSucceeded,
			},
		}

		r := reconcilerOver(storeWith(cj, job, cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "mixed-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		var kept []string
		for _, rec := range got.Status.RecentExecutions {
			kept = append(kept, rec.JobName)
		}
		Expect(kept).To(Equal([]string{"live-1", "ok-1"}), "newest-first order must survive selective removal")
		Expect(got.Status.ConsecutiveFailures).To(BeNumerically("==", 0))
	})

	It("handles the history going empty at the smallest legal limit", func() {
		base := time.Now().Add(22 * time.Hour).Truncate(time.Minute)
		cj := makeCronJob(ns, "limit1", "* * * * *")
		cjm := monitorFor(ns, "limit1-mon", "limit1", base.Add(-2*time.Hour))
		cjm.Spec.HistoryLimit = 1
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("only-1", base.Add(-10*time.Minute)),
		}

		r := reconcilerOver(storeWith(cj, cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "limit1-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.RecentExecutions).To(BeEmpty())
		Expect(runningCount(got)).To(Equal(0))
		Expect(got.Status.ConsecutiveFailures).To(BeNumerically("==", 0))
	})

	It("never prunes on an early-return path that did not observe Jobs", func() {
		base := time.Now().Add(15 * time.Hour).Truncate(time.Minute)
		// No CronJob object at all: the CronJobNotFound early return runs, and
		// that path has no live observation to justify a removal.
		cjm := monitorFor(ns, "orphan-path-mon", "absent", base.Add(-1*time.Hour))
		cjm.Status.RecentExecutions = []monitoringv1alpha1.ExecutionRecord{
			runningRec("stale-1", base.Add(-10*time.Minute)),
		}

		r := reconcilerOver(storeWith(cjm), clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "orphan-path-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(runningCount(got)).To(Equal(1), "pruning without evidence is the one thing this must not do")
	})
})

// F9 — the in-memory burn-counter baseline must never lead the persisted
// Status.MissedRuns that a successor process seeds from.
var _ = Describe("burn-counter checkpoint ordering (F9)", func() {
	const ns = "cg-f9"

	// failingOnceStore returns a client whose Nth status update fails with the
	// supplied error, so a reconcile can be driven through a failed patch.
	failingOnceStore := func(failOn int, failWith error, objs ...client.Object) client.Client {
		calls := 0
		return fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(objs...).
			WithStatusSubresource(&monitoringv1alpha1.CronJobMonitor{}).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
					obj client.Object, opts ...client.SubResourceUpdateOption) error {
					calls++
					if calls == failOn {
						return failWith
					}
					return c.Status().Update(ctx, obj, opts...)
				},
			}).Build()
	}

	seed := func(base time.Time, name string) (*batchv1.CronJob, *monitoringv1alpha1.CronJobMonitor) {
		cj := makeCronJob(ns, name, "* * * * *")
		cjm := monitorFor(ns, name+"-mon", name, base.Add(-1*time.Hour))
		cjm.Status = monitoringv1alpha1.CronJobMonitorStatus{
			MissedRuns:       10,
			LastScheduleTime: &metav1.Time{Time: base.Add(-10 * time.Minute)},
		}
		return cj, cjm
	}

	// Both trigger paths are covered: a conflict (which patchStatus reports as
	// a non-error RequeueAfter result — the trap a naive `if err == nil` fix
	// walks into) and a hard apiserver error.
	DescribeTable("emits each new miss exactly once across a failed patch and a failover",
		func(name string, failWith error, expectErr bool) {
			base := time.Now().Add(17 * time.Hour).Truncate(time.Minute)
			cj, cjm := seed(base, name)
			store := failingOnceStore(2, failWith, cj, cjm)
			key := types.NamespacedName{Name: name + "-mon", Namespace: ns}
			read := func() float64 {
				return testutil.ToFloat64(metrics.MissedRunsTotal.WithLabelValues(ns, name+"-mon"))
			}
			before := read()

			clk := clocktesting.NewFakePassiveClock(base)
			r1 := reconcilerOver(store, clk)

			// (A) first reconcile settles the baseline from persisted status.
			_, err := r1.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
			Expect(read()-before).To(BeNumerically("==", 0), "C2 seeding must absorb the persisted backlog")

			// (B) three more slots elapse; the status patch fails.
			clk.SetTime(base.Add(3 * time.Minute))
			res, err := r1.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			if expectErr {
				Expect(err).To(HaveOccurred())
				Expect(res).To(Equal(ctrl.Result{}))
			} else {
				Expect(err).NotTo(HaveOccurred())
				Expect(res.RequeueAfter).To(Equal(requeueAfterConflict),
					"a conflict surfaces as a requeue result, not an error")
			}

			// The invariant itself, stated directly: the in-memory baseline
			// must not have advanced past what was actually persisted.
			var persisted monitoringv1alpha1.CronJobMonitor
			Expect(store.Get(ctx, key, &persisted)).To(Succeed())
			r1.lastMissedMu.Lock()
			inMemory := r1.lastMissed[key]
			r1.lastMissedMu.Unlock()
			Expect(inMemory).To(Equal(persisted.Status.MissedRuns),
				"lastMissed must never lead the persisted checkpoint")

			// (C) the process restarts / leadership moves; the successor seeds
			// from the persisted value and must not re-emit what was counted.
			r2 := reconcilerOver(store, clk)
			_, err = r2.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())

			Expect(read()-before).To(BeNumerically("==", 3),
				"exactly the three genuinely new misses, counted once")
		},
		Entry("status conflict", "conflict",
			apierrors.NewConflict(
				schema.GroupResource{Group: "monitoring.cronguard.io", Resource: "cronjobmonitors"},
				"conflict-mon", fmt.Errorf("simulated conflict")), false),
		Entry("apiserver error", "apierr",
			apierrors.NewInternalError(fmt.Errorf("simulated apiserver failure")), true),
	)

	It("does not re-emit a delta the predecessor already committed", func() {
		// The accepted at-most-once trade, pinned as behaviour: once the
		// checkpoint is durable the successor adds nothing, which is also what
		// makes a crash in the two statements after the patch a permanent
		// (bounded, one-shot) loss rather than a double count.
		base := time.Now().Add(23 * time.Hour).Truncate(time.Minute)
		cj, cjm := seed(base, "atmostonce")
		store := storeWith(cj, cjm)
		key := types.NamespacedName{Name: "atmostonce-mon", Namespace: ns}
		read := func() float64 {
			return testutil.ToFloat64(metrics.MissedRunsTotal.WithLabelValues(ns, "atmostonce-mon"))
		}
		before := read()

		clk := clocktesting.NewFakePassiveClock(base)
		r1 := reconcilerOver(store, clk)
		_, err := r1.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		clk.SetTime(base.Add(3 * time.Minute))
		_, err = r1.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		afterR1 := read()
		Expect(afterR1 - before).To(BeNumerically("==", 3))

		r2 := reconcilerOver(store, clk)
		_, err = r2.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(read()).To(BeNumerically("==", afterR1),
			"the successor seeds from the durable checkpoint and adds nothing")
	})

	It("emits one cumulative delta after a conflict storm", func() {
		// Misses accumulated while status writes were failing must not be
		// lost, and must not be emitted piecemeal against a moving baseline.
		base := time.Now().Add(24 * time.Hour).Truncate(time.Minute)
		cj, cjm := seed(base, "storm")
		conflict := apierrors.NewConflict(
			schema.GroupResource{Group: "monitoring.cronguard.io", Resource: "cronjobmonitors"},
			"storm-mon", fmt.Errorf("simulated conflict"))
		calls := 0
		store := fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(cj, cjm).
			WithStatusSubresource(&monitoringv1alpha1.CronJobMonitor{}).
			WithInterceptorFuncs(interceptor.Funcs{
				SubResourceUpdate: func(ctx context.Context, c client.Client, sub string,
					obj client.Object, opts ...client.SubResourceUpdateOption) error {
					calls++
					if calls >= 2 && calls <= 4 {
						return conflict
					}
					return c.Status().Update(ctx, obj, opts...)
				},
			}).Build()
		key := types.NamespacedName{Name: "storm-mon", Namespace: ns}
		read := func() float64 {
			return testutil.ToFloat64(metrics.MissedRunsTotal.WithLabelValues(ns, "storm-mon"))
		}
		before := read()

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(store, clk)
		for _, offset := range []time.Duration{0, 3 * time.Minute, 4 * time.Minute, 5 * time.Minute, 5 * time.Minute} {
			clk.SetTime(base.Add(offset))
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}
		Expect(read()-before).To(BeNumerically("==", 5),
			"one cumulative delta against the baseline that was never advanced")
	})

	It("leaves the baseline untouched when the patch conflicts", func() {
		base := time.Now().Add(25 * time.Hour).Truncate(time.Minute)
		cj, cjm := seed(base, "frozen")
		store := failingOnceStore(2, apierrors.NewConflict(
			schema.GroupResource{Group: "monitoring.cronguard.io", Resource: "cronjobmonitors"},
			"frozen-mon", fmt.Errorf("simulated conflict")), cj, cjm)
		key := types.NamespacedName{Name: "frozen-mon", Namespace: ns}

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(store, clk)
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		clk.SetTime(base.Add(3 * time.Minute))
		_, err = r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())

		r.lastMissedMu.Lock()
		defer r.lastMissedMu.Unlock()
		Expect(r.lastMissed[key]).To(BeNumerically("==", 10),
			"the baseline must still hold the last durably persisted value")
	})

	It("still counts every miss on the happy path", func() {
		base := time.Now().Add(18 * time.Hour).Truncate(time.Minute)
		cj, cjm := seed(base, "happy")
		store := storeWith(cj, cjm)
		key := types.NamespacedName{Name: "happy-mon", Namespace: ns}
		read := func() float64 {
			return testutil.ToFloat64(metrics.MissedRunsTotal.WithLabelValues(ns, "happy-mon"))
		}
		before := read()

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(store, clk)
		for _, offset := range []time.Duration{0, 3 * time.Minute, 5 * time.Minute} {
			clk.SetTime(base.Add(offset))
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			Expect(err).NotTo(HaveOccurred())
		}
		// Deferring the increment must not turn into never incrementing: this
		// fails if `prev` is ever seeded after evaluateScheduleHealthy has
		// overwritten Status.MissedRuns with `missed`.
		Expect(read()-before).To(BeNumerically("==", 5), "3 + 2 new misses")

		var persisted monitoringv1alpha1.CronJobMonitor
		Expect(store.Get(ctx, key, &persisted)).To(Succeed())
		r.lastMissedMu.Lock()
		defer r.lastMissedMu.Unlock()
		Expect(r.lastMissed[key]).To(Equal(persisted.Status.MissedRuns))
	})
})

// M5 — a slot that ran and was garbage-collected during operator downtime must
// not be charged as missed, while a slot that genuinely never fired must be.
var _ = Describe("missed-run floor across operator downtime (M5)", func() {
	const ns = "cg-m5"

	// run reconciles once against a store seeded with a CronJob whose
	// status.lastScheduleTime is cjLast (zero means "never scheduled"), and a
	// monitor that last observed a run at cjmLast.
	run := func(name, cjSchedule, monSchedule string, cjLast, cjmLast, created, now time.Time,
	) monitoringv1alpha1.CronJobMonitorStatus {
		cj := makeCronJob(ns, name, cjSchedule)
		if !cjLast.IsZero() {
			cj.Status.LastScheduleTime = &metav1.Time{Time: cjLast}
		}
		cjm := monitorFor(ns, name+"-mon", name, created)
		cjm.Spec.GracePeriodSeconds = 60
		cjm.Spec.Schedule = monSchedule
		if !cjmLast.IsZero() {
			cjm.Status.LastScheduleTime = &metav1.Time{Time: cjmLast}
		}

		store := storeWith(cj, cjm)
		r := reconcilerOver(store, clocktesting.NewFakePassiveClock(now))
		key := types.NamespacedName{Name: name + "-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		return got.Status
	}

	base := time.Date(2026, 6, 1, 12, 0, 0, 0, time.UTC)

	It("charges nothing when the scheduler kept firing through the downtime", func() {
		// Operator blind from 10:00; kube-controller-manager scheduled through
		// 11:55 and every Job was TTL-deleted before the restart.
		st := run("gcd", "*/5 * * * *", "",
			base.Add(-5*time.Minute), base.Add(-2*time.Hour), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 0),
			"23 successful runs were GC'd; none of them is a miss")
	})

	It("charges only the slots after the scheduler's own last one", func() {
		st := run("partial", "*/5 * * * *", "",
			base.Add(-15*time.Minute), base.Add(-2*time.Hour), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 2),
			"slots at -10m and -5m are past the scheduler's own floor; the horizon excludes the newest")
	})

	It("still charges every slot when the whole control plane was down", func() {
		// The witness is as stale as our own record, which is what keeps
		// operator-down / never-firing detection working.
		st := run("blackout", "*/5 * * * *", "",
			base.Add(-2*time.Hour), base.Add(-2*time.Hour), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 23))
		cond := findCondition(st.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
	})

	It("leaves the dead man's switch untouched", func() {
		// Nothing ever ran and the CronJob controller never scheduled
		// anything: both witnesses are nil, so the floor is the CR's creation.
		st := run("never", "*/5 * * * *", "",
			time.Time{}, time.Time{}, base.Add(-1*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 11),
			"slots from creation up to the grace horizon")
		cond := findCondition(st.Conditions, monitoringv1alpha1.ConditionScheduleHealthy)
		Expect(cond).NotTo(BeNil())
		Expect(cond.Status).To(Equal(metav1.ConditionFalse))
	})

	It("ignores the CronJob's witness when the monitor overrides the schedule", func() {
		// The CronJob's slot grid is not the monitored one, so its
		// lastScheduleTime is not evidence about our slots.
		st := run("override", "0,30 * * * *", "*/5 * * * *",
			base.Add(-5*time.Minute), base.Add(-30*time.Minute), base.Add(-2*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 5),
			"five */5 slots elapsed since the monitor's own last observation, within the grace horizon")
	})

	It("ignores a witness older than our own last observation", func() {
		// Pins that the merge is a max and not an unconditional assignment:
		// when the operator saw a run more recently than the scheduler's
		// record, our own observation must win.
		st := run("stale-witness", "*/5 * * * *", "",
			base.Add(-2*time.Hour), base.Add(-30*time.Minute), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 5),
			"slots from -25m to -5m; the older CronJob witness must not lower the floor")
	})

	It("accepts masking when the scheduler recovered before the operator did", func() {
		// The known limit of this fix, pinned deliberately rather than left to
		// be discovered in production. CronJob.Status.LastScheduleTime records
		// a point, not a gap: when kube-controller-manager comes back from its
		// own outage it creates a single catch-up Job and stamps the field with
		// it, erasing every slot it skipped. The floor then jumps past all of
		// them. Accepted trade — see docs/adr/0001: the over-count this fix
		// removes fires on every TTL-GC cluster after any operator restart,
		// while this under-count needs the control plane itself to fail AND
		// recover inside the operator's downtime, a window in which other
		// alerting is already firing.
		st := run("catchup", "*/5 * * * *", "",
			base.Add(-5*time.Minute), base.Add(-2*time.Hour), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 0),
			"documented under-count: the scheduler's catch-up stamp erases the gap")
	})

	It("keeps the guard conservative when a timezone is set explicitly", func() {
		// spec.timeZone set — even to a value identical to the CronJob's
		// effective zone — withholds the floor. Deliberately conservative: the
		// two slot grids are computed independently (robfig here,
		// kube-controller-manager there), so treating them as interchangeable
		// on a string match would reopen the masking path around a DST
		// transition. Do not "fix" this into a timezone-equality check.
		cj := makeCronJob(ns, "tz-guard", "*/5 * * * *")
		cj.Status.LastScheduleTime = &metav1.Time{Time: base.Add(-5 * time.Minute)}
		cjm := monitorFor(ns, "tz-guard-mon", "tz-guard", base.Add(-3*time.Hour))
		cjm.Spec.GracePeriodSeconds = 60
		cjm.Spec.TimeZone = "UTC"
		cjm.Status.LastScheduleTime = &metav1.Time{Time: base.Add(-2 * time.Hour)}

		store := storeWith(cj, cjm)
		r := reconcilerOver(store, clocktesting.NewFakePassiveClock(base))
		key := types.NamespacedName{Name: "tz-guard-mon", Namespace: ns}
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.MissedRuns).To(BeNumerically("==", 23),
			"an explicit timezone withholds the CronJob witness")
	})

	It("degrades safely when the witness is in the future", func() {
		st := run("skew", "*/5 * * * *", "",
			base.Add(10*time.Minute), base.Add(-2*time.Hour), base.Add(-3*time.Hour), base)
		Expect(st.MissedRuns).To(BeNumerically("==", 0), "clock skew must not produce a negative count")
	})
})

func runningCount(cjm monitoringv1alpha1.CronJobMonitor) int {
	n := 0
	for _, rec := range cjm.Status.RecentExecutions {
		if rec.Phase == monitoringv1alpha1.ExecutionPhaseRunning {
			n++
		}
	}
	return n
}

// M6 — a schedule that parses but can never fire (Feb 30) used to hang the
// reconcile worker inside Prev. Once bounded, the reconciler must also stop
// republishing a drift it can no longer compute.
var _ = Describe("unresolvable expected slot (M6)", func() {
	const ns = "cg-m6"

	It("clears drift instead of freezing the last real value", func() {
		base := time.Date(2026, 6, 10, 0, 0, 0, 0, time.UTC)
		cj := makeCronJob(ns, "impossible", "0 0 30 2 *")
		cjm := monitorFor(ns, "impossible-mon", "impossible", base.Add(-30*24*time.Hour))
		drift := int32(63)
		expected := metav1.NewTime(base.Add(-9 * 24 * time.Hour))
		cjm.Status = monitoringv1alpha1.CronJobMonitorStatus{
			ScheduleDriftSeconds: 63,
			LastScheduleTime:     &metav1.Time{Time: base.Add(-9 * 24 * time.Hour)},
			RecentExecutions: []monitoringv1alpha1.ExecutionRecord{{
				JobName:           "impossible-1",
				StartTime:         metav1.NewTime(base.Add(-9 * 24 * time.Hour)),
				Phase:             monitoringv1alpha1.ExecutionPhaseSucceeded,
				ExpectedStartTime: &expected,
				DriftSeconds:      &drift,
			}},
		}

		clk := clocktesting.NewFakePassiveClock(base)
		r := reconcilerOver(storeWith(cj, cjm), clk)
		key := types.NamespacedName{Name: "impossible-mon", Namespace: ns}

		done := make(chan error, 1)
		go func() {
			_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
			done <- err
		}()
		Eventually(done, 10*time.Second).Should(Receive(BeNil()),
			"reconcile must terminate on a schedule that can never fire")

		var got monitoringv1alpha1.CronJobMonitor
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.ScheduleDriftSeconds).To(BeNumerically("==", 0),
			"a drift that can no longer be computed must not stay frozen at its last real value")
		Expect(got.Status.MissedRuns).To(BeNumerically("==", 0),
			"a schedule that can never fire misses nothing")
		Expect(got.Status.RecentExecutions).To(HaveLen(1))
		Expect(got.Status.RecentExecutions[0].ExpectedStartTime).To(BeNil())
		Expect(got.Status.RecentExecutions[0].DriftSeconds).To(BeNil())

		// history.Merge carries drift annotations forward; once cleared they
		// must stay cleared rather than being resurrected on the next pass.
		clk.SetTime(base.Add(2 * time.Minute))
		_, err := r.Reconcile(ctx, reconcile.Request{NamespacedName: key})
		Expect(err).NotTo(HaveOccurred())
		Expect(r.Get(ctx, key, &got)).To(Succeed())
		Expect(got.Status.ScheduleDriftSeconds).To(BeNumerically("==", 0))
		Expect(got.Status.RecentExecutions[0].DriftSeconds).To(BeNil())
	})
})
