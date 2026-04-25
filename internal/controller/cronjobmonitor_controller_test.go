/*
Copyright 2026 Dmitrii Zhukov.
Licensed under the Apache License, Version 2.0.
*/

package controller

import (
	"time"

	. "github.com/onsi/ginkgo/v2"
	. "github.com/onsi/gomega"

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
