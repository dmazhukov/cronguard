// Package metrics implements a Prometheus custom collector for CronJobMonitor.
package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
)

// Lister returns a cached list of CronJobMonitor objects.
type Lister interface {
	List() []monitoringv1alpha1.CronJobMonitor
}

// Collector is a Prometheus custom collector that reads from a Lister at scrape time.
type Collector struct {
	lister Lister

	lastSuccess  *prometheus.Desc
	lastFailure  *prometheus.Desc
	lastSchedule *prometheus.Desc
	nextExpected *prometheus.Desc
	consecFails  *prometheus.Desc
	missedRuns   *prometheus.Desc
	drift        *prometheus.Desc
	lastDuration *prometheus.Desc
	runningJobs  *prometheus.Desc
	condition    *prometheus.Desc
}

var baseLabels = []string{"namespace", "name", "cronjob"}

// NewCollector constructs a Collector backed by the given lister.
func NewCollector(l Lister) *Collector {
	return &Collector{
		lister: l,
		lastSuccess: prometheus.NewDesc(
			"cronguard_last_success_timestamp_seconds",
			"Unix time of last successful Job; 0 if never",
			baseLabels, nil),
		lastFailure: prometheus.NewDesc(
			"cronguard_last_failure_timestamp_seconds",
			"Unix time of last failed Job; 0 if never",
			baseLabels, nil),
		lastSchedule: prometheus.NewDesc(
			"cronguard_last_schedule_timestamp_seconds",
			"Unix time of last Job start (success or failure)",
			baseLabels, nil),
		nextExpected: prometheus.NewDesc(
			"cronguard_next_expected_timestamp_seconds",
			"Unix time of next expected run",
			baseLabels, nil),
		consecFails: prometheus.NewDesc(
			"cronguard_consecutive_failures",
			"Consecutive failed runs",
			baseLabels, nil),
		missedRuns: prometheus.NewDesc(
			"cronguard_missed_runs",
			"Count of consecutive missed runs",
			baseLabels, nil),
		drift: prometheus.NewDesc(
			"cronguard_schedule_drift_seconds",
			"Drift of the most recent run in seconds",
			baseLabels, nil),
		lastDuration: prometheus.NewDesc(
			"cronguard_last_duration_seconds",
			"Duration of last completed Job in seconds",
			baseLabels, nil),
		runningJobs: prometheus.NewDesc(
			"cronguard_running_jobs",
			"Currently-running Jobs owned by the watched CronJob",
			baseLabels, nil),
		condition: prometheus.NewDesc(
			"cronguard_condition",
			"Condition value: 1 (True), 0 (False), -1 (Unknown)",
			append(baseLabels, "type", "reason"), nil),
	}
}

// Describe implements prometheus.Collector.
func (c *Collector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.lastSuccess
	ch <- c.lastFailure
	ch <- c.lastSchedule
	ch <- c.nextExpected
	ch <- c.consecFails
	ch <- c.missedRuns
	ch <- c.drift
	ch <- c.lastDuration
	ch <- c.runningJobs
	ch <- c.condition
}

// Collect implements prometheus.Collector.
func (c *Collector) Collect(ch chan<- prometheus.Metric) {
	for _, cjm := range c.lister.List() {
		labels := []string{cjm.Namespace, cjm.Name, cjm.Spec.CronJobRef.Name}
		ch <- prometheus.MustNewConstMetric(c.lastSuccess, prometheus.GaugeValue, tsValue(cjm.Status.LastSuccessTime), labels...)
		ch <- prometheus.MustNewConstMetric(c.lastFailure, prometheus.GaugeValue, tsValue(cjm.Status.LastFailureTime), labels...)
		ch <- prometheus.MustNewConstMetric(c.lastSchedule, prometheus.GaugeValue, tsValue(cjm.Status.LastScheduleTime), labels...)
		ch <- prometheus.MustNewConstMetric(c.nextExpected, prometheus.GaugeValue, tsValue(cjm.Status.NextExpectedTime), labels...)
		ch <- prometheus.MustNewConstMetric(c.consecFails, prometheus.GaugeValue, float64(cjm.Status.ConsecutiveFailures), labels...)
		ch <- prometheus.MustNewConstMetric(c.missedRuns, prometheus.GaugeValue, float64(cjm.Status.MissedRuns), labels...)
		ch <- prometheus.MustNewConstMetric(c.drift, prometheus.GaugeValue, float64(cjm.Status.ScheduleDriftSeconds), labels...)

		for _, rec := range cjm.Status.RecentExecutions {
			if rec.DurationSeconds != nil {
				ch <- prometheus.MustNewConstMetric(c.lastDuration, prometheus.GaugeValue, float64(*rec.DurationSeconds), labels...)
				break
			}
		}

		var running float64
		for _, rec := range cjm.Status.RecentExecutions {
			if rec.Phase == monitoringv1alpha1.ExecutionPhaseRunning {
				running++
			}
		}
		ch <- prometheus.MustNewConstMetric(c.runningJobs, prometheus.GaugeValue, running, labels...)

		for _, cond := range cjm.Status.Conditions {
			ch <- prometheus.MustNewConstMetric(
				c.condition,
				prometheus.GaugeValue,
				conditionValue(cond.Status),
				append(labels, cond.Type, cond.Reason)...,
			)
		}
	}
}

func tsValue(t *metav1.Time) float64 {
	if t == nil {
		return 0
	}
	return float64(t.Unix())
}

func conditionValue(s metav1.ConditionStatus) float64 {
	switch s {
	case metav1.ConditionTrue:
		return 1
	case metav1.ConditionFalse:
		return 0
	default:
		return -1
	}
}
