package metrics

import (
	"github.com/prometheus/client_golang/prometheus"
)

// Result labels for ReconcileTotal.
const (
	ResultSuccess = "success"
	ResultError   = "error"
	ResultRequeue = "requeue"
)

// ReconcileTotal counts reconcile invocations per outcome.
var ReconcileTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cronguard_reconcile_total",
		Help: "Reconcile invocations per outcome",
	},
	[]string{"namespace", "name", "result"},
)

// ReconcileDurationSeconds observes reconcile latency.
var ReconcileDurationSeconds = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Name:    "cronguard_reconcile_duration_seconds",
		Help:    "Reconcile latency",
		Buckets: prometheus.DefBuckets,
	},
	[]string{"namespace", "name"},
)

// MissedRunsTotal counts each newly-detected missed run as a strictly
// monotonic counter. The reconciler increments it by the delta between the
// current MissedRunsSince() result and the previously-emitted value (tracked
// per-monitor in memory). Used by burn-rate alerts; the existing
// cronguard_missed_runs gauge stays for at-a-glance state.
//
// Labelled by (namespace, name) — the CronJobMonitor's own identity — not the
// referenced cronjob. This lets the deletion path clean the series up without
// needing to remember the cronjob name after the CR is gone (see M3), and
// matches the label set of the other reconcile counters. Burn-rate alerts
// aggregate rate() without the cronjob label, so this is transparent to them.
var MissedRunsTotal = prometheus.NewCounterVec(
	prometheus.CounterOpts{
		Name: "cronguard_missed_runs_total",
		Help: "Total number of missed runs observed since process start",
	},
	[]string{"namespace", "name"},
)

// MustRegister registers the reconcile counters with the given registry.
// Safe to call once during manager bootstrap.
func MustRegister(reg prometheus.Registerer) {
	reg.MustRegister(ReconcileTotal, ReconcileDurationSeconds, MissedRunsTotal)
}
