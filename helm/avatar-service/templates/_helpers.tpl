{{/*
Expand the name of the chart.
*/}}
{{- define "avatar-service.name" -}}
{{- .Chart.Name }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "avatar-service.fullname" -}}
{{- if contains .Chart.Name .Release.Name }}
{{- .Release.Name }}
{{- else }}
{{- printf "%s-%s" .Release.Name .Chart.Name }}
{{- end }}
{{- end }}

{{/*
Common labels
*/}}
{{- define "avatar-service.labels" -}}
helm.sh/chart: {{ .Chart.Name }}-{{ .Chart.Version }}
{{ include "avatar-service.selectorLabels" . }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
{{- end }}

{{/*
Selector labels
*/}}
{{- define "avatar-service.selectorLabels" -}}
app.kubernetes.io/name: {{ include "avatar-service.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
app: {{ include "avatar-service.name" . }}
{{- end }}
