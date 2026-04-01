{{/*
Expand the name of the chart.
*/}}
{{- define "daedalus.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
Truncate at 63 chars because some Kubernetes name fields are limited.
If release name contains chart name it will be used as a full name.
*/}}
{{- define "daedalus.fullname" -}}
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
{{- define "daedalus.chart" -}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Common labels applied to every resource.
*/}}
{{- define "daedalus.labels" -}}
helm.sh/chart: {{ include "daedalus.chart" . }}
{{ include "daedalus.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels - used in matchLabels and pod template labels.
Accepts a dict with "root" (top-level .) and "worker" (worker config).
*/}}
{{- define "daedalus.selectorLabels" -}}
app.kubernetes.io/name: {{ include "daedalus.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
Worker-scoped selector labels for a specific agent type.
Call with (dict "root" . "worker" $worker).
*/}}
{{- define "daedalus.workerSelectorLabels" -}}
app.kubernetes.io/name: {{ include "daedalus.name" .root }}
app.kubernetes.io/instance: {{ .root.Release.Name }}
app.kubernetes.io/component: {{ .worker.name }}
{{- end }}

{{/*
Full name for a worker resource: "<fullname>-<worker-name>"
Call with (dict "root" . "worker" $worker).
*/}}
{{- define "daedalus.workerFullname" -}}
{{- printf "%s-%s" (include "daedalus.fullname" .root) .worker.name | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Resolve the NATS URL dynamically.
When nats.enabled=true, compute the service name from the release name (bitnami/nats creates <release-name>-nats).
When nats.enabled=false, fall back to nats.url for external broker configuration.
*/}}
{{- define "daedalus.natsURL" -}}
{{- if .Values.nats.enabled }}
{{- printf "nats://%s-nats:4222" .Release.Name }}
{{- else }}
{{- .Values.nats.url }}
{{- end }}
{{- end }}

{{/*
Resolve the NATS monitoring endpoint dynamically.
When nats.enabled=true, compute from the release name (bitnami/nats exposes :8222 by default).
When nats.enabled=false, fall back to nats.monitoringEndpoint for external NATS.
*/}}
{{- define "daedalus.natsMonitoringURL" -}}
{{- if .Values.nats.enabled }}
{{- printf "http://%s-nats:8222" .Release.Name }}
{{- else }}
{{- .Values.nats.monitoringEndpoint }}
{{- end }}
{{- end }}
