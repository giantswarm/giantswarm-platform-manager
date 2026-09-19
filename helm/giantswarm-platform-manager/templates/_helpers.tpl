{{/*
Expand the name of the chart.
*/}}
{{- define "giantswarm-platform-manager.name" -}}
{{- default .Chart.Name .Values.nameOverride | trunc 63 | trimSuffix "-" }}
{{- end }}

{{/*
Create a default fully qualified app name.
*/}}
{{- define "giantswarm-platform-manager.fullname" -}}
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
Chart label value.
*/}}
{{- define "giantswarm-platform-manager.chart" -}}
{{- /* A label value ends alphanumeric: the 63-char cut of a branch build's
       version (0.x.y-dev.<branch>.<timestamp>.<sha>) can land on a "." too. */}}
{{- printf "%s-%s" .Chart.Name .Chart.Version | replace "+" "_" | trunc 63 | trimSuffix "-" | trimSuffix "." }}
{{- end }}

{{/*
Common labels. The team label is what the Giant Swarm chart validator reads.
*/}}
{{- define "giantswarm-platform-manager.labels" -}}
helm.sh/chart: {{ include "giantswarm-platform-manager.chart" . }}
{{ include "giantswarm-platform-manager.selectorLabels" . }}
{{- if .Chart.AppVersion }}
app.kubernetes.io/version: {{ .Chart.AppVersion | quote }}
{{- end }}
app.kubernetes.io/managed-by: {{ .Release.Service }}
application.giantswarm.io/team: {{ index .Chart.Annotations "io.giantswarm.application.team" | quote }}
{{- end }}

{{/*
Selector labels.
*/}}
{{- define "giantswarm-platform-manager.selectorLabels" -}}
app.kubernetes.io/name: {{ include "giantswarm-platform-manager.name" . }}
app.kubernetes.io/instance: {{ .Release.Name }}
{{- end }}

{{/*
ServiceAccount name.
*/}}
{{- define "giantswarm-platform-manager.serviceAccountName" -}}
{{- if .Values.serviceAccount.create }}
{{- default (include "giantswarm-platform-manager.fullname" .) .Values.serviceAccount.name }}
{{- else }}
{{- default "default" .Values.serviceAccount.name }}
{{- end }}
{{- end }}

{{/*
The in-cluster URL muster reaches this server at, without the MCP path.
*/}}
{{- define "giantswarm-platform-manager.serviceURL" -}}
{{- printf "http://%s.%s.svc.cluster.local:%v" (include "giantswarm-platform-manager.fullname" .) .Release.Namespace .Values.service.port }}
{{- end }}

{{/*
The OAuth base URL — the resource of the protected-resource metadata:
oauth.baseURL, else the in-cluster Service URL.
*/}}
{{- define "giantswarm-platform-manager.oauthBaseURL" -}}
{{- .Values.oauth.baseURL | default (include "giantswarm-platform-manager.serviceURL" .) }}
{{- end }}

{{/*
The audiences the live surface trusts, comma-separated: live.audiences and
the live registration's requiredAudiences, in that order, without duplicates.
Empty when neither names one.
*/}}
{{- define "giantswarm-platform-manager.liveAudiences" -}}
{{- concat (default (list) .Values.live.audiences) (default (list) .Values.muster.liveServer.requiredAudiences) | uniq | join "," -}}
{{- end }}

{{/*
muster's own MCP endpoint the live reads loop back to: live.muster.url, else
muster's Service in the release namespace.
*/}}
{{- define "giantswarm-platform-manager.musterURL" -}}
{{- .Values.live.muster.url | default (printf "http://muster.%s.svc.cluster.local:8090/mcp" .Release.Namespace) }}
{{- end }}
