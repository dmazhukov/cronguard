package metrics_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	monitoringv1alpha1 "github.com/dmazhukov/cronguard/api/v1alpha1"
	"github.com/dmazhukov/cronguard/internal/metrics"
)

type stubLister struct {
	items []monitoringv1alpha1.CronJobMonitor
}

func (s stubLister) List() []monitoringv1alpha1.CronJobMonitor { return s.items }

func TestCollectorEmitsBusinessMetrics(t *testing.T) {
	success := metav1.NewTime(time.Date(2026, 4, 24, 2, 5, 0, 0, time.UTC))
	schedule := "0 2 * * *"
	cjm := monitoringv1alpha1.CronJobMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
		Spec: monitoringv1alpha1.CronJobMonitorSpec{
			CronJobRef: monitoringv1alpha1.CronJobReference{Name: "demo-cj"},
		},
		Status: monitoringv1alpha1.CronJobMonitorStatus{
			ResolvedSchedule:     &schedule,
			LastSuccessTime:      &success,
			ConsecutiveFailures:  0,
			MissedRuns:           0,
			ScheduleDriftSeconds: 5,
			Conditions: []metav1.Condition{{
				Type: monitoringv1alpha1.ConditionReady, Status: metav1.ConditionTrue,
				Reason: monitoringv1alpha1.ReasonAllChecksPass,
			}},
		},
	}

	c := metrics.NewCollector(stubLister{items: []monitoringv1alpha1.CronJobMonitor{cjm}})
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := fmt.Sprintf(`
# HELP cronguard_consecutive_failures Consecutive failed runs
# TYPE cronguard_consecutive_failures gauge
cronguard_consecutive_failures{cronjob="demo-cj",name="demo",namespace="ns1"} 0
# HELP cronguard_last_success_timestamp_seconds Unix time of last successful Job; 0 if never
# TYPE cronguard_last_success_timestamp_seconds gauge
cronguard_last_success_timestamp_seconds{cronjob="demo-cj",name="demo",namespace="ns1"} %d
# HELP cronguard_missed_runs Count of consecutive missed runs
# TYPE cronguard_missed_runs gauge
cronguard_missed_runs{cronjob="demo-cj",name="demo",namespace="ns1"} 0
# HELP cronguard_schedule_drift_seconds Drift of the most recent run in seconds
# TYPE cronguard_schedule_drift_seconds gauge
cronguard_schedule_drift_seconds{cronjob="demo-cj",name="demo",namespace="ns1"} 5
`, success.Unix())
	names := []string{
		"cronguard_consecutive_failures",
		"cronguard_last_success_timestamp_seconds",
		"cronguard_missed_runs",
		"cronguard_schedule_drift_seconds",
	}
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), names...); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorEmitsConditionValue(t *testing.T) {
	cjm := monitoringv1alpha1.CronJobMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
		Spec:       monitoringv1alpha1.CronJobMonitorSpec{CronJobRef: monitoringv1alpha1.CronJobReference{Name: "demo-cj"}},
		Status: monitoringv1alpha1.CronJobMonitorStatus{
			Conditions: []metav1.Condition{
				{Type: monitoringv1alpha1.ConditionReady, Status: metav1.ConditionTrue, Reason: monitoringv1alpha1.ReasonAllChecksPass},
				{Type: monitoringv1alpha1.ConditionExecutionHealthy, Status: metav1.ConditionFalse, Reason: monitoringv1alpha1.ReasonConsecutiveFailures},
			},
		},
	}
	reg := prometheus.NewRegistry()
	c := metrics.NewCollector(stubLister{items: []monitoringv1alpha1.CronJobMonitor{cjm}})
	reg.MustRegister(c)

	expected := `
# HELP cronguard_condition Condition value: 1 (True), 0 (False), -1 (Unknown)
# TYPE cronguard_condition gauge
cronguard_condition{cronjob="demo-cj",name="demo",namespace="ns1",reason="AllChecksPass",type="Ready"} 1
cronguard_condition{cronjob="demo-cj",name="demo",namespace="ns1",reason="ConsecutiveFailures",type="ExecutionHealthy"} 0
`
	if err := testutil.CollectAndCompare(c, strings.NewReader(expected), "cronguard_condition"); err != nil {
		t.Fatal(err)
	}
}

func TestCollectorEmitsRemainingMetrics(t *testing.T) {
	failure := metav1.NewTime(time.Date(2026, 4, 24, 1, 0, 0, 0, time.UTC))
	next := metav1.NewTime(time.Date(2026, 4, 24, 4, 0, 0, 0, time.UTC))
	dur := int32(120)
	cjm := monitoringv1alpha1.CronJobMonitor{
		ObjectMeta: metav1.ObjectMeta{Name: "demo", Namespace: "ns1"},
		Spec:       monitoringv1alpha1.CronJobMonitorSpec{CronJobRef: monitoringv1alpha1.CronJobReference{Name: "demo-cj"}},
		Status: monitoringv1alpha1.CronJobMonitorStatus{
			LastFailureTime:  &failure,
			NextExpectedTime: &next,
			RecentExecutions: []monitoringv1alpha1.ExecutionRecord{
				{JobName: "newest", StartTime: metav1.Now(), Phase: monitoringv1alpha1.ExecutionPhaseRunning},
				{JobName: "older", StartTime: metav1.Now(), DurationSeconds: &dur, Phase: monitoringv1alpha1.ExecutionPhaseSucceeded},
			},
		},
	}

	c := metrics.NewCollector(stubLister{items: []monitoringv1alpha1.CronJobMonitor{cjm}})
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	expected := fmt.Sprintf(`
# HELP cronguard_last_duration_seconds Duration of last completed Job in seconds
# TYPE cronguard_last_duration_seconds gauge
cronguard_last_duration_seconds{cronjob="demo-cj",name="demo",namespace="ns1"} 120
# HELP cronguard_last_failure_timestamp_seconds Unix time of last failed Job; 0 if never
# TYPE cronguard_last_failure_timestamp_seconds gauge
cronguard_last_failure_timestamp_seconds{cronjob="demo-cj",name="demo",namespace="ns1"} %d
# HELP cronguard_next_expected_timestamp_seconds Unix time of next expected run
# TYPE cronguard_next_expected_timestamp_seconds gauge
cronguard_next_expected_timestamp_seconds{cronjob="demo-cj",name="demo",namespace="ns1"} %d
# HELP cronguard_running_jobs Currently-running Jobs owned by the watched CronJob
# TYPE cronguard_running_jobs gauge
cronguard_running_jobs{cronjob="demo-cj",name="demo",namespace="ns1"} 1
`, failure.Unix(), next.Unix())

	if err := testutil.CollectAndCompare(c, strings.NewReader(expected),
		"cronguard_last_duration_seconds",
		"cronguard_last_failure_timestamp_seconds",
		"cronguard_next_expected_timestamp_seconds",
		"cronguard_running_jobs",
	); err != nil {
		t.Fatal(err)
	}
}
