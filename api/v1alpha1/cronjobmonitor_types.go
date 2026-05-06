/*
Copyright 2026 Dmitrii Zhukov.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package v1alpha1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CronJobReference is a namespace-local reference to a batch/v1 CronJob.
type CronJobReference struct {
	// Name is the name of the CronJob in the same namespace as this CronJobMonitor.
	// Must be a DNS-1123 label (lowercase alphanumeric with hyphens, up to 63 chars).
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	Name string `json:"name"`
}

// CronJobMonitorSpec defines the desired SLO for a CronJob.
// +kubebuilder:validation:XValidation:rule="self.gracePeriodSeconds < 86400",message="gracePeriodSeconds must be less than 86400 (1 day)"
// +kubebuilder:validation:XValidation:rule="self.alertAfterMissedRuns <= self.historyLimit",message="alertAfterMissedRuns must be ≤ historyLimit (history must hold the missed-run threshold)"
// +kubebuilder:validation:XValidation:rule="self.maxConsecutiveFailures <= self.historyLimit",message="maxConsecutiveFailures must be ≤ historyLimit (history must hold the consecutive-failures threshold)"
type CronJobMonitorSpec struct {
	// CronJobRef points at the CronJob this monitor observes.
	// +kubebuilder:validation:Required
	CronJobRef CronJobReference `json:"cronJobRef"`

	// Schedule is the expected cron expression. When unset, the controller
	// falls back to the referenced CronJob's spec.schedule.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=128
	// +kubebuilder:validation:XValidation:rule="self.startsWith('@') || self.startsWith('CRON_TZ=') || self.startsWith('TZ=') || self.matches('^\\\\S+\\\\s+\\\\S+\\\\s+\\\\S+\\\\s+\\\\S+\\\\s+\\\\S+$')",message="schedule must be 5 whitespace-separated tokens, an @descriptor (@hourly/@daily/@weekly/@monthly/@yearly), or a CRON_TZ=/TZ= prefixed expression (range/value validity is checked by the reconciler)"
	Schedule string `json:"schedule,omitempty"`

	// TimeZone is the IANA time-zone name (e.g. "America/New_York", "Europe/Moscow")
	// the schedule is evaluated in. When unset, the controller falls back to
	// the referenced CronJob's spec.timeZone, then to UTC. Has no effect when
	// the schedule expression already carries a CRON_TZ=/TZ= prefix.
	// +optional
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=64
	// +kubebuilder:validation:XValidation:rule="self == 'UTC' || self.matches('^[A-Z][A-Za-z0-9_+-]*(/[A-Z][A-Za-z0-9_+-]*)*$')",message="timeZone must be 'UTC' or an IANA name (single-segment like 'GMT' or multi-segment like 'America/New_York'); first character of each segment must be uppercase"
	TimeZone string `json:"timeZone,omitempty"`

	// MaxDurationSeconds is the SLO for a single Job's wall-clock duration.
	// When unset, the check is disabled.
	// +optional
	// +kubebuilder:validation:Minimum=1
	MaxDurationSeconds *int32 `json:"maxDurationSeconds,omitempty"`

	// MaxConsecutiveFailures is the number of consecutive failed runs that
	// flips ExecutionHealthy to False.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	MaxConsecutiveFailures int32 `json:"maxConsecutiveFailures,omitempty"`

	// AlertAfterMissedRuns is the number of consecutive missed expected runs
	// that flips ScheduleHealthy to False.
	// +kubebuilder:default=2
	// +kubebuilder:validation:Minimum=1
	AlertAfterMissedRuns int32 `json:"alertAfterMissedRuns,omitempty"`

	// GracePeriodSeconds is the tolerance window after an expected run before
	// the run is considered missed.
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=0
	GracePeriodSeconds int32 `json:"gracePeriodSeconds,omitempty"`

	// HistoryLimit is the maximum number of recent executions kept in status.
	// +kubebuilder:default=10
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	HistoryLimit int32 `json:"historyLimit,omitempty"`
}

// ExecutionPhase is a closed set of values for ExecutionRecord.Phase.
// +kubebuilder:validation:Enum=Succeeded;Failed;Running
type ExecutionPhase string

// ExecutionPhase values reported in ExecutionRecord.Phase.
const (
	ExecutionPhaseSucceeded ExecutionPhase = "Succeeded"
	ExecutionPhaseFailed    ExecutionPhase = "Failed"
	ExecutionPhaseRunning   ExecutionPhase = "Running"
)

// ExecutionRecord summarises a single Job execution observed by the controller.
type ExecutionRecord struct {
	JobName           string         `json:"jobName"`
	StartTime         metav1.Time    `json:"startTime"`
	EndTime           *metav1.Time   `json:"endTime,omitempty"`
	ExpectedStartTime *metav1.Time   `json:"expectedStartTime,omitempty"`
	DurationSeconds   *int32         `json:"durationSeconds,omitempty"`
	DriftSeconds      *int32         `json:"driftSeconds,omitempty"`
	Phase             ExecutionPhase `json:"phase"`
}

// CronJobMonitorStatus captures the observed SLO state of the monitored CronJob.
type CronJobMonitorStatus struct {
	// ObservedGeneration matches metadata.generation after a successful reconcile.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions reflect the current SLO evaluation.
	// +optional
	// +patchMergeKey=type
	// +patchStrategy=merge
	// +listType=map
	// +listMapKey=type
	// +kubebuilder:validation:MaxItems=10
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// ResolvedSchedule is the schedule used for evaluation (spec.schedule or
	// CronJob.spec.schedule).
	// +optional
	ResolvedSchedule *string `json:"resolvedSchedule,omitempty"`

	// ResolvedTimeZone is the IANA time zone the schedule was evaluated in
	// (spec.timeZone, or the referenced CronJob's spec.timeZone, or UTC).
	// +optional
	ResolvedTimeZone *string `json:"resolvedTimeZone,omitempty"`

	// +optional
	LastScheduleTime *metav1.Time `json:"lastScheduleTime,omitempty"`
	// +optional
	LastSuccessTime *metav1.Time `json:"lastSuccessTime,omitempty"`
	// +optional
	LastFailureTime *metav1.Time `json:"lastFailureTime,omitempty"`
	// +optional
	NextExpectedTime *metav1.Time `json:"nextExpectedTime,omitempty"`

	// ConsecutiveFailures counts terminal failures in a row; resets on success.
	ConsecutiveFailures int32 `json:"consecutiveFailures"`
	// MissedRuns counts consecutive expected runs that did not start within grace.
	MissedRuns int32 `json:"missedRuns"`
	// ScheduleDriftSeconds is the drift of the most recent run.
	ScheduleDriftSeconds int32 `json:"scheduleDriftSeconds"`

	// RecentExecutions is a newest-first ring buffer of size HistoryLimit.
	// Server-side apply treats this as atomic — a writer fully replaces the
	// list rather than merging. The reconciler is the only writer in
	// practice.
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=100
	RecentExecutions []ExecutionRecord `json:"recentExecutions,omitempty"`
}

// Condition type constants.
const (
	ConditionReconciled       = "Reconciled"
	ConditionScheduleHealthy  = "ScheduleHealthy"
	ConditionExecutionHealthy = "ExecutionHealthy"
	ConditionDurationHealthy  = "DurationHealthy"
	ConditionReady            = "Ready"
)

// Condition reason constants.
const (
	ReasonReconcileSuccess    = "ReconcileSuccess"
	ReasonInvalidSchedule     = "InvalidSchedule"
	ReasonInvalidTimeZone     = "InvalidTimeZone"
	ReasonCronJobNotFound     = "CronJobNotFound"
	ReasonCronJobSuspended    = "CronJobSuspended"
	ReasonScheduleMismatch    = "ScheduleMismatch"
	ReasonOnSchedule          = "OnSchedule"
	ReasonScheduleMissed      = "ScheduleMissed"
	ReasonSuspended           = "Suspended"
	ReasonNoSchedule          = "NoSchedule"
	ReasonRecentSuccess       = "RecentSuccess"
	ReasonConsecutiveFailures = "ConsecutiveFailures"
	ReasonNoRuns              = "NoRuns"
	ReasonWithinBudget        = "WithinBudget"
	ReasonDurationExceeded    = "DurationExceeded"
	ReasonCheckDisabled       = "CheckDisabled"
	ReasonAllChecksPass       = "AllChecksPass"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Namespaced,shortName=cjmon;cjm,categories=monitoring
// +kubebuilder:printcolumn:name="Schedule",type=string,JSONPath=`.status.resolvedSchedule`
// +kubebuilder:printcolumn:name="LastSuccess",type=date,JSONPath=`.status.lastSuccessTime`
// +kubebuilder:printcolumn:name="ConsecFails",type=integer,JSONPath=`.status.consecutiveFailures`
// +kubebuilder:printcolumn:name="Missed",type=integer,JSONPath=`.status.missedRuns`
// +kubebuilder:printcolumn:name="Ready",type=string,JSONPath=`.status.conditions[?(@.type=="Ready")].status`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// CronJobMonitor declares an SLO for a CronJob and records its observed state.
type CronJobMonitor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CronJobMonitorSpec   `json:"spec,omitempty"`
	Status CronJobMonitorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CronJobMonitorList contains a list of CronJobMonitor.
type CronJobMonitorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CronJobMonitor `json:"items"`
}

// Type registration moved to groupversion_info.go's addKnownTypes() so the
// API package no longer depends on controller-runtime's deprecated
// scheme.Builder helper.
