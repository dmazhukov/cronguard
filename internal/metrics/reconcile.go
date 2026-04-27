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

// MustRegister registers the reconcile counters with the given registry.
// Safe to call once during manager bootstrap.
func MustRegister(reg prometheus.Registerer) {
	reg.MustRegister(ReconcileTotal, ReconcileDurationSeconds)
}
