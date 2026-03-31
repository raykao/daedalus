{{/*
Expand the name of the chart.
*/}}
{{- define "agent-forge.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncate at 63 chars because some Kubernetes name fields are limited.
If release name contains chart name it will be used as a full name.
*/}}
{{- define "agent-forge.fullname" -}}
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
Create chart label value: "<chart-name>-<chart-version>"
*/}}
{{- define "agent-forge.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "agent-forge.labels" -}}
helm.sh/chart: {{ include "agent-forge.chart" . }}
{{ include "agent-forge.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels - used in matchLabels and pod template labels.
Accepts a dict with "root" (top-level .) and "worker" (worker config).
*/}}
{{- define "agent-forge.selectorLabels" -}}
app.kubernetes.io/name: {{ include "agent-forge.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Worker-scoped selector labels for a specific agent type.
Call with (dict "root" . "worker" $worker).
*/}}
{{- define "agent-forge.workerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "agent-forge.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .worker.name }}
{{- end }}

{{/*
Full name for a worker resource: "<fullname>-<worker-name>"
Call with (dict "root" . "worker" $worker).
*/}}
{{- define "agent-forge.workerFullname" -}}
{{- printf "%s-%s" (include "agent-forge.fullname" .root) .worker.name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resolve the NATS URL: use nats.url value regardless of whether the subchart is enabled.
Callers may override nats.url to point at an external broker.
*/}}
{{- define "agent-forge.natsURL" -}}
{{- .Values.nats.url }}
{{- end }}
