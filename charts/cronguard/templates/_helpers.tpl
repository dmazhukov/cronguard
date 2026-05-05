{{/*
Expand the name of the chart.
*/}}
{{- define "cronguard.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "cronguard.fullname" -}}
{{- if .Values.fullnameOverride }}
{{- .Values.fullnameOverride | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- $name := default .Chart.Name .Values.nameOverride }}
{{- if contains $name .Release.Name }}
{{- .Release.Name | trunc 63 | trimSuffix "-" }}
{{- else }}
{{- printf "%s-%s" .Release.Name $name | trunc 63 | trimSuffix "-" }}
{{- end }}
{{- end }}
{{- end }}

{{/*
Chart name and version label.
*/}}
{{- define "cronguard.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels.
*/}}
{{- define "cronguard.labels" -}}
helm.sh/chart: {{ include "cronguard.chart" . }}
{{ include "cronguard.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
app.kubernetes.io/part-of: cronguard
{{- end }}

{{/*
Selector labels.

Hardcoded `name: cronguard` (literal) rather than `cronguard.name`
because selector labels on a Deployment are immutable after creation.
If a user re-rendered with a different `nameOverride`, the rendered
selector would change and `helm upgrade` would fail with
`field is immutable: spec.selector`. Cosmetic label customization
stays possible via `cronguard.labels` (which still includes the
`nameOverride`-aware components for non-selector tags).
*/}}
{{- define "cronguard.selectorLabels" -}}
app.kubernetes.io/name: cronguard
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "cronguard.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "cronguard.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}
